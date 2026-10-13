// Command utls_probe sends the port's request with a Chrome TLS fingerprint.
//
// Six controlled experiments established that the protocol is right and the
// client environment is what differs: the browser's own JavaScript is accepted
// while the same parameter set from Go or Python is refused with 710022004. The
// one part of that environment a stdlib binary cannot reproduce is the TLS and
// HTTP/2 fingerprint, so this probe borrows it from uTLS.
//
// It lives in its own module on purpose: the service itself must stay
// dependency-free, and this is the experiment that decides whether that
// constraint is what stands between the port and a working write.
//
// Usage:
//
//	utls_probe -cookie cookies.txt [-hello chrome] [-http2=true] [-prompt TEXT]
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

const relayHost = "vcrelay.dmean.dpdns.org"

// device is the identity captured from the browser's localStorage.
var device = map[string]string{
	"device_id": "7686671608228726325",
	"web_id":    "7695956629460485685",
	"tea_uuid":  "7695956629460485685",
	"fp":        "verify_mv6q3fus_Oi5TeE8s_72in_4CGa_9k8g_RYDKyKyHA2UK",
	"tz_name":   "Asia/Shanghai",
}

var hellos = map[string]utls.ClientHelloID{
	"chrome":      utls.HelloChrome_Auto,
	"chrome-120":  utls.HelloChrome_120,
	"chrome-131":  utls.HelloChrome_131,
	"firefox":     utls.HelloFirefox_Auto,
	"safari":      utls.HelloSafari_Auto,
	"edge":        utls.HelloEdge_Auto,
	"randomized":  utls.HelloRandomized,
	"golang":      utls.HelloGolang,
}

func main() {
	cookieFile := flag.String("cookie", "cookies.txt", "file holding the cookie header")
	helloName := flag.String("hello", "chrome", "TLS fingerprint: "+strings.Join(helloKeys(), ", "))
	useHTTP2 := flag.Bool("http2", true, "use HTTP/2 (the browser does)")
	prompt := flag.String("prompt", "用一句话介绍你自己", "message to send")
	direct := flag.Bool("direct", false, "connect to the upstream directly instead of through the relay")
	timeout := flag.Duration("timeout", 90*time.Second, "request timeout")
	flag.Parse()

	hello, ok := hellos[*helloName]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown fingerprint %q; have %s\n", *helloName, strings.Join(helloKeys(), ", "))
		os.Exit(2)
	}

	cookie, err := readCookie(*cookieFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cookie: %v\n", err)
		os.Exit(1)
	}

	body, err := chatBody(*prompt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "body: %v\n", err)
		os.Exit(1)
	}

	host := relayHost
	if *direct {
		host = "www.dola.com"
	}
	client, err := newClient(host, hello, *useHTTP2, *timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "client: %v\n", err)
		os.Exit(1)
	}

	target := "https://" + host + "/"
	if *direct {
		target = "https://www.dola.com/chat/completion?" + clientQuery()
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("agw-js-conv", "str")
	req.Header.Set("Agw-Js-Conv", "str")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Origin", "https://www.dola.com")
	req.Header.Set("Referer", "https://www.dola.com/chat")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
		"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("sec-ch-ua", `"Chromium";v="131", "Not_A Brand";v="24", "Google Chrome";v="131"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	// The browser's own request carried this, in W3C traceparent shape:
	// 04-<32 hex>-<16 hex>-01. It is the one header the capture showed that the
	// probes had been omitting, and the trace id is plausibly correlated with
	// the page's telemetry rather than being decorative.
	req.Header.Set("x-flow-trace", flowTrace())
	req.Header.Set("Cookie", cookie)
	if !*direct {
		req.Header.Set("x-relay-target", "https://www.dola.com")
		req.Header.Set("x-relay-path", "/chat/completion?"+clientQuery())
	}

	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("  hello=%-12s http2=%-5v -> transport error: %v\n", *helloName, *useHTTP2, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	text := string(raw)

	code := ""
	if m := regexp.MustCompile(`"error_code":(\d+)`).FindStringSubmatch(text); m != nil {
		code = m[1]
	}
	conversation := ""
	if m := regexp.MustCompile(`"conversation_id":"(\d+)"`).FindStringSubmatch(text); m != nil {
		conversation = m[1]
	}
	verdict := "refused"
	if strings.Contains(text, "SSE_ACK") {
		verdict = "ACCEPTED"
	}

	fmt.Printf("  hello=%-12s http2=%-5v proto=%-3s -> %-8s http=%d code=%s conv=%s events=%d %s\n",
		*helloName, *useHTTP2, resp.Proto, verdict, resp.StatusCode,
		orDash(code), orDash(conversation), strings.Count(text, "event:"),
		time.Since(started).Round(time.Millisecond))
	if verdict != "ACCEPTED" && len(text) > 0 {
		fmt.Printf("      head: %s\n", firstLine(text, 200))
	}
}

func helloKeys() []string {
	out := make([]string, 0, len(hellos))
	for k := range hellos {
		out = append(out, k)
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func firstLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// newClient builds an HTTP client whose TLS handshake carries the borrowed
// fingerprint.
func newClient(host string, hello utls.ClientHelloID, useHTTP2 bool, timeout time.Duration) (*http.Client, error) {
	dialTLS := func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		serverName := host
		if h, _, err := net.SplitHostPort(addr); err == nil {
			serverName = h
		}
		uconn := utls.UClient(raw, &utls.Config{ServerName: serverName}, hello)
		if err := uconn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("handshake: %w", err)
		}
		return uconn, nil
	}

	if useHTTP2 {
		return &http.Client{
			Timeout: timeout,
			Transport: &http2.Transport{
				DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
					return dialTLS(ctx, network, addr)
				},
			},
		}, nil
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialTLS(ctx, network, addr)
			},
		},
	}, nil
}

