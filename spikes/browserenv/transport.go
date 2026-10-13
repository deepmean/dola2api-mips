package browserenv

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// errNoPrelude is returned when a helper is installed before the prelude, so
// window does not exist yet.
var errNoPrelude = fmt.Errorf("browserenv: install the prelude first")

// Transport is a real XMLHttpRequest and fetch, backed by net/http, for an SDK
// running under goja.
//
// This exists because the dola signing SDK will not sign anything until it has
// exchanged a token with mssdk.bytedance.com/web/r/token, and a stub that
// returns nothing leaves its interceptor passing URLs through unsigned. The
// router has a network anyway, so the honest fix is to let the SDK use it.
//
// The emulation is deliberately synchronous: send() performs the request and
// fires the ready-state callbacks before returning. Browser SDKs written for
// XHR are usually happy with that, and it keeps the probe a single-threaded
// script. A truly async SDK would need a job pump instead.
type Transport struct {
	Client *http.Client
	// Relay, when set, forwards through a relay that takes the real target in
	// x-relay-target and x-relay-path headers.
	Relay string
	// DefaultHeaders are applied to every request (user agent, cookie).
	DefaultHeaders map[string]string
	// Log, when set, receives one line per request and response.
	Log func(string)
}

// NewTransport returns a transport with a sane default client and user agent.
func NewTransport() *Transport {
	return &Transport{
		Client: &http.Client{Timeout: 30 * time.Second},
		DefaultHeaders: map[string]string{
			"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
				"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		},
	}
}

func (t *Transport) logf(format string, args ...any) {
	if t.Log != nil {
		t.Log(fmt.Sprintf(format, args...))
	}
}

// Install replaces window.XMLHttpRequest and window.fetch in vm.
func (t *Transport) Install(vm *goja.Runtime) error {
	window := vm.Get("window")
	if window == nil || goja.IsUndefined(window) {
		return fmt.Errorf("browserenv: install the prelude first")
	}
	obj, ok := window.(*goja.Object)
	if !ok {
		return fmt.Errorf("browserenv: window is not an object")
	}

	constructor := func(call goja.ConstructorCall) *goja.Object {
		return t.newXHR(vm, call.This)
	}
	if err := obj.Set("XMLHttpRequest", constructor); err != nil {
		return err
	}
	if err := obj.Set("fetch", t.newFetch(vm)); err != nil {
		return err
	}
	return nil
}

// xhrState is the per-instance state of an emulated XMLHttpRequest.
type xhrState struct {
	method  string
	url     string
	headers map[string]string
	body    string
}

func (t *Transport) newXHR(vm *goja.Runtime, this *goja.Object) *goja.Object {
	state := &xhrState{headers: map[string]string{}}

	this.Set("readyState", 0)
	this.Set("status", 0)
	this.Set("responseText", "")
	this.Set("response", "")
	this.Set("withCredentials", false)
	this.Set("onreadystatechange", goja.Undefined())
	this.Set("onload", goja.Undefined())
	this.Set("onerror", goja.Undefined())

	this.Set("open", func(call goja.FunctionCall) goja.Value {
		state.method = strings.ToUpper(call.Argument(0).String())
		state.url = call.Argument(1).String()
		this.Set("readyState", 1)
		return goja.Undefined()
	})
	this.Set("setRequestHeader", func(call goja.FunctionCall) goja.Value {
		state.headers[call.Argument(0).String()] = call.Argument(1).String()
		return goja.Undefined()
	})
	this.Set("getAllResponseHeaders", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue("")
	})
	this.Set("getResponseHeader", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue("")
	})
	this.Set("abort", func(call goja.FunctionCall) goja.Value { return goja.Undefined() })

	this.Set("send", func(call goja.FunctionCall) goja.Value {
		if !call.Argument(0).Equals(goja.Undefined()) && !goja.IsNull(call.Argument(0)) {
			state.body = call.Argument(0).String()
		}
		status, text, err := t.do(vm, state)
		if err != nil {
			t.logf("XHR %s %s -> error: %v", state.method, state.url, err)
			this.Set("status", 0)
			if fn, ok := goja.AssertFunction(this.Get("onerror")); ok {
				_, _ = fn(this)
			}
			return goja.Undefined()
		}
		this.Set("status", status)
		this.Set("responseText", text)
		this.Set("response", text)
		this.Set("readyState", 4)
		// Fire the callbacks the SDK registered, in the browser's order.
		for _, name := range []string{"onreadystatechange", "onload"} {
			if fn, ok := goja.AssertFunction(this.Get(name)); ok {
				_, _ = fn(this)
			}
		}
		return goja.Undefined()
	})

	return this
}

// do performs the request, optionally through the relay.
func (t *Transport) do(vm *goja.Runtime, state *xhrState) (int, string, error) {
	target := state.url
	req, err := t.buildRequest(state.method, target, state.body, state.headers)
	if err != nil {
		return 0, "", err
	}
	t.logf("XHR %s %s", state.method, target)
	resp, err := t.Client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, "", err
	}
	text := string(raw)
	t.logf("  -> %d (%d bytes)", resp.StatusCode, len(raw))
	return resp.StatusCode, text, nil
}

func (t *Transport) buildRequest(method, target, body string, headers map[string]string) (*http.Request, error) {
	endpoint := target
	extra := map[string]string{}
	if t.Relay != "" {
		u, err := url.Parse(target)
		if err != nil {
			return nil, fmt.Errorf("browserenv: parse %q: %w", target, err)
		}
		extra["x-relay-target"] = u.Scheme + "://" + u.Host
		extra["x-relay-path"] = u.RequestURI()
		endpoint = t.Relay
	}

	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range t.DefaultHeaders {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	return req, nil
}

// newFetch returns a fetch that resolves with a minimal Response object.
func (t *Transport) newFetch(vm *goja.Runtime) func(call goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		method := http.MethodGet
		body := ""
		headers := map[string]string{}
		if opt := call.Argument(1); opt != nil && !goja.IsUndefined(opt) {
			if o, ok := opt.(*goja.Object); ok {
				if m := o.Get("method"); m != nil && !goja.IsUndefined(m) {
					method = strings.ToUpper(m.String())
				}
				if b := o.Get("body"); b != nil && !goja.IsUndefined(b) {
					body = b.String()
				}
				if h := o.Get("headers"); h != nil && !goja.IsUndefined(h) {
					if ho, ok := h.(*goja.Object); ok {
						for _, k := range ho.Keys() {
							headers[k] = ho.Get(k).String()
						}
					}
				}
			}
		}

		promise, resolve, _ := vm.NewPromise()
		status, text, err := t.do(vm, &xhrState{method: method, url: url, body: body, headers: headers})
		if err != nil {
			_ = resolve(vm.ToValue(map[string]any{"ok": false, "status": 0, "text": ""}))
			return vm.ToValue(promise)
		}
		resp := vm.NewObject()
		resp.Set("ok", status >= 200 && status < 300)
		resp.Set("status", status)
		resp.Set("url", url)
		resp.Set("text", func(goja.FunctionCall) goja.Value { return vm.ToValue(text) })
		resp.Set("json", func(goja.FunctionCall) goja.Value { return vm.ToValue(text) })
		hdrs := vm.NewObject()
		hdrs.Set("get", func(goja.FunctionCall) goja.Value { return goja.Null() })
		resp.Set("headers", hdrs)
		_ = resolve(resp)
		return vm.ToValue(promise)
	}
}
