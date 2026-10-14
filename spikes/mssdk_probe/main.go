// Command mssdk_probe runs the webmssdk (the a_bogus signer) inside goja.
//
// a_bogus does not come from the bdms chunk: that SDK produces X-Bogus, and its
// source never contains the string "a_bogus". The page loads a second script,
// ${cdnBase}/mssdk/webmssdk_1.0.0.44.es5.js, which installs window.byted_acrawler
// and signs requests for the paths the app enables. This probe loads that
// script with the shared browser stubs and reports what the signer exposes.
//
// The script is ByteDance's code and is deliberately not committed; pass its
// path as the first argument (or set DOLA_MSSDK).
//
// Usage:
//
//	go run ./mssdk_probe ../../spikes/webmssdk.js
package main

import (
	"fmt"
	"os"
	"time"

	"dolaspikes/browserenv"

	"github.com/dop251/goja"
)

func main() {
	path := os.Getenv("DOLA_MSSDK")
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "usage: mssdk_probe <path to webmssdk.js>")
		os.Exit(2)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("mssdk: %s (%d bytes)\n", path, len(src))

	vm := goja.New()
	if _, err := vm.RunScript("prelude.js", browserenv.Prelude); err != nil {
		fail("prelude: %v", err)
	}
	fmt.Println("prelude: ok")

	start := time.Now()
	if _, err := vm.RunScript("webmssdk.js", string(src)); err != nil {
		fmt.Printf("mssdk FAILED after %s: %v\n", time.Since(start).Round(time.Millisecond), err)
		// The obfuscated SDK reports the failing call site; catching in JS gives
		// the real stack rather than goja's internal structures.
		os.Exit(1)
	}
	fmt.Printf("mssdk loaded in %s\n", time.Since(start).Round(time.Millisecond))

	acrawler := vm.Get("byted_acrawler")
	if acrawler == nil || goja.IsUndefined(acrawler) || goja.IsNull(acrawler) {
		fmt.Println("window.byted_acrawler: NOT SET")
		reportGlobals(vm)
		os.Exit(1)
	}
	obj, ok := acrawler.(*goja.Object)
	if !ok {
		fmt.Printf("byted_acrawler is %T, expected an object\n", acrawler)
		os.Exit(1)
	}
	fmt.Printf("\nbyted_acrawler keys: %v\n", obj.Keys())
	for _, k := range obj.Keys() {
		v := obj.Get(k)
		if fn, ok := goja.AssertFunction(v); ok {
			_ = fn
			fmt.Printf("  %-16s function (arity %d)\n", k, v.ToObject(vm).Get("length"))
		} else {
			fmt.Printf("  %-16s %T %v\n", k, v, v)
		}
	}

	fmt.Println("\nNext: call the signer with a request URL and compare against a captured a_bogus.")

	// frontierSign is the signer. Try the input shapes seen in public
	// implementations and report what comes back for each.
	probe := `
(function () {
  var out = { steps: [] };
  var ac = window.byted_acrawler;
  // The page's own config, recovered from the bundle:
  //   let c = { aid, pageId, paths, ic, ddrt }; window.bdms.init(c);
  // with pageId 26930 and the paths the app signs. A bare {aid} left the
  // interceptor installed but inactive, which is why nothing was appended.
  var cfg = {
    aid: 495671,
    pageId: 26930,
    ic: 13,
    ddrt: 13,
    paths: {
      include: ["/alice", "/samantha", "/passport", "/biz",
                "/chat/completion", "/chat/async/chunk_stream"],
      exclude: ["/samantha/notice/info", "/samantha/user/preference/get",
                "/samantha/user/ab/get", "/samantha/plugin/recommend/webtodesktop",
                "/samantha/guidance/get_task", "/samantha/guidance/draw_result"]
    }
  };
  try { ac.init(cfg); out.steps.push("init cfg ok"); }
  catch (e) { out.steps.push("init cfg threw: " + (e && e.message ? e.message : e)); }
  try { ac.setTTWid(""); out.steps.push("setTTWid ok"); } catch (e) {}
  try { ac.setUserMode(0); } catch (e) {}
  try { ac.setConfig && ac.setConfig("aid", 495671); } catch (e) {}

  // The bootstrap endpoints are obfuscated strings, so they cannot be found by
  // searching the source. If the SDK issues them itself, the recording stubs
  // see them: dump everything that arrived before this probe made any call of
  // its own.
  out.bootstrap = (window.__calls || []).slice();

  var query = "aid=495671&channel=g&device_platform=web&version_code=20800&web_platform=browser&msToken=TESTTOKEN%3D";
  var shapes = {
    stub: { "X-MS-STUB": query },
    url: { url: "/chat/completion?" + query },
    fullUrl: { url: "https://www.dola.com/chat/completion?" + query },
    rawString: query
  };
  out.results = {};
  for (var k in shapes) {
    try {
      var r = ac.frontierSign(shapes[k]);
      var entry = { keys: [], values: {} };
      for (var kk in r) {
        entry.keys.push(kk);
        entry.values[kk] = { len: String(r[kk]).length, head: String(r[kk]).slice(0, 60) };
      }
      out.results[k] = entry;
    } catch (e) {
      out.results[k] = { threw: String(e && e.message ? e.message : e) };
    }
  }
  window.__sigResult = JSON.stringify(out, null, 2);
})();
`
	if _, err := vm.RunScript("sign.js", probe); err != nil {
		fail("sign probe: %v", err)
	}
	fmt.Println(vm.Get("__sigResult"))

	// frontierSign only yields X-Bogus, so look for a_bogus where it actually
	// appears: on the request the SDK signs. Drive one through the recording
	// stubs and print whatever URL comes out the bottom.
	drive := `
(function () {
  var query = "aid=495671&channel=g&device_platform=web&version_code=20800&web_platform=browser"
            + "&msToken=TESTTOKEN%3D&fp=verify_test";
  window.__calls.length = 0;
  try {
    var x = new XMLHttpRequest();
    x.open("POST", "https://www.dola.com/chat/completion?" + query);
    x.send("{}");
  } catch (e) { window.__calls.push("xhr threw: " + (e && e.message ? e.message : e)); }
  try {
    window.fetch("https://www.dola.com/alice/user/config/pull?" + query, {});
  } catch (e) { window.__calls.push("fetch threw: " + (e && e.message ? e.message : e)); }
})();
`
	if _, err := vm.RunScript("drive.js", drive); err != nil {
		fail("drive probe: %v", err)
	}
	fmt.Println("\nURLs reaching the transport layer:")
	if calls := vm.Get("__calls"); calls != nil {
		if obj, ok := calls.(*goja.Object); ok {
			keys := obj.Keys()
			for _, k := range keys {
				fmt.Printf("  %s\n", obj.Get(k))
			}
			if len(keys) == 0 {
				fmt.Println("  (none)")
			}
		}
	}
}

// reportGlobals lists plausible signer names, for when byted_acrawler is absent.
func reportGlobals(vm *goja.Runtime) {
	names := []string{"_0x", "bytedacrawler", "bytedAcrawler", "webmssdk", "mssdk", "bdms"}
	for _, n := range names {
		if v := vm.Get(n); v != nil && !goja.IsUndefined(v) {
			fmt.Printf("  found global %s: %T\n", n, v)
		}
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
