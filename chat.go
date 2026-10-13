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

// chatRequest is the OpenAI chat body, of which only the model and the last user
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

// chatResponse is an OpenAI-shaped completion with the video task attached.
//
// The shape matters more than it looks: an existing client will not be changed
// to speak a bespoke protocol, so the adapter speaks its protocol and carries
// this service's own fields alongside.
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
	Usage     map[string]int `json:"usage"`
	VideoTask *apiTask       `json:"video_task,omitempty"`
}

// chatWaitBudget bounds how long a chat request waits for the video.
//
// A chat client expects an answer promptly and has its own timeout, usually a
// minute or two. Waiting longer would trade a useful "still working" answer for
// a client-side timeout, so the request returns the task either way and the
// client can poll it.
const chatWaitBudget = 60 * time.Second

// registerChat adds the chat-completions adapter.
func (a *api) registerChat(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", a.withAuth(a.handleChatCompletions))
}

// handleChatCompletions maps a conversation onto one video request.
//
// This service generates video, not text, so the mapping is deliberately plain:
// the last user message becomes the prompt, and the answer says what happened.
// Returning the task rather than blocking forever is the honest shape for an
// asynchronous backend — a chat client that shows "still working, poll here" is
// more useful than one that times out with nothing.
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
	if req.Stream {
		// Streaming is not implemented. Saying so beats sending a body that a
		// client will try to parse as SSE and give up on.
		writeError(w, http.StatusNotImplemented,
			"streaming is not supported; send stream=false", "no_streaming")
		return
	}

	prompt, err := lastUserText(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}

	submit, err := a.validate(apiRequest{Prompt: prompt, Model: req.Model})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}

	task, err := a.pool.Submit(submit)
	switch {
	case errors.Is(err, ErrPoolFull):
		writeError(w, http.StatusTooManyRequests, err.Error(), "pool_full")
		return
	case errors.Is(err, ErrPoolClosed):
		writeError(w, http.StatusServiceUnavailable, err.Error(), "shutting_down")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error(), "submit_failed")
		return
	}

	// Wait briefly so a client that just wants the answer often gets it.
	settled := a.awaitTask(r.Context(), task.ID, chatWaitBudget)

	content := describeTask(settled, submit)
	resp := chatResponse{
		ID:      "chatcmpl-" + settled.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   submit.Model,
		Usage: map[string]int{"prompt_tokens": len(submit.Prompt) / 4, "completion_tokens": len(content) / 4,
			"total_tokens": (len(submit.Prompt) + len(content)) / 4},
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
	snapshot := a.toAPITask(settled)
	resp.VideoTask = &snapshot

	writeJSON(w, http.StatusOK, resp)
}

// awaitTask waits for a task to settle, or returns it as it stands.
func (a *api) awaitTask(ctx context.Context, id string, budget time.Duration) Task {
	deadline := time.Now().Add(budget)
	for {
		task, ok := a.pool.Get(id)
		if !ok {
			return Task{ID: id, State: TaskFailed, Error: "the task disappeared"}
		}
		if task.State == TaskSucceeded || task.State == TaskFailed {
			return task
		}
		if time.Now().After(deadline) {
			return task
		}
		select {
		case <-ctx.Done():
			return task
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// describeTask is what the client shows its user.
func describeTask(task Task, req SubmitRequest) string {
	switch task.State {
	case TaskSucceeded:
		if len(task.Videos) > 0 {
			url := bestURL(task.Videos[0])
			return fmt.Sprintf("Video ready (%s, %ds).\n\n%s\n\nPoll: GET /v1/videos/%s",
				req.Model, req.Duration, url, task.ID)
		}
		return fmt.Sprintf("The task finished but produced no video. Poll: GET /v1/videos/%s", task.ID)
	case TaskFailed:
		return fmt.Sprintf("Video generation failed: %s\n\nTask: GET /v1/videos/%s", task.Error, task.ID)
	default:
		return fmt.Sprintf("Video generation is running (%s, %ds).\n\nPoll: GET /v1/videos/%s\nDownload: GET /v1/videos/%s/content",
			req.Model, req.Duration, task.ID, task.ID)
	}
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
