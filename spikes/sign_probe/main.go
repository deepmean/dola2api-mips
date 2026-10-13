// Command sign_probe runs dola's own signing SDK under goja with a real network
// underneath it, and reports whether a_bogus appears on the URL it signs.
//
// The chain: load the SDK chunk, install its modules with the minimal loader,
// call init() the way the app does, then drive a request through the transport
// the SDK patched. The SDK will not sign until it has exchanged a token with
// mssdk.bytedance.com/web/r/token, which is why the transport must be real.
//
// The SDK file is ByteDance's code and is deliberately not committed.
//
// Usage:
//
//	go run ./sign_probe <bdms-sdk.js> <cookies.txt> [relayURL]
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"dolaspikes/browserenv"

	"github.com/dop251/goja"
)

const dolaAID = 495671
const dolaPageID = 26930

func main() {
	sdkPath := "spikes/bdms-sdk.js"
	cookieFile := "cookies.txt"
	relay := ""
	if len(os.Args) > 1 {
		sdkPath = os.Args[1]
	}
	if len(os.Args) > 2 {
		cookieFile = os.Args[2]
	}
	if len(os.Args) > 3 {
		relay = os.Args[3]
	}

	src, err := os.ReadFile(sdkPath)
	if err != nil {
		fail("read sdk: %v", err)
	}
	cookie, err := readCookie(cookieFile)
	if err != nil {
		fail("read cookie: %v", err)
	}

	vm := goja.New()
	if _, err := vm.RunScript("prelude.js", browserenv.Prelude); err != nil {
		fail("prelude: %v", err)
	}
	// The SDK reads its tokens out of document.cookie, so give it the real jar.
	if _, err := vm.RunScript("cookie.js", fmt.Sprintf("window.document.cookie = %q;", cookie)); err != nil {
		fail("cookie: %v", err)
	}

	transport := browserenv.NewTransport()
	if relay != "" {
		transport.Relay = relay
	}
	transport.DefaultHeaders["Cookie"] = cookie
	requests := []string{}
	transport.Log = func(line string) {
		fmt.Printf("  [net] %s\n", line)
		if strings.Contains(line, "XHR ") || strings.Contains(line, "fetch ") {
			requests = append(requests, line)
		}
	}
	if err := transport.Install(vm); err != nil {
		fail("transport: %v", err)
	}
	timers := browserenv.NewTimers()
	if err := timers.Install(vm); err != nil {
		fail("timers: %v", err)
	}
	fmt.Println("prelude + transport + timers: ok")

	start := time.Now()
	if _, err := vm.RunScript("bdms-sdk.js", string(src)); err != nil {
		fail("sdk: %v", err)
	}
	if _, err := vm.RunScript("loader.js", browserenv.ChunkLoader); err != nil {
		fail("loader: %v", err)
	}
	fmt.Printf("sdk booted in %s (entry=%v, modules=%v)\n",
		time.Since(start).Round(time.Millisecond), vm.Get("__bdmsEntry"), vm.Get("__bdmsModuleCount"))
	if e := vm.Get("__bdmsErr"); e != nil && !goja.IsNull(e) && !goja.IsUndefined(e) {
		if obj, ok := e.(*goja.Object); ok {
			fail("entry module threw: %v\n%s", obj.Get("message"), obj.Get("stack"))
		}
	}

	// init() the way the app does, in its own script so goja drains the
	// microtask queue its readiness chain depends on.
	initScript := fmt.Sprintf(`
(function () {
  try {
    window.bdms.init({
      aid: %d, pageId: %d,
      paths: { include: ["/alice", "/samantha", "/passport", "/biz", "/chat/completion", "/chat/async/chunk_stream"],
               exclude: ["/samantha/notice/info"] },
      ic: 13, ddrt: 13
    });
    window.__initOk = true;
  } catch (e) { window.__initOk = false; window.__initErr = String(e); }
})();
`, dolaAID, dolaPageID)
	if _, err := vm.RunScript("init.js", initScript); err != nil {
		fail("init: %v", err)
	}
	fmt.Printf("init ok: %v\n", vm.Get("__initOk"))
	// Pump the timers: the SDK defers its token exchange, so nothing happens
	// until its scheduled callbacks run.
	for i := 0; i < 6; i++ {
		if _, err := vm.RunScript("tick.js", "0"); err != nil {
			fail("tick: %v", err)
		}
		if n := timers.RunPending(500 * time.Millisecond); n > 0 {
			fmt.Printf("timers: ran %d callback(s), %d still armed\n", n, timers.Pending())
		}
	}

	// Drive a request the SDK should sign.
	drive := `
(function () {
  var query = "aid=495671&channel=g&device_platform=web&version_code=20800&web_platform=browser";
  window.__result = null;
  try {
    var x = new XMLHttpRequest();
    x.open("POST", "https://www.dola.com/chat/completion?" + query);
    x.setRequestHeader("Content-Type", "application/json");
    x.send("{}");
    window.__result = "sent";
  } catch (e) { window.__result = "threw: " + (e && e.message ? e.message : e); }
})();
`
	if _, err := vm.RunScript("drive.js", drive); err != nil {
		fail("drive: %v", err)
	}
	fmt.Printf("drive: %v\n", vm.Get("__result"))

	fmt.Println("\nrequests the SDK made:")
	found := false
	for _, r := range requests {
		fmt.Printf("  %s\n", r)
		if i := strings.Index(r, "a_bogus="); i >= 0 {
			fmt.Printf("\n  => a_bogus: %s\n", r[i:])
			found = true
		}
	}
	if !found {
		fmt.Println("  (no a_bogus on any URL)")
	}
}

func readCookie(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line != "" && !strings.HasPrefix(line, "#") {
			return line, nil
		}
	}
	return "", fmt.Errorf("no cookie line in %s", path)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
