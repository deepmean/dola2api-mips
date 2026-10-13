package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Upstream talks to the dola API through the egress layer.
//
// Only the parts the router can actually do are here. Polling and media
// retrieval need no browser and no signature: /im/chain/single is not on the
// mssdk's signed path list, which is why this half of the pipeline works
// outside a browser while the submit does not.
type Upstream struct {
	egress *Egress
	// tabID is generated once per process, as the page generates one per tab.
	tabID string
	// base is the API origin. Tests point it at a fake server.
	base string
	// now is injectable so tests can pin timestamps.
	now func() time.Time
}

// defaultBase is the upstream API origin.
const defaultBase = "https://www.dola.com"

// NewUpstream builds the client.
//
// The session cookie is a parameter of each call rather than a field: the pool
// picks an account per task, and polling a conversation has to use the same
// account that created it.
func NewUpstream(egress *Egress) *Upstream {
	return &Upstream{
		egress: egress,
		tabID:  newUUID(),
		base:   defaultBase,
		now:    time.Now,
	}
}

// Video is one generated video found in a poll response.
type Video struct {
	// DownloadURL is the watermarked CDN link.
	DownloadURL string
	// UnwatermarkedURL is the original, base64-encoded inside video_model.
	UnwatermarkedURL string
	// Cover is the poster frame, when the response carries one.
	Cover string
	// Model is the model name reported for the video.
	Model string
}

// PollResult is what one poll of a conversation yielded.
type PollResult struct {
	ConversationID string
	Messages       int
	Texts          []string
	Videos         []Video
	// Finished reports whether the response looked complete rather than partial.
	Finished bool
}

// maxPollBody caps how much of a poll response is read. A poll response is
// JSON with every message in the conversation, so an unbounded read is a way to
// exhaust a 250MB router.
const maxPollBody = 2 << 20