// clientQuery is the twenty-parameter set captured from the working browser.
func clientQuery() string {
	params := url.Values{}
	for k, v := range map[string]string{
		"aid": "495671", "device_id": device["device_id"],
		"device_platform": "web", "doubao_device_platform": "web",
		"doubao_pc_version": "3.41.0", "fp": device["fp"], "language": "",
		"pc_version": "3.41.0", "pkg_type": "release_version", "real_aid": "495671",
		"region": "JP", "samantha_web": "1", "sys_region": "JP",
		"tea_uuid": device["tea_uuid"], "tz_name": device["tz_name"],
		"use-olympus-account": "1", "version_code": "20800", "web_id": device["web_id"],
		"web_platform": "browser", "web_tab_id": randomUUID(),
	} {
		params.Set(k, v)
	}
	return params.Encode()
}

func chatBody(prompt string) ([]byte, error) {
	now := time.Now().UnixMilli()
	payload := map[string]any{
		"client_meta": map[string]any{
			"local_conversation_id": fmt.Sprintf("local_%d", now), "conversation_id": "",
			"bot_id": "7339470689562525703", "last_section_id": "", "last_message_index": nil,
		},
		"messages": []any{map[string]any{
			"local_message_id": randomUUID(),
			"content_block": []any{map[string]any{
				"block_type": 10000,
				"content": map[string]any{
					"text_block": map[string]any{
						"text": prompt, "icon_url": "", "icon_url_dark": "", "summary": "",
					},
					"pc_event_block": "",
				},
				"block_id": randomUUID(), "parent_id": "",
				"meta_info": []any{}, "append_fields": []any{},
			}},
			"message_status": 0,
		}},
		"option": map[string]any{
			"send_message_scene": "", "create_time_ms": now, "collect_id": "", "is_audio": false,
			"answer_with_suggest": false, "tts_switch": false, "need_deep_think": 0,
			"click_clear_context": false, "from_suggest": false, "is_regen": false, "is_replace": false,
			"is_from_click_option": false, "disable_sse_cache": false, "select_text_action": "",
			"is_select_text": false, "resend_for_regen": false, "scene_type": 0,
			"unique_key": randomUUID(), "start_seq": 0, "need_create_conversation": true,
			"conversation_init_option": map[string]any{"need_ack_conversation": true},
			"regen_query_id":           []any{}, "edit_query_id": []any{}, "regen_instruction": "",
			"no_replace_for_regen": false, "message_from": 0, "shared_app_name": "", "shared_app_id": "",
			"sse_recv_event_options": map[string]any{"support_chunk_delta": true},
			"is_ai_playground":       false, "is_old_user": false,
			"recovery_option": map[string]any{
				"is_recovery": false, "req_create_time_sec": now / 1000, "append_sse_event_scene": 0,
			},
			"message_storage_type": 0,
		},
		"user_context": []any{},
		"ext": map[string]any{
			"fp": device["fp"], "use_deep_think": "0", "sub_conv_firstmet_type": "1",
			"collection_id": "", "conversation_init_option": `{"need_ack_conversation":true}`,
			"commerce_credit_config_enable": "0",
		},
	}
	return json.Marshal(payload)
}

// flowTrace builds a value shaped like the browser's x-flow-trace header.
func flowTrace() string {
	b := make([]byte, 24)
	f, err := os.Open("/dev/urandom")
	if err == nil {
		_, _ = io.ReadFull(f, b)
		f.Close()
	} else {
		for i := range b {
			b[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	return fmt.Sprintf("04-%x-%x-01", b[:16], b[16:24])
}

func randomUUID() string {
	b := make([]byte, 16)
	f, err := os.Open("/dev/urandom")
	if err == nil {
		_, _ = io.ReadFull(f, b)
		f.Close()
	} else {
		for i := range b {
			b[i] = byte(time.Now().UnixNano() >> (i % 8))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func readCookie(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line, nil
	}
	return "", fmt.Errorf("no cookie line in %s", filepath.Base(path))
}
