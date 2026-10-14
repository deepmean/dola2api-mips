package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatRequest is one text turn.
type ChatRequest struct {
	Prompt string
	// DeepThink asks for the reasoning pass. The wire value is uncertain 鈥?the
	// upstream Python uses 1, a third-party client uses 3 鈥?so it is a setting
	// rather than a constant.
	DeepThink bool
}

// ChatResult is what one turn yielded.
type ChatResult struct {
	ConversationID string
	Reply          string
	// Events counts the frames seen, which is what distinguishes "the extractor
	// found nothing" from "the upstream sent nothing".
	Events int
	// SampleFrames keeps the first few raw frames. A tolerant extractor that is
	// still wrong produces an empty reply that looks like success, so the raw
	// material has to be reachable without a packet capture.
	SampleFrames []string
	// FinishReason is the upstream's end type, when it said.
	FinishReason string
}

// sampleFrames is how many raw frames are kept for diagnosis.
const sampleFrames = 6

// maxChatStream caps how much of a reply stream is read.
const maxChatStream = 4 << 20

// Chat runs one text turn and returns the assistant's reply.
//
// It is deliberately not a Pool task: chat needs no task record, no polling and
// no media store, and with one worker and a ten-minute video timeout a chat
// queued behind a video would look like a hang.
func (u *Upstream) Chat(ctx context.Context, cookie string, req ChatRequest, deepThinkValue int) (ChatResult, error) {
	return u.chat(ctx, cookie, req, deepThinkValue, nil)
}

// ChatStream is Chat with each piece of text handed to onDelta as it arrives.
//
// A caller that wants to forward the reply incrementally gets it while it is
// being produced rather than after. onDelta returning an error stops the read,
// which is how a client that has gone away ends the request.
func (u *Upstream) ChatStream(ctx context.Context, cookie string, req ChatRequest,
	deepThinkValue int, onDelta func(string) error) (ChatResult, error) {
	if onDelta == nil {
		return ChatResult{}, errors.New("chat: ChatStream needs an onDelta callback")
	}
	return u.chat(ctx, cookie, req, deepThinkValue, onDelta)
}

func (u *Upstream) chat(ctx context.Context, cookie string, req ChatRequest,
	deepThinkValue int, onDelta func(string) error) (ChatResult, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return ChatResult{}, errors.New("chat: empty prompt")
	}
	body, err := buildChatBody(req, u.now(), deepThinkValue)
	if err != nil {
		return ChatResult{}, err
	}

	hdr := http.Header{}
	hdr.Set("Content-Type", "application/json")
	hdr.Set("agw-js-conv", "str, str")
	hdr.Set("Accept", "text/event-stream")
	hdr.Set("Cookie", cookie)

	resp, err := u.egress.Do(ctx, http.MethodPost, u.submitURL(cookie), hdr, body)
	if err != nil {
		return ChatResult{}, fmt.Errorf("chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ChatResult{}, fmt.Errorf("chat: upstream status %d: %s", resp.StatusCode, firstLine(raw, 160))
	}

	return readChatStream(bufio.NewReaderSize(io.LimitReader(resp.Body, maxChatStream), 8<<10), onDelta)
}

// A chat turn posts to the same URL the video submit does, so it uses the same
// builder rather than a second copy that can drift from it.

// buildChatBody renders a text turn.
//
// chat_ability is omitted rather than sent as the video skill: the only client
// known to work against this endpoint omits it, and the upstream's own default
// is skill 0. Sending 17 is what a reused video body would do, and it would ask
// for a video instead of an answer.
func buildChatBody(req ChatRequest, now time.Time, deepThinkValue int) ([]byte, error) {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, errors.New("chat: empty prompt")
	}
	nowMs := now.UnixMilli()
	think := 0
	if req.DeepThink {
		think = deepThinkValue
	}

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
						"text": prompt, "icon_url": "", "icon_url_dark": "", "summary": "",
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
			"answer_with_suggest": false, "tts_switch": false, "need_deep_think": think,
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
		"user_context": []any{},
		"ext": map[string]any{
			"fp": "", "use_deep_think": "0", "sub_conv_firstmet_type": "1", "collection_id": "",
			"conversation_init_option":      `{"need_ack_conversation":true}`,
			"commerce_credit_config_enable": "0",
		},
	}
	return json.Marshal(payload)
}

