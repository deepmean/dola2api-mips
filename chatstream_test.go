package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// frame renders one SSE frame the way the endpoint does.
func frame(id int, event, data string) string {
	return "id: " + string(rune('0'+id%10)) + "\nevent: " + event + "\ndata: " + data + "\n\n"
}

func readStream(t *testing.T, stream string) ChatResult {
	t.Helper()
	result, err := readChatStream(bufio.NewReader(strings.NewReader(stream)), nil)
	if err != nil {
		t.Fatalf("readChatStream: %v", err)
	}
	return result
}

// The happy path, with the text arriving as deltas across several frames.
func TestChatStreamAssemblesDeltas(t *testing.T) {
	stream := frame(0, "SSE_HEARTBEAT", "{}") +
		frame(1, "SSE_ACK", `{"ack_client_meta":{"conversation_id":"38418091602493713"}}`) +
		frame(2, "STREAM_MSG_NOTIFY", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"Hello"}}}]}}`) +
		frame(3, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":", world"}}}]}}`) +
		frame(4, "SSE_REPLY_END", `{"end_type":3}`)

	result := readStream(t, stream)
	if result.ConversationID != "38418091602493713" {
		t.Errorf("conversation id = %q", result.ConversationID)
	}
	if result.Reply != "Hello, world" {
		t.Errorf("reply = %q, want the deltas joined", result.Reply)
	}
	if result.FinishReason != "3" {
		t.Errorf("finish reason = %q", result.FinishReason)
	}
	if result.Events < 4 {
		t.Errorf("events = %d, want every frame counted", result.Events)
	}
	if len(result.SampleFrames) == 0 {
		t.Error("no raw frames were kept for diagnosis")
	}
}

// The payload shape is the one part of chat that was inferred rather than
// measured, so every shape seen in the wild has to work.
func TestChatStreamAcceptsEveryPayloadShape(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{
			"content.content_block",
			`{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"a"}}}]}}`,
			"a",
		},
		{
			"content_block at the root",
			`{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"b"}}}]}`,
			"b",
		},
		{
			"everything wrapped in data",
			`{"data":{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"c"}}}]}}}`,
			"c",
		},
		{
			"a patch operation",
			`{"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"d"}}}]}}]}`,
			"d",
		},
		{
			"a patch operation wrapped in data",
			`{"data":{"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"e"}}}]}}]}}`,
			"e",
		},
		{
			"a thinking block counts as text too",
			`{"content":{"content_block":[{"block_type":10040,"content":{"thinking_block":{"text":"f"}}}]}}`,
			"f",
		},
	}
	for _, c := range cases {
		result := readStream(t, frame(0, "STREAM_CHUNK", c.data)+frame(1, "SSE_REPLY_END", `{"end_type":3}`))
		if result.Reply != c.want {
			t.Errorf("%s: reply = %q, want %q", c.name, result.Reply, c.want)
		}
	}
}

// A finished reasoning block means the answer starts after it, so whatever the
// reasoning pass emitted is not part of the reply.
func TestChatStreamDropsReasoningBeforeTheAnswer(t *testing.T) {
	stream := frame(0, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10040,"content":{"thinking_block":{"text":"let me think"}}}]}}`) +
		frame(1, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10040,"is_finish":true,"content":{"thinking_block":{"text":"."}}}]}}`) +
		frame(2, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"the answer"}}}]}}`) +
		frame(3, "SSE_REPLY_END", `{"end_type":3}`)

	result := readStream(t, stream)
	if strings.Contains(result.Reply, "think") {
		t.Errorf("reply contains the reasoning pass: %q", result.Reply)
	}
	if !strings.Contains(result.Reply, "the answer") {
		t.Errorf("reply = %q, want the answer", result.Reply)
	}
}

func TestChatStreamReportsRefusals(t *testing.T) {
	stream := frame(0, "SSE_HEARTBEAT", "{}") +
		frame(1, "STREAM_ERROR", `{"error_code":710022002,"error_msg":"rate limited"}`)

	_, err := readChatStream(bufio.NewReader(strings.NewReader(stream)), nil)
	if err == nil {
		t.Fatal("a STREAM_ERROR was reported as a successful reply")
	}
	if strings.Contains(err.Error(), "e+08") {
		t.Errorf("the code was rendered in scientific notation: %v", err)
	}
	if !strings.Contains(err.Error(), "710022002") {
		t.Errorf("error %q does not carry the code", err)
	}
}

func TestChatStreamHandlesTheWrappedRefusal(t *testing.T) {
	stream := frame(0, "STREAM_ERROR", `{"data":{"error_code":710022004,"error_msg":"slide"}}`)
	_, err := readChatStream(bufio.NewReader(strings.NewReader(stream)), nil)
	if err == nil {
		t.Fatal("a wrapped refusal was reported as success")
	}
	if !strings.Contains(err.Error(), "710022004") {
		t.Errorf("error = %v", err)
	}
}

// An empty stream is not an error here; the caller decides what to say about it.
func TestChatStreamWithNoText(t *testing.T) {
	result := readStream(t, frame(0, "SSE_HEARTBEAT", "{}")+frame(1, "SSE_REPLY_END", `{"end_type":3}`))
	if result.Reply != "" {
		t.Errorf("reply = %q, want empty", result.Reply)
	}
	if result.Events != 2 {
		t.Errorf("events = %d, want 2", result.Events)
	}
}

// A stream that just stops still yields whatever arrived.
func TestChatStreamToleratesATruncatedStream(t *testing.T) {
	result := readStream(t, frame(0, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"half"}}}]}}`))
	if result.Reply != "half" {
		t.Errorf("reply = %q, want what arrived before the stream stopped", result.Reply)
	}
}

