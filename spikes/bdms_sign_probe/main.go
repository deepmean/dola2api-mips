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

	// The SDK probes the environment before it does anything, and a probe that
	// comes back "not a browser" is the likeliest reason a hook stays installed
	// but inert. These are the signals Chrome exposes and a minimal stub does
	// not: window.chrome, a non-empty plugin list, and a canvas whose getContext
	// answers. Installed before the SDK loads, because that is when it probes.
	if _, err := vm.RunScript("harden.js", harden); err != nil {
		fail("harden: %v", err)
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

  // The interceptor stays inert, so try the exported signer directly. A valid
  // a_bogus is 188-196 characters while X-Bogus is 16, so the length alone says
  // which one came back.
  try {
    var sig = window.bdms.frontierSign;
    out.signs = {};
    var shapes = {
      query: QUERY,
      pathQuery: "/chat/completion?" + QUERY,
      fullUrl: "https://www.dola.com/chat/completion?" + QUERY,
      stub: { "X-MS-STUB": QUERY }
    };
    for (var k in shapes) {
      try {
        var v = sig(shapes[k]);
        if (v && typeof v === "object") {
          var ks = Object.keys(v);
          out.signs[k] = { keys: ks };
          for (var j = 0; j < ks.length; j++) {
            out.signs[k][ks[j]] = { len: String(v[ks[j]]).length, head: String(v[ks[j]]).slice(0, 40) };
          }
        } else {
          out.signs[k] = { type: typeof v, len: String(v).length, head: String(v).slice(0, 40) };
        }
      } catch (e) {
        out.signs[k] = { threw: String(e && e.message ? e.message : e).slice(0, 80) };
      }
    }
  } catch (e) {
    out.steps.push("frontierSign threw: " + (e && e.message ? e.message : e));
  }

  // The SDK signs what it sees; the stubs record what leaves. The URL is
  // relative on purpose: the app requests "/chat/completion?...", and the
  // configured include list holds paths, so an absolute URL is a different
  // string for a matcher to compare against.
  try {
    window.__calls.length = 0;
    var x = new XMLHttpRequest();
    x.open("POST", "/chat/completion?" + QUERY);
    x.send("{}");
    out.calls = window.__calls.slice();
  } catch (e) {
    out.steps.push("xhr threw: " + (e && e.message ? e.message : e));
  }
  // And once with the absolute form, so the two can be compared.
  try {
    var y = new XMLHttpRequest();
    y.open("POST", "https://www.dola.com/chat/completion?" + QUERY);
    y.send("{}");
    out.callsAbs = window.__calls.slice();
  } catch (e) {
    out.steps.push("xhr abs threw: " + (e && e.message ? e.message : e));
  }
  window.__out = JSON.stringify(out);
})();
`
	script = strings.ReplaceAll(script, "QUERY", jsString(query))
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
		Steps    []string       `json:"steps"`
		Calls    []string       `json:\"calls\"`
		CallsAbs []string       `json:\"callsAbs\"`
		Before   map[string]any `json:"before"`
		After    map[string]any `json:"after"`
	}
	fmt.Println("RAW:", raw)
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
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

// harden adds the browser signals the SDK is most likely to probe. A stub that
// answers "not a browser" is enough to keep the signer switched off, and none of
// this costs anything on the router because the port will ship a Go
// implementation, not the SDK.
const harden = `
(function () {
  // window.chrome exists in Chrome and is the first thing a bot check looks at.
  if (!window.chrome) {
    window.chrome = {
      runtime: {},
      loadTimes: function () {},
      csi: function () {},
      app: { isInstalled: false }
    };
  }

  // A plugin list that is empty is a strong headless signal, so give it the
  // entries Chrome reports.
  var fakePlugin = { name: "PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" };
  var fakePlugins = [fakePlugin, { name: "Chrome PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" }, { name: "Chromium PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" }];
  try {
    Object.defineProperty(window.navigator, "plugins", {
      get: function () {
        var arr = fakePlugins.slice();
        arr.item = function (i) { return this[i] || null; };
        arr.namedItem = function (n) {
          for (var i = 0; i < this.length; i++) if (this[i].name === n) return this[i];
          return null;
        };
        return arr;
      },
      configurable: true
    });
  } catch (e) {}

  // Permissions.query answers "prompt" for notifications in Chrome; an
  // unstubbed Permissions leaves a signature that walks straight into it.
  try {
    if (window.navigator.permissions) {
      window.navigator.permissions.query = function (desc) {
        return Promise.resolve({ state: desc && desc.name === "notifications" ? "prompt" : "prompt" });
      };
    } else {
      window.navigator.permissions = { query: function () { return Promise.resolve({ state: "prompt" }); } };
    }
  } catch (e) {}

  // webdriver is the other classic check, and it should be false.
  try { Object.defineProperty(window.navigator, "webdriver", { get: function () { return false; }, configurable: true }); } catch (e) {}

  // A canvas that answers toDataURL, which some signers hash to bind the
  // signature to the environment.
  try {
    var proto = window.HTMLCanvasElement && window.HTMLCanvasElement.prototype;
    if (proto && !proto.getContext.__stubbed) {
      var realGetContext = proto.getContext;
      proto.getContext = function (type) {
        if (type === "2d") {
          return {
            fillRect: function () {}, fillText: function () {}, measureText: function () { return { width: 42 }; },
            getImageData: function () { return { data: new Array(16) }; },
            // A deterministic fill pattern, so the same input hashes the same.
            createLinearGradient: function () { return { addColorStop: function () {} }; },
            font: "10px sans-serif", textBaseline: "alphabetic", fillStyle: "#000", strokeStyle: "#000"
          };
        }
        try { return realGetContext ? realGetContext.apply(this, arguments) : null; } catch (e) { return null; }
      };
      proto.getContext.__stubbed = true;
    }
  } catch (e) {}
})();
`

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
