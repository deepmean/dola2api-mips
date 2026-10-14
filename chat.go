package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// chatRequest is the OpenAI chat body, of which the model and the last user
// message matter here.
type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role string `json:"role"`
		// Content is a string in the simple form, or an array of parts
		// ({"type":"text","text":"..."}) in the richer one. Both are accepted.
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

// chatResponse is an OpenAI-shaped completion carrying the reply text.
type chatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage map[string]int `json:"usage"`
	// ConversationID is this service's own addition; an OpenAI client ignores it
	// and an operator finds it useful when a reply looks wrong.
	ConversationID string `json:"conversation_id,omitempty"`
	// Events and SampleFrames are diagnostics for the case that matters most:
	// a tolerant extractor that still finds nothing yields an empty reply that
	// is indistinguishable from a working service answering nothing.
	Events       int      `json:"events,omitempty"`
	SampleFrames []string `json:"sample_frames,omitempty"`
}

// registerChat adds the chat-completions endpoint.
func (a *api) registerChat(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", a.withAuth(a.handleChatCompletions))
}

// handleChatCompletions runs one text turn.
//
// This does not go through the Pool. Chat needs no task record, no polling and
// no media store, and with one worker and a ten-minute video timeout a chat
// queued behind a video would look like a hang rather than a queue. It gets its
// own concurrency bound and its own deadline instead.
func (a *api) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxAPIRequestBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read the request body", "bad_body")
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "bad_json")
		return
	}

	prompt, err := lastUserText(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if len(prompt) > 4000 {
		writeError(w, http.StatusBadRequest, "prompt is too long (4000 characters maximum)", "invalid_request")
		return
	}

	if req.Stream {
		a.handleChatStream(w, r, prompt, req.Model)
		return
	}
	if a.upstream == nil || a.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "chat is not configured", "no_upstream")
		return
	}

	// Its own bound, so a slow turn cannot occupy the whole service.
	select {
	case a.chatSlots <- struct{}{}:
		defer func() { <-a.chatSlots }()
	default:
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests,
			"too many chat requests in flight", "chat_busy")
		return
	}

	account, err := a.accounts.Pick(time.Now())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no session available: "+err.Error(), "no_session")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.ChatTimeout)
	defer cancel()

	result, err := a.upstream.Chat(ctx, account.Cookie, ChatRequest{Prompt: prompt}, a.cfg.DeepThinkValue)
	if err != nil {
		// A refusal or a transport failure is the account's problem as much as
		// the request's, so it is parked like a failed submit would be.
		a.accounts.RecordFailure(account, time.Now(), err)
		writeError(w, http.StatusBadGateway, err.Error(), "chat_failed")
		return
	}
	a.accounts.RecordSuccess(account, time.Now())

	content := result.Reply
	if content == "" {
		// Do not report an empty answer as success: it is the signature of an
		// extractor that no longer matches the stream.
		content = fmt.Sprintf("The upstream returned no text (%d frame(s)). Raw frames are in sample_frames.",
			result.Events)
	}

	resp := chatResponse{
		ID:             "chatcmpl-" + newUUID(),
		Object:         "chat.completion",
		Created:        time.Now().Unix(),
		Model:          req.Model,
		ConversationID: result.ConversationID,
		Events:         result.Events,
		SampleFrames:   result.SampleFrames,
		Usage: map[string]int{
			"prompt_tokens":     len(prompt) / 4,
			"completion_tokens": len(content) / 4,
			"total_tokens":      (len(prompt) + len(content)) / 4,
		},
	}
	resp.Choices = make([]struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}, 1)
	resp.Choices[0].Index = 0
	resp.Choices[0].Message.Role = "assistant"
	resp.Choices[0].Message.Content = content
	resp.Choices[0].FinishReason = "stop"

	writeJSON(w, http.StatusOK, resp)
}

// handleChatStream runs one text turn and forwards the reply as it arrives.
//
// The upstream already speaks SSE, so this is a translation rather than a
// simulation: its deltas become OpenAI chunks. A client that asked for
// stream=true and got a 501 is a client that cannot use the service at all.
func (a *api) handleChatStream(w http.ResponseWriter, r *http.Request, prompt, model string) {
	if a.upstream == nil || a.accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "chat is not configured", "no_upstream")
		return
	}
	select {
	case a.chatSlots <- struct{}{}:
		defer func() { <-a.chatSlots }()
	default:
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "too many chat requests in flight", "chat_busy")
		return
	}

	account, err := a.accounts.Pick(time.Now())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no session available: "+err.Error(), "no_session")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.ChatTimeout)
	defer cancel()

	completionID := "chatcmpl-" + newUUID()
	created := time.Now().Unix()

	// Nothing is written until the first delta or the first refusal, so a
	// refusal can still be reported with its own status code instead of as a
	// 200 with an error inside it.
	started := false
	controller := http.NewResponseController(w)

	emit := func(chunk string) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			// Tells a reverse proxy not to buffer, which would defeat the point.
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		payload, err := json.Marshal(chatChunk{
			ID: completionID, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []chatChunkChoice{{Index: 0, Delta: chatDelta{Content: chunk}}},
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		return controller.Flush()
	}

	result, err := a.upstream.ChatStream(ctx, account.Cookie, ChatRequest{Prompt: prompt},
		a.cfg.DeepThinkValue, emit)
	if err != nil {
		a.accounts.RecordFailure(account, time.Now(), err)
		if !started {
			writeError(w, http.StatusBadGateway, err.Error(), "chat_failed")
			return
		}
		// Headers are already out; the only honest thing left is to end the
		// stream with the error visible in it.
		_, _ = fmt.Fprintf(w, "data: {\"error\":%q}\n\ndata: [DONE]\n\n", err.Error())
		_ = controller.Flush()
		return
	}
	a.accounts.RecordSuccess(account, time.Now())

	if !started {
		// The upstream answered without any text. Saying so beats an empty
		// stream a client reads as a finished, empty reply.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		started = true
	}
	final, err := json.Marshal(chatChunk{
		ID: completionID, Object: "chat.completion.chunk", Created: created, Model: model,
		ConversationID: result.ConversationID,
		Choices:        []chatChunkChoice{{Index: 0, Delta: chatDelta{}, FinishReason: "stop"}},
	})
	if err == nil {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", final)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	_ = controller.Flush()
}

// chatChunk is one OpenAI streaming chunk.
type chatChunk struct {
	ID             string            `json:"id"`
	Object         string            `json:"object"`
	Created        int64             `json:"created"`
	Model          string            `json:"model"`
	Choices        []chatChunkChoice `json:"choices"`
	ConversationID string            `json:"conversation_id,omitempty"`
}

type chatChunkChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason string    `json:"finish_reason,omitempty"`
}

type chatDelta struct {
	Content string `json:"content,omitempty"`
}

// lastUserText extracts the prompt from the last user message.
func lastUserText(req chatRequest) (string, error) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != "user" {
			continue
		}
		if text := contentText(msg.Content); text != "" {
			return text, nil
		}
	}
	return "", errors.New("no user message with text was found")
}

// contentText reads either content shape.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var out []string
		for _, part := range parts {
			if part.Text != "" {
				out = append(out, part.Text)
			}
		}
		return strings.TrimSpace(strings.Join(out, "\n"))
	}
	return ""
}
