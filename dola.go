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
	// imageSkill is the ability_type used for image requests.
	imageSkill int
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
		egress:     egress,
		tabID:      newUUID(),
		base:       defaultBase,
		imageSkill: skillImage,
		now:        time.Now,
	}
}

// Media is one generated item found in a poll response: a video or an image.
type Media struct {
	// Kind is "video" or "image".
	Kind string
	// URL is the best available link: the unwatermarked original for a video,
	// the full-size original for an image.
	URL string
	// WatermarkedURL is the CDN link the web client shows, when present.
	WatermarkedURL string
	// Cover is a poster or thumbnail, when the response carries one.
	Cover string
	// Model is the model name reported for the item.
	Model string
}

// PollResult is what one poll of a conversation yielded.
type PollResult struct {
	ConversationID string
	Messages       int
	Texts          []string
	Media          []Media
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
			Creations []creation `json:"creations"`
		} `json:"creation_block"`
	} `json:"content"`
}

// creation is one generated item inside a creation block.
type creation struct {
	// Type is 1 for an image and 2 for a video, which is what the poll response
	// uses to say what came back.
	Type  int `json:"type"`
	Video struct {
		DownloadURL string `json:"download_url"`
		Cover       string `json:"cover_url"`
		VideoModel  string `json:"video_model"`
	} `json:"video"`
	Image struct {
		ImageThumb struct {
			URL string `json:"url"`
		} `json:"image_thumb"`
		ImageOri struct {
			URL string `json:"url"`
		} `json:"image_ori"`
		// ImageOriRaw is the unwatermarked original. image_ori looks like the
		// better field and is the watermarked rendition, which is the same trap
		// the video path has: prefer the raw one, fall back to the pretty one.
		ImageOriRaw struct {
			URL string `json:"url"`
		} `json:"image_ori_raw"`
		ImagePreview struct {
			URL string `json:"url"`
		} `json:"image_preview"`
	} `json:"image"`
}

const (
	// blockTypeCreation is the block type that carries generated media.
	blockTypeCreation = 2074
	// creationTypeImage and creationTypeVideo are the two values observed in a
	// creation block. Both come back from the same submit: a prompt that asks
	// for a video yields type 2, and one that does not yields a batch of type 1
	// images, so the poll has to look for both rather than assume.
	creationTypeImage = 1
	creationTypeVideo = 2
)

// blockTypeThinking carries a reasoning block.
const blockTypeThinking = 10040

// extractPoll pulls the text and the generated media out of a poll response.
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
			for _, item := range block.Content.CreationBlock.Creations {
				if media, ok := mediaOf(item); ok {
					result.Media = append(result.Media, media)
				}
			}
		}
	}
	return result, nil
}