// A client that asks for stream=true must get a stream. Returning 501 is what
// made the service unusable for such a client.
func TestChatStreamEndpointEmitsOpenAIChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			frame(0, "SSE_ACK", `{"ack_client_meta":{"conversation_id":"77"}}`) +
				frame(1, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"Hel"}}}]}}`) +
				frame(2, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"lo"}}}]}}`) +
				frame(3, "SSE_REPLY_END", `{"end_type":3}`)))
	}))
	defer srv.Close()

	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, accounts := newTestAPI(t, exec, Config{ChatTimeout: 20 * time.Second})
	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	up.base = srv.URL
	api.upstream = up
	if len(accounts.Cookies()) == 0 {
		t.Fatal("the test harness has no session")
	}

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/chat/completions",
		`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "sk-test")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want an event stream", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{`"object":"chat.completion.chunk"`, `"content":"Hel"`, `"content":"lo"`,
		`"finish_reason":"stop"`, "[DONE]"} {
		if !strings.Contains(body, want) {
			t.Errorf("the stream is missing %s:\n%s", want, body)
		}
	}
	// The deltas must arrive separately, not as one blob at the end.
	if strings.Count(body, "data: ") < 4 {
		t.Errorf("the reply was not split into chunks:\n%s", body)
	}
}

// A refusal must still be a refusal: nothing is written until the first delta,
// so the error can carry its own status instead of hiding inside a 200.
func TestChatStreamRefusalKeepsItsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(frame(0, "STREAM_ERROR", `{"error_code":710022004,"error_msg":"refused"}`)))
	}))
	defer srv.Close()

	exec := &fakeExecutor{conversationID: "conv-1"}
	api, _, _ := newTestAPI(t, exec, Config{ChatTimeout: 20 * time.Second})
	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	up.base = srv.URL
	api.upstream = up

	rec := doRequest(t, apiRouter(api), http.MethodPost, "/v1/chat/completions",
		`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "sk-test")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502: a refusal must not be presented as a finished stream", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "710022004") {
		t.Errorf("the refusal is missing its code: %s", rec.Body.String())
	}
}

// ChatStream hands each piece of text over as it arrives, and stops when the
// consumer says so.
func TestChatStreamForwardsDeltas(t *testing.T) {
	var sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			frame(0, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"a"}}}]}}`) +
				frame(1, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"b"}}}]}}`) +
				frame(2, "SSE_REPLY_END", `{"end_type":3}`)))
	}))
	defer srv.Close()

	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	up.base = srv.URL

	var got []string
	result, err := up.ChatStream(context.Background(), "sessionid=x",
		ChatRequest{Prompt: "hi"}, 1, func(chunk string) error {
			got = append(got, chunk)
			return nil
		})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if strings.Join(got, "") != "ab" {
		t.Errorf("deltas = %v, want a then b", got)
	}
	if result.Reply != "ab" {
		t.Errorf("reply = %q", result.Reply)
	}
	if sawPath != "/chat/completion" {
		t.Errorf("path = %q", sawPath)
	}
}

