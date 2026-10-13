package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBuildPollBodyShape(t *testing.T) {
	body, err := buildPollBody("38418263229243665", "tab-1")
	if err != nil {
		t.Fatalf("buildPollBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if got := parsed["cmd"]; got != float64(3100) {
		t.Errorf("cmd = %v, want 3100", got)
	}
	uplink, ok := parsed["uplink_body"].(map[string]any)
	if !ok {
		t.Fatal("uplink_body missing")
	}
	pull, ok := uplink["pull_singe_chain_uplink_body"].(map[string]any)
	if !ok {
		t.Fatal("pull_singe_chain_uplink_body missing")
	}
	if got := pull["conversation_id"]; got != "38418263229243665" {
		t.Errorf("conversation_id = %v", got)
	}
	// The page sends Number.MAX_SAFE_INTEGER to mean "from the newest message".
	if got := pull["anchor_index"]; got != float64(9007199254740991) {
		t.Errorf("anchor_index = %v, want 2^53-1", got)
	}
	if _, ok := parsed["sequence_id"].(string); !ok {
		t.Error("sequence_id missing")
	}
}

// pollFixture builds a response shaped like the real one: the first message
// carries its content as a JSON string, the second as an array, and the video
// hides its unwatermarked URL in a base64 field inside video_model.
func pollFixture(t *testing.T) []byte {
	t.Helper()
	original := "https://cdn.example/original.mp4"
	encoded := base64.StdEncoding.EncodeToString([]byte(original))
	model := fmt.Sprintf(`{"model":"seedance_v2.0","video_list":{"1":{"main_url":"%s"}}}`, encoded)
	modelJSON, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("marshal model: %v", err)
	}

	asString := `[{"block_type":10000,"content":{"text_block":{"text":"a cat on a windowsill"}}}]`
	asArray := fmt.Sprintf(`[{"block_type":2074,"content":{"creation_block":{"creations":[`+
		`{"type":2,"video":{"download_url":"https://cdn.example/watermarked.mp4",`+
		`"cover_url":"https://cdn.example/cover.jpg","video_model":%s}},`+
		`{"type":1,"video":{"download_url":"https://cdn.example/ignored.mp4"}}]}}}]`, modelJSON)

	response := map[string]any{
		"downlink_body": map[string]any{
			"pull_singe_chain_downlink_body": map[string]any{
				"messages": []any{
					map[string]any{"content": asString},
					map[string]any{"content": asArray},
					map[string]any{"content": "{not json"}, // must be skipped, not fatal
				},
			},
		},
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return raw
}

func TestExtractPollFindsVideoAndText(t *testing.T) {
	result, err := extractPoll("conv-1", pollFixture(t))
	if err != nil {
		t.Fatalf("extractPoll: %v", err)
	}
	if result.ConversationID != "conv-1" {
		t.Errorf("conversation id = %q", result.ConversationID)
	}
	if result.Messages != 3 {
		t.Errorf("messages = %d, want 3 (including the unreadable one)", result.Messages)
	}
	if len(result.Texts) != 1 || result.Texts[0] != "a cat on a windowsill" {
		t.Errorf("texts = %v", result.Texts)
	}
	if len(result.Videos) != 1 {
		t.Fatalf("videos = %d, want 1 (type 1 creations must be ignored)", len(result.Videos))
	}
	v := result.Videos[0]
	if v.DownloadURL != "https://cdn.example/watermarked.mp4" {
		t.Errorf("download url = %q", v.DownloadURL)
	}
	if v.UnwatermarkedURL != "https://cdn.example/original.mp4" {
		t.Errorf("unwatermarked url = %q", v.UnwatermarkedURL)
	}
	if v.Cover != "https://cdn.example/cover.jpg" {
		t.Errorf("cover = %q", v.Cover)
	}
	if v.Model != "seedance_v2.0" {
		t.Errorf("model = %q", v.Model)
	}
}

func TestDecodeVideoModelRejectsNonURLs(t *testing.T) {
	notAURL := base64.StdEncoding.EncodeToString([]byte("not a link"))
	cases := []struct {
		name      string
		model     string
		wantModel string
		wantURL   string
	}{
		{"empty", "", "", ""},
		{"not json", "{", "", ""},
		{"no list", `{"model":"m"}`, "m", ""},
		{"decodes to a non-url", fmt.Sprintf(`{"model":"m","video_list":{"1":{"main_url":%q}}}`, notAURL), "m", ""},
	}
	for _, c := range cases {
		gotModel, gotURL := decodeVideoModel(c.model)
		if gotModel != c.wantModel || gotURL != c.wantURL {
			t.Errorf("%s: decodeVideoModel = (%q, %q), want (%q, %q)",
				c.name, gotModel, gotURL, c.wantModel, c.wantURL)
		}
	}
}

func TestExtractPollRejectsGarbage(t *testing.T) {
	if _, err := extractPoll("conv-1", []byte("not json")); err == nil {
		t.Error("a non-JSON body was accepted")
	}
}

// Poll is exercised end to end against a fake upstream, with the egress in
// direct mode so the test needs no relay.
func TestUpstreamPollEndToEnd(t *testing.T) {
	var sawPath, sawBody, sawContentType string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sawPath, sawBody, sawContentType = r.URL.RequestURI(), string(raw), r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pollFixture(t))
	}))
	defer upstream.Close()

	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()

	client := NewUpstream(egress)
	// Point the client at the fake server instead of the real host.
	client.base = upstream.URL

	result, err := client.Poll(context.Background(), "sessionid=abc", "38418263229243665")
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !strings.HasPrefix(sawPath, "/im/chain/single?") {
		t.Errorf("path = %q, want /im/chain/single", sawPath)
	}
	if !strings.Contains(sawPath, "aid=495671") {
		t.Errorf("query is missing aid: %q", sawPath)
	}
	if sawContentType != "application/json; encoding=utf-8" {
		t.Errorf("Content-Type = %q", sawContentType)
	}
	if !strings.Contains(sawBody, `"cmd":3100`) {
		t.Errorf("body = %q", sawBody)
	}
	if len(result.Videos) != 1 || result.Videos[0].UnwatermarkedURL == "" {
		t.Errorf("Poll did not surface the video: %+v", result.Videos)
	}
}

func TestUpstreamPollReportsUpstreamErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("denied\nsecond line"))
	}))
	defer upstream.Close()

	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()

	client := NewUpstream(egress)
	client.base = upstream.URL

	_, err = client.Poll(context.Background(), "sessionid=abc", "conv-1")
	if err == nil {
		t.Fatal("a 403 was reported as success")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "denied") {
		t.Errorf("error %q does not carry the status and the body", err)
	}
}

func TestUpstreamPollRejectsEmptyConversation(t *testing.T) {
	egress, err := NewEgress(Config{EgressMode: "direct"})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()
	if _, err := NewUpstream(egress).Poll(context.Background(), "", ""); err == nil {
		t.Error("an empty conversation id was accepted")
	}
}

func TestReadAckFormatsErrorCodesAsIntegers(t *testing.T) {
	// error_code arrives as a JSON number. Rendering it with %v produced
	// "7.10022002e+08" in the deployed service's logs, which is useless: the
	// number that identifies the refusal is 710022002. An on-device acceptance
	// run is what surfaced it.
	stream := "id: 0\nevent: SSE_HEARTBEAT\ndata: {}\n\n" +
		"id: 0\nevent: STREAM_ERROR\ndata: {\"error_code\":710022002,\"error_msg\":\"rate limited\"}\n\n"

	_, err := readAck(bufio.NewReader(strings.NewReader(stream)))
	if err == nil {
		t.Fatal("a STREAM_ERROR was not reported as an error")
	}
	if strings.Contains(err.Error(), "e+08") {
		t.Errorf("the error code was rendered in scientific notation: %v", err)
	}
	if !strings.Contains(err.Error(), "710022002") {
		t.Errorf("error %q does not carry the code", err)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error %q does not carry the message", err)
	}
}

func TestReadAckFindsTheConversationID(t *testing.T) {
	stream := "id: 0\nevent: SSE_ACK\ndata: {\"ack_client_meta\":{\"conversation_id\":\"38418263229243665\"}}\n\n"
	id, err := readAck(bufio.NewReader(strings.NewReader(stream)))
	if err != nil {
		t.Fatalf("readAck: %v", err)
	}
	if id != "38418263229243665" {
		t.Errorf("conversation id = %q", id)
	}
}

