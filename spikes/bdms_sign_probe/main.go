// Command bdms_sign_probe gets a real a_bogus out of the page's own SDK.
//
// The pieces, all measured rather than guessed:
//
//   - the signer is the bdms chunk (bdms-sdk.<hash>.js), which the page loads
//     lazily and initialises itself. The mssdk at /mssdk/webmssdk_1.0.0.44.es5.js
//     is a second SDK that produces X-Bogus, not a_bogus.
//   - the page's own call site builds the config and passes it to bdms.init:
//     let c = { aid, pageId, paths, ic, ddrt };
//     window.bdms.init(c);
//     with, from the same bundle,
//     pageId: 26930, ic: 13, ddrt: 13,
//     paths.include = ["/alice","/samantha","/passport","/biz",
//     "/chat/completion","/chat/async/chunk_stream"]
//   - once initialised, the SDK patches XMLHttpRequest, and the query it signs
//     is the one that leaves, with a fresh msToken and a_bogus appended.
//
// So this probe loads the chunk, calls init with that config, drives an XHR
// through the recording stubs, and prints the URL that comes out the bottom.
// The bdms chunk is ByteDance's code and is deliberately not committed.
//
// Usage:
//
//	go run ./bdms_sign_probe <path to bdms-sdk.js> ["<query string>"]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"dolaspikes/browserenv"

	"github.com/dop251/goja"
)

const defaultQuery = "aid=495671&device_id=7686671608228726325&device_platform=web" +
	"&doubao_device_platform=web&doubao_pc_version=3.41.0&fp=verify_test" +
	"&language=&pc_version=3.41.0&pkg_type=release_version&real_aid=495671" +
	"&region=JP&samantha_web=1&sys_region=JP&tea_uuid=7695956629460485685" +
	"&tz_name=Asia%2FShanghai&use-olympus-account=1&version_code=20800" +
	"&web_id=7695956629460485685&web_platform=browser&web_tab_id=fd56c268-32f4-4d08-9a53-76fdd3444e88"

func main() {
	path := os.Getenv("DOLA_BDMS")
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	query := defaultQuery
	if len(os.Args) > 2 {
		query = os.Args[2]
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "usage: bdms_sign_probe <path to bdms-sdk.js> [query]")
		os.Exit(2)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("bdms: %s (%d bytes)\n", path, len(src))

	vm := goja.New()
	if _, err := vm.RunScript("prelude.js", browserenv.Prelude); err != nil {
		fail("prelude: %v", err)
	}

	// The chunk only registers its modules; the runtime that installs them lives
	// in the app's main bundle. Prelude captures the push, the chunk pushes, and
	// ChunkLoader installs the modules and runs the entry that assigns
	// window.bdms.
	start := time.Now()
	if _, err := vm.RunScript("bdms-sdk.js", string(src)); err != nil {
		fail("bdms load failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	}
	fmt.Printf("chunk evaluated in %s\n", time.Since(start).Round(time.Millisecond))

	if _, err := vm.RunScript("chunkloader.js", browserenv.ChunkLoader); err != nil {
		fail("chunk loader: %v", err)
	}
	fmt.Printf("modules installed in %s (entry %v, %v modules)\n",
		time.Since(start).Round(time.Millisecond),
		vm.Get("__bdmsEntry"), vm.Get("__bdmsModuleCount"))

	// Report what the SDK installed, so a missing bdms is obvious.
	if v := vm.Get("bdms"); v == nil || goja.IsUndefined(v) {
		fmt.Println("window.bdms: NOT SET")
	} else if obj, ok := v.(*goja.Object); ok {
		fmt.Printf("window.bdms keys: %v\n", obj.Keys())
	}

	script := `
(function () {
  var out = { steps: [], signed: null };
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
  // The SDK may replace the constructor rather than wrap its prototype, which
  // would mean the call never reaches the recording stub. Report what happened
  // to XMLHttpRequest before and after init, because "no a_bogus" and "the call
  // went somewhere else" look identical from the outside.
  out.before = describeXHR();
  try {
    if (window.bdms && typeof window.bdms.init === "function") {
      window.bdms.init(cfg);
      out.steps.push("bdms.init ok");
    } else {
      out.steps.push("bdms.init missing");
    }
  } catch (e) {
    out.steps.push("bdms.init threw: " + (e && e.message ? e.message : e));
  }
  // The mssdk is the other signer the page loads. Its init takes the same shape
  // of config, and it is the one whose frontierSign exists, so it is worth
  // initialising with the page's own paths rather than a bare aid.
  try {
    if (window.byted_acrawler && typeof window.byted_acrawler.init === "function") {
      window.byted_acrawler.init(cfg);
      out.steps.push("byted_acrawler.init ok");
    } else {
      out.steps.push("byted_acrawler absent");
    }
  } catch (e) {
    out.steps.push("byted_acrawler.init threw: " + (e && e.message ? e.message : e));
  }
  out.after = describeXHR();

  // The SDK signs what it sees; the stubs record what leaves.
  try {
    window.__calls.length = 0;
    var x = new XMLHttpRequest();
    x.open("POST", "https://www.dola.com/chat/completion?" + QUERY);
    x.send("{}");
    out.calls = window.__calls.slice();
  } catch (e) {
    out.steps.push("xhr threw: " + (e && e.message ? e.message : e));
  }
  window.__out = JSON.stringify(out);
})();
`
	script = strings.Replace(script, "QUERY", jsString(query), 1)
	script = strings.Replace(script, "var out = { steps: [], signed: null };", `var out = { steps: [], signed: null };
  function describeXHR() {
    try {
      var proto = XMLHttpRequest.prototype;
      return {
        ctorName: String(XMLHttpRequest.name),
        hasOpen: typeof proto.open,
        openSource: String(proto.open).slice(0, 80),
        sameAsStub: window.__calls ? (String(proto.open).indexOf("__calls") >= 0) : null,
        hasSend: typeof proto.send
      };
    } catch (e) { return { error: String(e) }; }
  }`, 1)
	if _, err := vm.RunScript("sign.js", script); err != nil {
		fail("sign probe: %v", err)
	}
	raw := vm.Get("__out").String()

	var out struct {
		Steps  []string       `json:"steps"`
		Calls  []string       `json:"calls"`
		Before map[string]any `json:"before"`
		After  map[string]any `json:"after"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		fmt.Println(raw)
		return
	}
	for _, s := range out.Steps {
		fmt.Println("  " + s)
	}
	fmt.Printf("\nXMLHttpRequest before init: %v\n", out.Before)
	fmt.Printf("XMLHttpRequest after init:  %v\n", out.After)
	fmt.Println("\nURLs reaching the transport layer:")
	signed := ""
	for _, c := range out.Calls {
		fmt.Printf("  %s\n", c)
		if strings.Contains(c, "a_bogus=") {
			signed = c
		}
	}
	if signed == "" {
		fmt.Println("\nno a_bogus was appended")
		return
	}
	idx := strings.Index(signed, "a_bogus=")
	value := signed[idx+len("a_bogus="):]
	if amp := strings.Index(value, "&"); amp >= 0 {
		value = value[:amp]
	}
	fmt.Printf("\na_bogus (%d chars): %s\n", len(value), value)
}

// jsString quotes a string for embedding in JavaScript.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
