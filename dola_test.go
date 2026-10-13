package main

import (
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

	client := NewUpstream(egress, "sessionid=abc")
	// Point the client at the fake server instead of the real host.
	client.base = upstream.URL

	result, err := client.Poll(context.Background(), "38418263229243665")
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

	client := NewUpstream(egress, "sessionid=abc")
	client.base = upstream.URL

	_, err = client.Poll(context.Background(), "conv-1")
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
	if _, err := NewUpstream(egress, "").Poll(context.Background(), ""); err == nil {
		t.Error("an empty conversation id was accepted")
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