// readChatStream consumes the reply stream to the end.
func readChatStream(r *bufio.Reader, onDelta func(string) error) (ChatResult, error) {
	result := ChatResult{}
	var (
		event string
		data  strings.Builder
		// text accumulates everything the stream emits. A finished reasoning
		// block marks the boundary between reasoning and answer, and clears it 鈥?		// which is what the one client known to work against this endpoint does.
		// Keeping the two apart instead would return nothing at all for a stream
		// that reasons and never announces the boundary.
		text strings.Builder
	)

	for {
		line, err := r.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				if event != "" {
					result.Events++
					if len(result.SampleFrames) < sampleFrames {
						result.SampleFrames = append(result.SampleFrames,
							truncateFrame(event, data.String()))
					}
					done, ferr := applyChatFrame(event, data.String(), &result, &text, onDelta)
					if ferr != nil {
						return result, ferr
					}
					if done {
						result.Reply = strings.TrimSpace(text.String())
						return result, nil
					}
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
				// The stream ended without an explicit end frame; whatever was
				// collected is the answer.
				result.Reply = strings.TrimSpace(text.String())
				return result, nil
			}
			return result, fmt.Errorf("chat: read: %w", err)
		}
	}
}

func truncateFrame(event, data string) string {
	frame := event + " " + data
	if len(frame) > 300 {
		frame = frame[:300] + "..."
	}
	return frame
}

// applyChatFrame handles one complete frame, and reports whether the reply is over.
func applyChatFrame(event, data string, result *ChatResult, text *strings.Builder, onDelta func(string) error) (bool, error) {
	switch event {
	case "SSE_ACK":
		result.ConversationID = digString(data, "ack_client_meta", "conversation_id")
	case "STREAM_ERROR":
		return true, chatRefusal(data)
	case "SSE_REPLY_END":
		result.FinishReason = fmt.Sprint(digAny(data, "end_type"))
		return true, nil
	}

	// Everything else may carry text. The extractor is tolerant on purpose:
	// a strict one yields an empty reply, which is indistinguishable from a
	// working feature that answers nothing.
	for _, block := range chatBlocks(data) {
		thinking := false
		if kind, ok := block["block_type"].(float64); ok && int(kind) == blockTypeThinking {
			thinking = true
			if finished, _ := block["is_finish"].(bool); finished {
				// The reasoning pass is over, so the answer starts here.
				text.Reset()
			}
		}
		chunk, _ := blockText(block)
		if chunk == "" {
			continue
		}
		text.WriteString(chunk)
		// A stream cannot take back what it has already sent, so reasoning is
		// not forwarded: the buffer can be cleared, a client's screen cannot.
		if onDelta != nil && !thinking {
			if err := onDelta(chunk); err != nil {
				return true, err
			}
		}
	}
	return false, nil
}

// chatRefusal reads a refusal in either of the shapes the endpoint uses.
func chatRefusal(data string) error {
	code := digAny(data, "error_code")
	msg := digString(data, "error_msg")
	if code == nil {
		code = digAny(data, "data", "error_code")
		msg = digString(data, "data", "error_msg")
	}
	if code == nil {
		msg = digString(data, "message")
	}
	if msg == "" {
		msg = digString(data, "data", "message")
	}
	if code == nil && msg == "" {
		return errors.New("chat: upstream refused with an unreadable error")
	}
	return fmt.Errorf("chat: upstream refused: %s %s", formatErrorCode(code), msg)
}

// chatBlocks collects content blocks from every place the endpoint puts them.
//
// Observed shapes, from two independent implementations and the frames this port
// already saw: content.content_block, data.content_block, and patch_op entries
// whose patch_value carries content_block. All are accepted; unknown ones are
// ignored rather than treated as an error.
func chatBlocks(data string) []map[string]any {
	var root map[string]any
	if err := json.Unmarshal([]byte(data), &root); err != nil {
		return nil
	}

	var out []map[string]any
	addFrom := func(m map[string]any) {
		if m == nil {
			return
		}
		if content, ok := m["content"].(map[string]any); ok {
			out = append(out, asBlocks(content["content_block"])...)
		}
		out = append(out, asBlocks(m["content_block"])...)
		for _, op := range asBlocks(m["patch_op"]) {
			if patch, ok := op["patch_value"].(map[string]any); ok {
				out = append(out, asBlocks(patch["content_block"])...)
			}
		}
	}
	addFrom(root)
	if payload, ok := root["data"].(map[string]any); ok {
		addFrom(payload)
	}
	return out
}

func asBlocks(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		if block, ok := entry.(map[string]any); ok {
			out = append(out, block)
		}
	}
	return out
}

// blockText reads the text of one block and says whether it came from a
// reasoning block rather than the answer.
func blockText(block map[string]any) (text string, thinking bool) {
	content, ok := block["content"].(map[string]any)
	if !ok {
		return "", false
	}
	if textBlock, ok := content["text_block"].(map[string]any); ok {
		if s, ok := textBlock["text"].(string); ok && s != "" {
			return s, false
		}
	}
	if thinkBlock, ok := content["thinking_block"].(map[string]any); ok {
		if s, ok := thinkBlock["text"].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// digString walks nested maps for a string field, returning "" when absent.
func digString(data string, path ...string) string {
	value := digAny(data, path...)
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

// digAny walks nested maps for a field.
func digAny(data string, path ...string) any {
	var current any
	if err := json.Unmarshal([]byte(data), &current); err != nil {
		return nil
	}
	for _, key := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[key]
	}
	return current
}
