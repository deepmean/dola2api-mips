// Sign the port's own parameter set and report the result, so the acceptance
// test is one command: 710022004 means unsigned, 710022002 means the signature
// was rejected, SSE_ACK means it passed.
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
    language: "zh-CN",
    languages: ["zh-CN", "zh"],
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
  location: { href: "https://www.dola.com/chat/", protocol: "https:", host: "www.dola.com", hostname: "www.dola.com" },
  performance: { now: () => Date.now() },
  Date, Math, JSON, String, Number, Array, Object, Boolean, RegExp, Error, TypeError,
  Uint8Array, Uint16Array, Int32Array, encodeURIComponent, decodeURIComponent, console
};
const ctx = vm.createContext(sandbox);
vm.runInContext("var window = globalThis; var self = globalThis;", ctx);
for (const f of files) {
  vm.runInContext(fs.readFileSync(path.join(__dirname, f), "utf8"), ctx, { filename: f });
}

// The page builds its query alphabetically before signing, so the signature
// covers the same string the server reassembles.
// The recovered algorithm ships with the constants of the site it was taken
// from: aid 6383, pageId 7571. dola signs with its own, so they are set here
// before signing. Whether these are the only two that differ is exactly what the
// live test decides.
vm.runInContext("aid = 495671; pageId = 26930;", ctx);

const params = {
  aid: "495671",
  device_id: "7686671608228726325",
  device_platform: "web",
  doubao_device_platform: "web",
  doubao_pc_version: "3.41.0",
  fp: "verify_mv6q3fus_Oi5TeE8s_72in_4CGa_9k8g_RYDKyKyHA2UK",
  language: "",
  pc_version: "3.41.0",
  pkg_type: "release_version",
  real_aid: "495671",
  region: "JP",
  samantha_web: "1",
  sys_region: "JP",
  tea_uuid: "7695956629460485685",
  tz_name: "Asia/Shanghai",
  "use-olympus-account": "1",
  version_code: "20800",
  web_id: "7695956629460485685",
  web_platform: "browser",
  web_tab_id: require("crypto").randomUUID()
};
const sorted = Object.keys(params).sort().map((k) => `${k}=${params[k]}`).join("&");

const t0 = Date.now();
const sig = vm.runInContext(`makeABogus(${JSON.stringify(sorted)}, Date.now())`, ctx);
if (!sig) {
  console.error("signer produced nothing");
  process.exit(1);
}
console.log(JSON.stringify({ sorted, sig, len: sig.length, elapsed: Date.now() - t0 }));