// mediaOf turns one creation into a Media, or reports that it carries nothing.
func mediaOf(item creation) (Media, bool) {
	switch item.Type {
	case creationTypeVideo:
		media := Media{
			Kind:           "video",
			WatermarkedURL: item.Video.DownloadURL,
			Cover:          item.Video.Cover,
		}
		if model := item.Video.VideoModel; model != "" {
			media.Model, media.URL = decodeVideoModel(model)
		}
		if media.URL == "" {
			// The unwatermarked original is preferred but not guaranteed; the
			// watermarked link is still a usable answer.
			media.URL = media.WatermarkedURL
		}
		return media, media.URL != ""
	case creationTypeImage:
		// Prefer the unwatermarked original. image_ori is the watermarked
		// rendition despite the name; image_ori_raw is the original. The thumb
		// and preview are smaller renditions of the same picture and are only
		// a last resort.
		url := item.Image.ImageOriRaw.URL
		watermarked := item.Image.ImageOri.URL
		if url == "" {
			url = watermarked
		}
		if url == "" {
			url = item.Image.ImagePreview.URL
		}
		if url == "" {
			url = item.Image.ImageThumb.URL
		}
		if url == "" {
			return Media{}, false
		}
		return Media{
			Kind:           "image",
			URL:            url,
			WatermarkedURL: watermarked,
			Cover:          item.Image.ImageThumb.URL,
		}, true
	default:
		return Media{}, false
	}
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

// VerifySession reports whether a session cookie is alive.
//
// It reads the recent-conversation list: a small authenticated call that needs
// no signature, and the same one the standalone probe uses. This is what makes
// pasting a cookie into the panel useful rather than a guess 闂?a dead session
// would otherwise only surface later, as an account parked by the cooldown with
// an upstream error that looks like a rate limit.
func (u *Upstream) VerifySession(ctx context.Context, cookie string) error {
	if strings.TrimSpace(cookie) == "" {
		return errors.New("verify: empty cookie")
	}
	if !strings.Contains(cookie, "sessionid=") {
		return errors.New("verify: the cookie has no sessionid, so it cannot be a login")
	}

	body, err := buildRecentConvBody()
	if err != nil {
		return err
	}

	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json; encoding=utf-8")
	hdr.Set("agw-js-conv", "str")
	hdr.Set("Accept", "*/*")
	hdr.Set("Cookie", cookie)

	resp, err := u.egress.Do(ctx, http.MethodPost,
		u.base+"/im/chain/recent_conv?"+sessionQuery(u.tabID, cookie).Encode(), hdr, body)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return fmt.Errorf("verify: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("verify: upstream status %d: %s", resp.StatusCode, firstLine(raw, 160))
	}

	// The failure signal on this endpoint is status_code, not code, and a dead
	// session answers 200 with downlink_body as an EMPTY OBJECT rather than
	// null. Checking the wrong field made verification a false positive: a
	// sessionid of deadbeef passed, which is the opposite of the point.
	var parsed struct {
		StatusCode int    `json:"status_code"`
		StatusDesc string `json:"status_desc"`
		Code       int    `json:"code"`
		Msg        string `json:"msg"`
		Downlink   struct {
			Chain json.RawMessage `json:"pull_recent_conv_chain_downlink_body"`
		} `json:"downlink_body"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("verify: upstream sent something unreadable: %s", firstLine(raw, 120))
	}
	if parsed.StatusCode != 0 {
		return fmt.Errorf("verify: the session was refused: %d %s", parsed.StatusCode, parsed.StatusDesc)
	}
	if parsed.Code != 0 {
		return fmt.Errorf("verify: the session was refused: %d %s", parsed.Code, parsed.Msg)
	}
	// The conversation list is the proof of life. A live session always returns
	// one, even with no conversations in it.
	if len(parsed.Downlink.Chain) == 0 || string(parsed.Downlink.Chain) == "null" {
		return errors.New("verify: the session was accepted but no conversation list came back, which is what a dead session returns")
	}
	return nil
}

// sessionQuery is the parameter set these calls share. msToken and fp come from
// the cookie when it has them, since a request without them carries more risk.
func sessionQuery(tabID, cookie string) url.Values {
	params := url.Values{}
	for k, v := range map[string]string{
		"version_code": "20800", "language": "ja", "device_platform": "web",
		"doubao_device_platform": "web", "aid": "495671", "real_aid": "495671",
		"pkg_type": "release_version", "pc_version": "3.32.61", "doubao_pc_version": "3.32.61",
		"region": "JP", "sys_region": "JP", "samantha_web": "1",
		"web_platform": "browser", "use-olympus-account": "1", "web_tab_id": tabID,
	} {
		params.Set(k, v)
	}
	if token := cookieField(cookie, "msToken"); token != "" {
		params.Set("msToken", token)
	}
	if fp := cookieField(cookie, "s_v_web_id"); fp != "" {
		params.Set("fp", fp)
	}
	return params
}

// buildRecentConvBody is the small read that proves a session is alive.
func buildRecentConvBody() ([]byte, error) {
	payload := map[string]any{
		"cmd": 3200,
		"uplink_body": map[string]any{
			"pull_recent_conv_chain_uplink_body": map[string]any{
				"limit": 5, "message_count_per_conv": 1, "api_version": 1,
				"conv_version": 0, "direction": 3,
				"option": map[string]any{
					"not_need_message": true, "need_complete_conversation": true,
					"need_coco_conversation": true, "need_coco_bot": true,
					"need_pc_pin_chain": true, "pc_pin_query_type": 0,
				},
			},
		},
		"sequence_id": newUUID(),
		"channel":     2,
		"version":     "1",
	}
	return json.Marshal(payload)
}

// SubmitRequest is one request, already validated by the API layer.
type SubmitRequest struct {
	Prompt   string
	Ratio    string
	Model    string
	Duration int
	// Kind is "video" (the default) or "image".
	Kind string
	// Style is an image style, only meaningful when Kind is "image".
	Style string
	// ReferenceImages are optional source images.
	ReferenceImages [][]byte
}

// Skill types, as the upstream names them. ability_type carries the skill, and
// the server derives input_skill.skill_id from it: a poll of a conversation this
// port created comes back with ext.input_skill = {"skill_id":"17"} even though
// this port never sends that field.
const (
	// skillVideo is the video-generation skill.
	skillVideo = 17
	// skillImage is image generation. The upstream Python used 16, which is
	// SkillCodeAssistant: /samantha/skill/pack answers 710010702 for 16 and 0
	// for 3, and the skill table reports 3 as "闂傚倷绶氬濠氭⒔閸曨垱鍋嬫繝濠傚濞呯娀鏌″搴″箺闁稿鍔戦弻锝夊籍閸屻倗鍔搁梺?. That Python branch had
	// no callers, so it was never validated.
	skillImage = 3
)

// wirePrompt phrases the prompt the way the web client does.
//
// This is the single most important detail of the submit: the agent reads the
// prompt to decide what to produce. The client sends
// "Generate video: <prompt>, <ratio>", and a request that says anything else
// comes back as a batch of images 闂?which is exactly what happened here, and
// looked for a long time like a broken poll rather than a wrong prompt.
func wirePrompt(req SubmitRequest) string {
	if req.Kind == "image" {
		return req.Prompt
	}
	return fmt.Sprintf("Generate video: %s, %s", req.Prompt, req.Ratio)
}

// abilityFor builds chat_ability for a request.
//
// The parameter keys differ per skill, and the difference is not cosmetic: the
// video skill takes {model, ratio, duration} and the image skill takes
// {ratio, style}. Sending the wrong set is accepted and then produces nothing.
func abilityFor(req SubmitRequest, imageSkill int) (int, string) {
	param := map[string]any{"ratio": req.Ratio}
	skill := skillVideo
	if req.Kind == "image" {
		skill = imageSkill
		if req.Style != "" {
			param["style"] = req.Style
		}
	} else {
		param["model"] = req.Model
		param["duration"] = req.Duration
	}
	if len(req.ReferenceImages) > 0 {
		// A reference image rides along inside the same parameter object.
		frames := make([]string, 0, len(req.ReferenceImages))
		for _, img := range req.ReferenceImages {
			frames = append(frames, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(img))
		}
		param["reference_images"] = frames
	}
	encoded, err := json.Marshal(param)
	if err != nil {
		// The map holds only strings, ints and slices of strings, so this cannot
		// fail; an empty parameter is a safer answer than a panic.
		return skill, "{}"
	}
	return skill, string(encoded)
}

// Submit posts a request and returns the conversation id.
//
// The response is an SSE stream: the conversation id arrives in the SSE_ACK
// event, and a refusal arrives in STREAM_ERROR. Only the first acknowledgement
// is needed, so the stream is read just far enough and then closed rather than
// drained 闂?the generation continues upstream and is picked up by Poll.
func (u *Upstream) Submit(ctx context.Context, cookie string, req SubmitRequest) (string, error) {
	body, err := buildSubmitBody(req, u.imageSkill, u.now())
	if err != nil {
		return "", err
	}

	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("agw-js-conv", "str, str")
	hdr.Set("Accept", "*/*")
	hdr.Set("Cookie", cookie)

	resp, err := u.egress.Do(ctx, http.MethodPost, u.submitURL(cookie), hdr, body)
	if err != nil {
		return "", fmt.Errorf("submit: %w", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReaderSize(io.LimitReader(resp.Body, maxSubmitBody), 8<<10)
	conversationID, streamErr := readAck(reader)
	if streamErr != nil {
		// A refusal says as much about the path as about the request. Parking
		// the relay that carried it means a retry through a different one is
		// genuinely a different attempt rather than the same one again.
		u.egress.MarkRefused(streamErr.Error())
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
//
// It deliberately carries no a_bogus. Measured on the live endpoint: a submit
// with no a_bogus at all is accepted and acknowledged, while one carrying a
// well-formed but meaningless a_bogus is refused with 710022002 闂?the same code
// a genuinely invalid signature gets. So the parameter is only validated when it
// is present, and sending a placeholder is worse than sending nothing.
//
// That is the whole of the signing question for this endpoint: the way to
// satisfy it is to omit it. An earlier reading of the same measurements
// concluded the opposite, because a service-side throttle was in force at the
// time and made every variant fail differently.
func (u *Upstream) submitURL(cookie string) string {
	// sessionQuery, not a hand-written map: it carries msToken and fp when the
	// session has them, which the read path always did and this one did not.
	// That is exactly the shape of a session that can be read but never writes,
	// and the verifier flagged the missing msToken as higher captcha risk from
	// the first run.
	return u.base + "/chat/completion?" + sessionQuery(u.tabID, cookie).Encode()
}

// buildSubmitBody renders the request the page sends for a video generation.
func buildSubmitBody(req SubmitRequest, imageSkill int, now time.Time) ([]byte, error) {
	if req.Prompt == "" {
		return nil, errors.New("submit: empty prompt")
	}
	abilityType, abilityParam := abilityFor(req, imageSkill)

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
						// The phrasing is what selects the output; see wirePrompt.
						"text": wirePrompt(req), "icon_url": "", "icon_url_dark": "", "summary": "",
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
			"ability_type": abilityType, "ability_param": abilityParam,
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

// ErrUpstreamRateLimited marks a submit refusal with the upstream's "cannot
// generate right now" codes.
//
// The name says rate, and the first reading of the evidence was that it was
// about submit rate 閳?the failover burned three submits in a row and all three
// were refused. Polling the one conversation that a submit did create showed
// what is actually happening: the reply is 娴犲﹤銇夐惃鍕晸閹存劖顐奸弫绋垮嚒缂嶆捇浠堥崚棰佺瑐闂勬劧绱濋弰搴°亯閸愬秳绶犻崗宥堜笉閻㈢喐鍨氶崥?
// the account's daily generation allowance is spent. That makes the codes a
// per-account condition after all 閳?one session passed the submit layer while
// three others were refused in the same minute 閳?so failing over is the right
// response and is left in place.
//
// The codes are still classified, because the distinction is worth reporting:
// "this session cannot generate" reads very differently from "the request was
// malformed", and an operator who sees it knows to wait for the allowance to
// reset or to add a session rather than to look for a bug.
var ErrUpstreamRateLimited = errors.New("upstream will not generate right now")

// quotaCodes are the refusals that mean "not now, for this session".
var quotaCodes = map[string]bool{
	"710022002": true, // 閻╊喖澧犻張宥呭珡娴ｈ法鏁ゆ禍鐑樻毄鏉撳啫顦?閳?high demand
	"710022004": true, // shark_admin challenge, also returned once the daily allowance is spent
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
			code := formatErrorCode(failure.ErrorCode)
			if quotaCodes[code] {
				return "", true, fmt.Errorf("%w: %s %s", ErrUpstreamRateLimited, code, failure.ErrorMsg)
			}
			return "", true, fmt.Errorf("upstream refused: %s %s", code, failure.ErrorMsg)
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