// Reasoning is not forwarded: a stream cannot take back what it has sent, and
// the buffer can be cleared but a client's screen cannot.
func TestChatStreamDoesNotForwardReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			frame(0, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10040,"content":{"thinking_block":{"text":"thinking..."}}}]}}`) +
				frame(1, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10040,"is_finish":true,"content":{"thinking_block":{"text":"."}}}]}}`) +
				frame(2, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"the answer"}}}]}}`) +
				frame(3, "SSE_REPLY_END", `{"end_type":3}`)))
	}))
	defer srv.Close()

	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	up.base = srv.URL

	var got []string
	if _, err := up.ChatStream(context.Background(), "sessionid=x", ChatRequest{Prompt: "hi"}, 1,
		func(chunk string) error { got = append(got, chunk); return nil }); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	joined := strings.Join(got, "")
	if strings.Contains(joined, "thinking") {
		t.Errorf("reasoning was forwarded: %q", joined)
	}
	if joined != "the answer" {
		t.Errorf("deltas = %q, want only the answer", joined)
	}
}

// A consumer that stops reading ends the upstream request.
func TestChatStreamStopsWhenTheConsumerStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 20; i++ {
			_, _ = w.Write([]byte(frame(i, "STREAM_CHUNK",
				`{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"x"}}}]}}`)))
		}
	}))
	defer srv.Close()

	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	up.base = srv.URL

	stop := errors.New("client went away")
	count := 0
	_, err = up.ChatStream(context.Background(), "sessionid=x", ChatRequest{Prompt: "hi"}, 1,
		func(string) error {
			count++
			if count == 3 {
				return stop
			}
			return nil
		})
	if !errors.Is(err, stop) {
		t.Errorf("err = %v, want the consumer's error", err)
	}
	if count != 3 {
		t.Errorf("the consumer was called %d times, want it to stop at 3", count)
	}
}

func TestChatStreamRequiresAConsumer(t *testing.T) {
	egress, err := NewEgress(Config{EgressMode: "direct"})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	if _, err := up.ChatStream(context.Background(), "sessionid=x", ChatRequest{Prompt: "hi"}, 1, nil); err == nil {
		t.Error("a stream with no consumer was accepted")
	}
}

func TestChatStreamIgnoresUnreadableFrames(t *testing.T) {
	result := readStream(t, frame(0, "STREAM_CHUNK", "not json")+
		frame(1, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"ok"}}}]}}`))
	if result.Reply != "ok" {
		t.Errorf("reply = %q, want the readable frame", result.Reply)
	}
}

// chat_ability must be absent: the only client known to work against this
// endpoint omits it, and a reused video body would send the video skill.
func TestBuildChatBodyOmitsChatAbility(t *testing.T) {
	body, err := buildChatBody(ChatRequest{Prompt: "hi"}, time.Unix(1700000000, 0), 1)
	if err != nil {
		t.Fatalf("buildChatBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if _, present := parsed["chat_ability"]; present {
		t.Errorf("the chat body carries chat_ability: %s", body)
	}
	if _, present := parsed["messages"]; !present {
		t.Error("the chat body has no messages")
	}
}

func TestBuildChatBodyDeepThinkValue(t *testing.T) {
	body, err := buildChatBody(ChatRequest{Prompt: "hi", DeepThink: true}, time.Now(), 3)
	if err != nil {
		t.Fatalf("buildChatBody: %v", err)
	}
	var parsed struct {
		Option struct {
			NeedDeepThink int `json:"need_deep_think"`
		} `json:"option"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.Option.NeedDeepThink != 3 {
		t.Errorf("need_deep_think = %d, want the configured value", parsed.Option.NeedDeepThink)
	}

	// Off by default, whatever the configured value is.
	body, _ = buildChatBody(ChatRequest{Prompt: "hi"}, time.Now(), 3)
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.Option.NeedDeepThink != 0 {
		t.Errorf("need_deep_think = %d, want 0 when not requested", parsed.Option.NeedDeepThink)
	}
}

func TestBuildChatBodyRejectsAnEmptyPrompt(t *testing.T) {
	if _, err := buildChatBody(ChatRequest{Prompt: "   "}, time.Now(), 1); err == nil {
		t.Error("an empty prompt was accepted")
	}
}

// The whole turn, against a fake upstream.
func TestUpstreamChatEndToEnd(t *testing.T) {
	var sawPath, sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		sawBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			frame(0, "SSE_ACK", `{"ack_client_meta":{"conversation_id":"99"}}`) +
				frame(1, "STREAM_CHUNK", `{"content":{"content_block":[{"block_type":10000,"content":{"text_block":{"text":"hi there"}}}]}}`) +
				frame(2, "SSE_REPLY_END", `{"end_type":3}`)))
	}))
	defer srv.Close()

	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()

	up := NewUpstream(egress)
	up.base = srv.URL

	result, err := up.Chat(context.Background(), "sessionid=x", ChatRequest{Prompt: "hi"}, 1)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if result.Reply != "hi there" || result.ConversationID != "99" {
		t.Errorf("result = %+v", result)
	}
	if sawPath != "/chat/completion" {
		t.Errorf("path = %q", sawPath)
	}
	if strings.Contains(sawBody, "a_bogus") {
		t.Error("the request carried a_bogus")
	}
	if strings.Contains(sawBody, `"ability_type":17`) {
		t.Error("the chat request asked for the video skill")
	}
}

func TestUpstreamChatRejectsAnEmptyPrompt(t *testing.T) {
	egress, err := NewEgress(Config{EgressMode: "direct"})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	up := NewUpstream(egress)
	if _, err := up.Chat(context.Background(), "sessionid=x", ChatRequest{}, 1); err == nil {
		t.Error("an empty prompt was sent")
	}
}