// Poll reads a conversation once, as the account that owns it.
func (u *Upstream) Poll(ctx context.Context, cookie, conversationID string) (PollResult, error) {
	if conversationID == "" {
		return PollResult{}, errors.New("poll: empty conversation id")
	}
	body, err := buildPollBody(conversationID, u.tabID)
	if err != nil {
		return PollResult{}, err
	}

	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json; encoding=utf-8")
	hdr.Set("agw-js-conv", "str")
	hdr.Set("Accept", "*/*")
	hdr.Set("Cookie", cookie)

	resp, err := u.egress.Do(ctx, http.MethodPost, u.pollURL(), hdr, body)
	if err != nil {
		return PollResult{}, fmt.Errorf("poll: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPollBody))
	if err != nil {
		return PollResult{}, fmt.Errorf("poll: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return PollResult{}, fmt.Errorf("poll: upstream status %d: %s", resp.StatusCode, firstLine(raw, 160))
	}
	result, err := extractPoll(conversationID, raw)
	if err != nil {
		return PollResult{}, err
	}
	return result, nil
}

// pollURL builds the request URL for one poll.
func (u *Upstream) pollURL() string {
	params := url.Values{}
	for k, v := range map[string]string{
		"version_code": "20800", "language": "ja", "device_platform": "web",
		"doubao_device_platform": "web", "aid": "495671", "real_aid": "495671",
		"pkg_type": "release_version", "pc_version": "3.32.61", "doubao_pc_version": "3.32.61",
		"region": "JP", "sys_region": "JP", "samantha_web": "1",
		"web_platform": "browser", "use-olympus-account": "1", "web_tab_id": u.tabID,
	} {
		params.Set(k, v)
	}
	return u.base + "/im/chain/single?" + params.Encode()
}

// buildPollBody is the pull_singe_chain request. anchor_index is
// Number.MAX_SAFE_INTEGER because that is what the page sends to mean "from the
// newest message".
func buildPollBody(conversationID, tabID string) ([]byte, error) {
	payload := map[string]any{
		"cmd": 3100,
		"uplink_body": map[string]any{
			"pull_singe_chain_uplink_body": map[string]any{
				"conversation_id":        conversationID,
				"anchor_index":           int64(9007199254740991),
				"conversation_type":      3,
				"direction":              1,
				"limit":                  20,
				"ext":                    map[string]any{},
				"filter":                 map[string]any{"index_list": []any{}},
				"evaluate_ab_params":     "",
				"evaluate_common_params": "",
			},
		},
		"sequence_id": newUUID(),
		"channel":     2,
		"version":     "1",
	}
	return json.Marshal(payload)
}

// pollResponse decodes only the fields that matter. Unknown fields are skipped
// without allocating, which matters because a poll response carries every
// message in the conversation.
type pollResponse struct {
	DownlinkBody struct {
		Chain struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		} `json:"pull_singe_chain_downlink_body"`
	} `json:"downlink_body"`
}

// contentBlock is the subset of a content block the router reads.
type contentBlock struct {
	BlockType int `json:"block_type"`
	Content   struct {
		TextBlock struct {
			Text string `json:"text"`
		} `json:"text_block"`
		CreationBlock struct {
			Creations []struct {
				Type  int `json:"type"`
				Video struct {
					DownloadURL string `json:"download_url"`
					Cover       string `json:"cover_url"`
					VideoModel  string `json:"video_model"`
				} `json:"video"`
			} `json:"creations"`
		} `json:"creation_block"`
	} `json:"content"`
}

// blockTypeCreation is the block type that carries generated media.
const blockTypeCreation = 2074

// creationTypeVideo marks a video creation inside a creation block.
const creationTypeVideo = 2

// extractPoll pulls the text and videos out of a poll response.
func extractPoll(conversationID string, raw []byte) (PollResult, error) {
	var parsed pollResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return PollResult{}, fmt.Errorf("poll: decode: %w", err)
	}
	messages := parsed.DownlinkBody.Chain.Messages

	result := PollResult{ConversationID: conversationID, Messages: len(messages)}
	for _, msg := range messages {
		blocks, err := decodeContent(msg.Content)
		if err != nil {
			// One unreadable message must not discard the whole response.
			continue
		}
		for _, block := range blocks {
			if text := block.Content.TextBlock.Text; text != "" {
				result.Texts = append(result.Texts, text)
			}
			if block.BlockType != blockTypeCreation {
				continue
			}
			for _, creation := range block.Content.CreationBlock.Creations {
				if creation.Type != creationTypeVideo {
					continue
				}
				video := Video{
					DownloadURL: creation.Video.DownloadURL,
					Cover:       creation.Video.Cover,
				}
				if model := creation.Video.VideoModel; model != "" {
					video.Model, video.UnwatermarkedURL = decodeVideoModel(model)
				}
				if video.DownloadURL != "" || video.UnwatermarkedURL != "" {
					result.Videos = append(result.Videos, video)
				}
			}
		}
	}
	return result, nil
}

// decodeContent handles a message content that is either a JSON array or a JSON
// string containing one, which is how the API varies between responses.
func decodeContent(raw json.RawMessage) ([]contentBlock, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return nil, err
		}
		trimmed = inner
	}
	if !strings.HasPrefix(strings.TrimSpace(trimmed), "[") {
		return nil, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal([]byte(trimmed), &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// decodeVideoModel reads video_model, whose video_list values carry a
// base64-encoded main_url that is the unwatermarked original.
func decodeVideoModel(model string) (modelName, unwatermarked string) {
	var parsed struct {
		Model     string `json:"model"`
		VideoList map[string]struct {
			MainURL string `json:"main_url"`
		} `json:"video_list"`
	}
	if err := json.Unmarshal([]byte(model), &parsed); err != nil {
		return "", ""
	}
	modelName = parsed.Model
	for _, entry := range parsed.VideoList {
		if entry.MainURL == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(entry.MainURL)
		if err != nil {
			continue
		}
		if candidate := string(decoded); strings.HasPrefix(candidate, "http") {
			return modelName, candidate
		}
	}
	return modelName, ""
}

// newUUID returns a random RFC 4122 version 4 identifier.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A UUID that is merely unique is still usable; never fail a request
		// because the system entropy source hiccupped.
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (i % 8 * 8))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Submit posts a video request and returns the conversation id.
//
// The response is an SSE stream: the conversation id arrives in the SSE_ACK
// event, and a refusal arrives in STREAM_ERROR. Only the first acknowledgement
// is needed, so the stream is read just far enough and then closed rather than
// drained 鈥?the generation continues upstream and is picked up by Poll.
func (u *Upstream) Submit(ctx context.Context, cookie string, req SubmitRequest) (string, error) {
	body, err := buildSubmitBody(req, u.now())
	if err != nil {
		return "", err
	}
	aBogus, err := PlaceholderABogus()
	if err != nil {
		return "", err
	}

	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("agw-js-conv", "str, str")
	hdr.Set("Accept", "*/*")
	hdr.Set("Cookie", cookie)

	resp, err := u.egress.Do(ctx, http.MethodPost, u.submitURL(aBogus), hdr, body)
	if err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReaderSize(io.LimitReader(resp.Body, maxSubmitBody), 8<<10)
	conversationID, streamErr := readAck(reader)
	if streamErr != nil {
		return "", fmt.Errorf("submit: %w", streamErr)
	}
	if conversationID == "" {
		return "", fmt.Errorf("submit: upstream status %d without an acknowledgement: %s",
			resp.StatusCode, firstLine(body, 0))
	}
	return conversationID, nil
}

// maxSubmitBody caps how much of the submit stream is read. Only the opening
// events matter, so this is generous but bounded.
const maxSubmitBody = 256 << 10

// submitURL builds the submit URL with the parameter set the page sends.
func (u *Upstream) submitURL(aBogus string) string {
	params := url.Values{}
	for k, v := range map[string]string{
		"aid": "495671", "channel": "g", "device_platform": "web",
		"language": "zh-Hant", "region": "JP", "sys_region": "JP",
		"samantha_web": "1", "use-olympus-account": "1", "version_code": "20800",
		"web_platform": "browser", "web_tab_id": u.tabID,
	} {
		params.Set(k, v)
	}
	params.Set("a_bogus", aBogus)
	return u.base + "/chat/completion?" + params.Encode()
}

// buildSubmitBody renders the request the page sends for a video generation.
func buildSubmitBody(req SubmitRequest, now time.Time) ([]byte, error) {
	if req.Prompt == "" {
		return nil, errors.New("submit: empty prompt")
	}
	ability := map[string]any{"ratio": req.Ratio, "model": req.Model, "duration": req.Duration}
	if len(req.ReferenceImages) > 0 {
		// Image-to-video carries the frames as data URLs inside the same param.
		frames := make([]string, 0, len(req.ReferenceImages))
		for _, img := range req.ReferenceImages {
			frames = append(frames, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(img))
		}
		ability["reference_images"] = frames
	}
	abilityParam, err := json.Marshal(ability)
	if err != nil {
		return nil, fmt.Errorf("submit: ability param: %w", err)
	}

	nowMs := now.UnixMilli()
	payload := map[string]any{
		"client_meta": map[string]any{
			"local_conversation_id": fmt.Sprintf("local_%d", nowMs),
			"conversation_id":       "",
			"bot_id":                "7339470689562525703",
			"last_section_id":       "",
			"last_message_index":    nil,
		},
		"messages": []any{map[string]any{
			"local_message_id": newUUID(),
			"content_block": []any{map[string]any{
				"block_type": 10000,
				"content": map[string]any{
					"text_block": map[string]any{
						"text": req.Prompt, "icon_url": "", "icon_url_dark": "", "summary": "",
					},
					"pc_event_block": "",
				},
				"block_id": newUUID(), "parent_id": "",
				"meta_info": []any{}, "append_fields": []any{},
			}},
			"message_status": 0,
		}},
		"option": map[string]any{
			"send_message_scene": "", "create_time_ms": nowMs, "collect_id": "", "is_audio": false,
			"answer_with_suggest": false, "tts_switch": false, "need_deep_think": 0,
			"click_clear_context": false, "from_suggest": false, "is_regen": false, "is_replace": false,
			"is_from_click_option": false, "disable_sse_cache": false, "select_text_action": "",
			"is_select_text": false, "resend_for_regen": false, "scene_type": 0,
			"unique_key": newUUID(), "start_seq": 0, "need_create_conversation": true,
			"conversation_init_option": map[string]any{"need_ack_conversation": true},
			"regen_query_id":           []any{}, "edit_query_id": []any{}, "regen_instruction": "",
			"no_replace_for_regen": false, "message_from": 0, "shared_app_name": "", "shared_app_id": "",
			"sse_recv_event_options": map[string]any{"support_chunk_delta": true},
			"is_ai_playground":       false, "is_old_user": false,
			"recovery_option": map[string]any{
				"is_recovery": false, "req_create_time_sec": nowMs / 1000, "append_sse_event_scene": 0,
			},
			"message_storage_type": 0,
		},
		"chat_ability": map[string]any{
			"ability_type": 17, "ability_param": string(abilityParam),
		},
		"user_context": []any{},
		"ext": map[string]any{
			"fp": "", "use_deep_think": "0", "sub_conv_firstmet_type": "1", "collection_id": "",
			"conversation_init_option":      `{"need_ack_conversation":true}`,
			"commerce_credit_config_enable": "0",
		},
	}
	return json.Marshal(payload)
}

// readAck scans an SSE stream for the acknowledgement or a refusal.
func readAck(r *bufio.Reader) (string, error) {
	var (
		event string
		data  strings.Builder
	)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				// End of an event: act on what was collected.
				if id, stop, parseErr := handleEvent(event, data.String()); stop {
					return id, parseErr
				}
				event, data = "", strings.Builder{}
			case strings.HasPrefix(trimmed, "event:"):
				event = strings.TrimSpace(trimmed[len("event:"):])
			case strings.HasPrefix(trimmed, "data:"):
				data.WriteString(strings.TrimSpace(trimmed[len("data:"):]))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", nil
			}
			return "", err
		}
	}
}

