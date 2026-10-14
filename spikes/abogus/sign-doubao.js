// Sign doubao's parameter set, as the control experiment for the algorithm.
//
// The point of this file is not dola. doubao and dola share the same anti-abuse
// system, and doubao's deployment parameters are known: aid 497858, pc_version
// 2.41.0. Signing those decides whether the recovered algorithm works at all, or
// whether only dola's variant does.
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const files = ["sm3.js", "utils.js", "vm_decode.js"];
const sandbox = {
  navigator: {
    userAgent:
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Safari/537.36",
    vendor: "Google Inc.",
    vendorSubs: {},
    platform: "Win32",
    language: "zh",
    languages: ["zh", "zh-CN"],
    appVersion: "5.0 (Windows)",
    maxTouchPoints: 0,
    hardwareConcurrency: 20,
    deviceMemory: 8,
    webdriver: false,
    plugins: { length: 3 },
    mimeTypes: { length: 2 },
    cookieEnabled: true,
    onLine: true,
    doNotTrack: null
  },
  screen: { width: 2560, height: 1440, availWidth: 2560, availHeight: 1440, colorDepth: 24, pixelDepth: 24 },
  location: { href: "https://www.doubao.com/chat/", protocol: "https:", host: "www.doubao.com", hostname: "www.doubao.com" },
  performance: { now: () => Date.now() },
  Date, Math, JSON, String, Number, Array, Object, Boolean, RegExp, Error, TypeError,
  Uint8Array, Uint16Array, Int32Array, encodeURIComponent, decodeURIComponent, console
};
const ctx = vm.createContext(sandbox);
vm.runInContext("var window = globalThis; var self = globalThis;", ctx);
for (const f of files) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, f), "utf8"), ctx, { filename: f });
}

// Doubao's values: aid from the working deployment, pageId is unknown for
// doubao and left at its default so the first attempt only varies what is known.
vm.runInContext("aid = 497858; pageId = 7571; sdkVersion = '1.0.1.19-fix.01';", ctx);

const fp = process.env.DOUBAO_FP || "verify_test";
const deviceId = process.env.DOUBAO_DEVICE_ID || "7686671608228726325";
const webId = process.env.DOUBAO_WEB_ID || "7695956629460485685";
const teaUuid = process.env.DOUBAO_TEA_UUID || "7695956629460485685";

const params = {
  aid: "497858",
  device_id: deviceId,
  device_platform: "web",
  fp: fp,
  language: "zh",
  pc_version: "2.41.0",
  pkg_type: "release_version",
  real_aid: "497858",
  tea_uuid: teaUuid,
  version_code: "170400",
  web_id: webId,
  web_platform: "browser",
  web_tab_id: process.argv[2] || require("crypto").randomUUID()
};
const msToken = process.env.ABOGUS_MS_TOKEN || "TESTTOKEN123";

const sorted = Object.keys(params)
  .map((k) => [k, `${k}=${params[k]}`])
  .concat([["msToken", `msToken=${msToken}`]])
  .sort((a, b) => (a[0] < b[0] ? -1 : 1))
  .map((x) => x[1])
  .join("&");

const t0 = Date.now();
const sig = vm.runInContext(`makeABogus(${JSON.stringify(sorted)}, Date.now())`, ctx);
if (!sig) {
  console.error("signer produced nothing");
  process.exit(1);
}
console.log(JSON.stringify({ sorted, sig, len: sig.length, elapsed: Date.now() - t0 }));