func TestFormatErrorCode(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{float64(710022002), "710022002"},
		{float64(710022004), "710022004"},
		{"710022002", "710022002"},
		{nil, "(none)"},
	}
	for _, c := range cases {
		if got := formatErrorCode(c.in); got != c.want {
			t.Errorf("formatErrorCode(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The submit URL must not carry a_bogus. This is the single most expensive
// lesson in this port: sending a placeholder signature looked harmless and
// actively broke every submission, while omitting the parameter is accepted.
func TestSubmitURLCarriesNoSignature(t *testing.T) {
	egress, err := NewEgress(Config{EgressMode: "direct"})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()

	u := NewUpstream(egress)
	target := u.submitURL()

	if strings.Contains(target, "a_bogus") {
		t.Errorf("the submit URL carries a_bogus, which the upstream refuses: %s", target)
	}
	for _, want := range []string{"/chat/completion?", "aid=495671", "version_code=20800", "web_tab_id="} {
		if !strings.Contains(target, want) {
			t.Errorf("the submit URL is missing %q: %s", want, target)
		}
	}
}

// The request the adapter actually sends must match, not just the URL builder.
func TestSubmitSendsNoSignatureParameter(t *testing.T) {
	var sawQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 0\nevent: SSE_ACK\ndata: {\"ack_client_meta\":{\"conversation_id\":\"42\"}}\n\n"))
	}))
	defer upstream.Close()

	egress, err := NewEgress(Config{EgressMode: "direct", FirstByteTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewEgress: %v", err)
	}
	defer egress.Close()

	client := NewUpstream(egress)
	client.base = upstream.URL

	convID, err := client.Submit(context.Background(), "sessionid=x",
		SubmitRequest{Prompt: "a cat", Ratio: "9:16", Model: "seedance_v2.0", Duration: 10})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if convID != "42" {
		t.Errorf("conversation id = %q", convID)
	}
	if strings.Contains(sawQuery, "a_bogus") {
		t.Errorf("the request carried a_bogus: %s", sawQuery)
	}
}

func TestBuildSubmitBodyShape(t *testing.T) {
	body, err := buildSubmitBody(SubmitRequest{
		Prompt: "a cat", Ratio: "9:16", Model: "seedance_v2.0", Duration: 10,
	}, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("buildSubmitBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	ability, ok := parsed["chat_ability"].(map[string]any)
	if !ok {
		t.Fatal("chat_ability missing")
	}
	if ability["ability_type"] != float64(17) {
		t.Errorf("ability_type = %v, want 17 for video", ability["ability_type"])
	}
	var param map[string]any
	if err := json.Unmarshal([]byte(ability["ability_param"].(string)), &param); err != nil {
		t.Fatalf("ability_param is not JSON: %v", err)
	}
	if param["model"] != "seedance_v2.0" || param["ratio"] != "9:16" || param["duration"] != float64(10) {
		t.Errorf("ability_param = %v", param)
	}

	messages, ok := parsed["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %v", parsed["messages"])
	}
	first := messages[0].(map[string]any)
	blocks := first["content_block"].([]any)
	block := blocks[0].(map[string]any)
	if block["block_type"] != float64(10000) {
		t.Errorf("block_type = %v, want 10000", block["block_type"])
	}
	text := block["content"].(map[string]any)["text_block"].(map[string]any)["text"]
	if text != "a cat" {
		t.Errorf("prompt = %v", text)
	}
}

func TestBuildSubmitBodyRejectsAnEmptyPrompt(t *testing.T) {
	if _, err := buildSubmitBody(SubmitRequest{}, time.Now()); err == nil {
		t.Error("an empty prompt was accepted")
	}
}

func TestNewUUIDIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		id := newUUID()
		if len(id) != 36 {
			t.Fatalf("uuid %q has length %d, want 36", id, len(id))
		}
		if id[14] != '4' {
			t.Errorf("uuid %q is not version 4", id)
		}
		if !strings.ContainsRune("89ab", rune(id[19])) {
			t.Errorf("uuid %q has a bad variant nibble %q", id, id[19])
		}
		if seen[id] {
			t.Fatalf("uuid %q was generated twice", id)
		}
		seen[id] = true
	}
}
