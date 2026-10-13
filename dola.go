package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	cookie string
	// tabID is generated once per process, as the page generates one per tab.
	tabID string
	// base is the API origin. Tests point it at a fake server.
	base string
	// now is injectable so tests can pin timestamps.
	now func() time.Time
}

// defaultBase is the upstream API origin.
const defaultBase = "https://www.dola.com"

// NewUpstream builds the client. cookie is a single Cookie header value.
func NewUpstream(egress *Egress, cookie string) *Upstream {
	return &Upstream{
		egress: egress,
		cookie: cookie,
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

// Poll reads a conversation once.
func (u *Upstream) Poll(ctx context.Context, conversationID string) (PollResult, error) {
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
	hdr.Set("Cookie", u.cookie)

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

func firstLine(raw []byte, limit int) string {
	text := string(raw)
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = text[:i]
	}
	if len(text) > limit {
		text = text[:limit]
	}
	return text
}