// handleEvent reports whether the event ends the read, and why.
func handleEvent(event, data string) (conversationID string, stop bool, err error) {
	switch event {
	case "SSE_ACK":
		var ack struct {
			AckClientMeta struct {
				ConversationID string `json:"conversation_id"`
			} `json:"ack_client_meta"`
		}
		if json.Unmarshal([]byte(data), &ack) == nil {
			return ack.AckClientMeta.ConversationID, true, nil
		}
		return "", true, nil
	case "STREAM_ERROR":
		var failure struct {
			ErrorCode any    `json:"error_code"`
			ErrorMsg  string `json:"error_msg"`
		}
		if json.Unmarshal([]byte(data), &failure) == nil {
			return "", true, fmt.Errorf("upstream refused: %s %s",
				formatErrorCode(failure.ErrorCode), failure.ErrorMsg)
		}
		return "", true, errors.New("upstream refused with an unreadable error")
	}
	return "", false, nil
}

// formatErrorCode renders error_code, which arrives as a JSON number.
//
// Printing it with %v gives scientific notation, and 7.10022002e+08 is useless
// in a log: the number that identifies the refusal is 710022002. An on-device
// acceptance run is what surfaced this, which is why the codes are worth
// rendering exactly.
func formatErrorCode(value any) string {
	switch v := value.(type) {
	case nil:
		return "(none)"
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func firstLine(raw []byte, limit int) string {
	text := string(raw)
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	if limit > 0 && len(text) > limit {
		text = text[:limit]
	}
	return text
}
