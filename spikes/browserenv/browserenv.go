// Package browserenv holds the browser stubs the ByteDance SDKs need when they
// run outside a browser.
//
// Both the bdms SDK (which produces X-Bogus) and the webmssdk (which produces
// a_bogus) do environment probing, so they need window, navigator, screen and
// friends to exist. The probes are wrapped in try/catch upstream, so anything
// left out reads as "not detected" rather than failing the load, and the honest
// answer for a server-side client is that nothing is detected.
package browserenv

// Prelude is evaluated before an SDK is loaded.
//
// Two stubs are not cosmetic and were both found the hard way:
//
//   - atob must really decode. The SDKs hand it a base64 VM payload and read
//     the bytes back out; a stub returning "" makes the payload decrypt to
//     nothing and the entry module throws on the empty result.
//   - Uint8Array.from must accept a string, as it does in a browser. goja
//     rejects one, and the SDKs call Uint8Array.from(atob(...), mapper) while
//     installing that payload.
const Prelude = `
var window = this;
var self = window;
var globalThis = window;
window.window = window;
window.self = window;

window.navigator = {
  userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
  platform: "Win32", webdriver: false, languages: ["zh-CN", "zh"], language: "zh-CN",
  hardwareConcurrency: 8, deviceMemory: 8, vendor: "Google Inc.", cookieEnabled: true,
  vendorSubs: { ink: 0 }, plugins: { length: 0 }, mimeTypes: { length: 0 }
};
window.location = { href: "https://www.dola.com/chat/", host: "www.dola.com",
  hostname: "www.dola.com", protocol: "https:", pathname: "/chat/", search: "", hash: "",
  origin: "https://www.dola.com" };
window.screen = { width: 1920, height: 1080, availWidth: 1920, availHeight: 1040,
  colorDepth: 24, pixelDepth: 24, availLeft: 0, availTop: 0 };
window.innerWidth = 1920; window.innerHeight = 1080;
window.outerWidth = 1920; window.outerHeight = 1080;
window.devicePixelRatio = 1;
window.document = {
  cookie: "", referrer: "", hidden: false, visibilityState: "visible", title: "Dola",
  writeln: function () {}, write: function () {},
  createElement: function () { return { style: {}, getContext: function () { return null; },
    setAttribute: function () {}, appendChild: function () {} }; },
  createEvent: function () { return { initEvent: function () {} }; },
  getElementById: function () { return null; },
  querySelector: function () { return null; },
  querySelectorAll: function () { return []; },
  addEventListener: function () {}, removeEventListener: function () {},
  documentElement: { style: {}, clientWidth: 1920, clientHeight: 1080 },
  body: { style: {}, appendChild: function () {}, addEventListener: function () {} },
  head: { appendChild: function () {} }
};
window.performance = { now: function () { return Date.now(); }, timeOrigin: Date.now(),
  getEntriesByType: function () { return []; }, getEntries: function () { return []; }, timing: {} };
var __mkStorage = function () { var d = {}; return {
  getItem: function (k) { return Object.prototype.hasOwnProperty.call(d, k) ? d[k] : null; },
  setItem: function (k, v) { d[k] = String(v); }, removeItem: function (k) { delete d[k]; },
  clear: function () { d = {}; }, key: function () { return null; }, length: 0 }; };
window.localStorage = __mkStorage();
window.sessionStorage = __mkStorage();
window.console = { log: function () {}, warn: function () {}, error: function () {},
  info: function () {}, debug: function () {}, trace: function () {} };
window.setTimeout = function () { return 0; };
window.clearTimeout = function () {};
window.setInterval = function () { return 0; };
window.clearInterval = function () {};
window.addEventListener = function () {};
window.removeEventListener = function () {};
window.crypto = { getRandomValues: function (a) {
  for (var i = 0; i < a.length; i++) { a[i] = Math.floor(Math.random() * 256); } return a; } };

window.atob = function (input) {
  var chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=";
  var s = String(input).replace(/[^A-Za-z0-9+/=]/g, "");
  var out = "", bits = 0, nbits = 0;
  for (var i = 0; i < s.length; i++) {
    var c = s.charAt(i);
    if (c === "=") { break; }
    var v = chars.indexOf(c);
    if (v < 0) { continue; }
    bits = (bits << 6) | v;
    nbits += 6;
    if (nbits >= 8) { nbits -= 8; out += String.fromCharCode((bits >> nbits) & 255); }
  }
  return out;
};
window.btoa = function (input) {
  var chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  var s = String(input), out = "";
  for (var i = 0; i < s.length; i += 3) {
    var c1 = s.charCodeAt(i), c2 = s.charCodeAt(i + 1), c3 = s.charCodeAt(i + 2);
    out += chars.charAt(c1 >> 2);
    out += chars.charAt(((c1 & 3) << 4) | (isNaN(c2) ? 0 : c2 >> 4));
    out += isNaN(c2) ? "=" : chars.charAt(((c2 & 15) << 2) | (isNaN(c3) ? 0 : c3 >> 6));
    out += isNaN(c3) ? "=" : chars.charAt(c3 & 63);
  }
  return out;
};

// goja's Uint8Array.from rejects a string, while a browser accepts one. A
// browser passes each character to the mapper and each code point when there is
// no mapper; the SDK's mapper calls charCodeAt on what it receives.
(function () {
  var nativeFrom = Uint8Array.from;
  Uint8Array.from = function (source, mapFn, thisArg) {
    if (typeof source === "string") {
      var elems = new Array(source.length);
      for (var i = 0; i < source.length; i++) {
        elems[i] = mapFn === undefined ? source.charCodeAt(i) : source.charAt(i);
      }
      source = elems;
    }
    return mapFn === undefined
      ? nativeFrom.call(Uint8Array, source)
      : nativeFrom.call(Uint8Array, source, mapFn, thisArg);
  };
})();

// The XHR and fetch stubs record every call. An SDK installs its signing
// interceptor *on top of* these, so a URL that arrives here already carries
// whatever the interceptor appended, which is how a_bogus is observed.
window.__calls = [];
window.XMLHttpRequest = function () {
  this.open = function (method, url) {
    window.__calls.push("XHR " + String(method) + " " + String(url));
  };
  this.send = function () {};
  this.setRequestHeader = function () {};
  this.getAllResponseHeaders = function () { return ""; };
  this.abort = function () {};
  this.readyState = 0;
  this.status = 0;
};
window.fetch = function (input) {
  var u = String(typeof input === "string" ? input : (input && input.url));
  window.__calls.push("fetch " + u);
  return Promise.resolve({ ok: true, status: 200, headers: { get: function () { return null; } },
    json: function () { return Promise.resolve({}); }, text: function () { return Promise.resolve(""); } });
};
window.WebSocket = function () { this.send = function () {}; this.close = function () {}; };
window.Image = function () {};
window.CanvasRenderingContext2D = function () {};

// A chunk file only registers its modules by pushing them here; the runtime
// that installs them lives in the app's main bundle. Keep the array real,
// because the chunk's own runtime reads it back, and record the payload so a
// minimal loader can install the modules itself.
window.__bdmsPushed = null;
window.__LOADABLE_LOADED_CHUNKS__ = [];
window.__LOADABLE_LOADED_CHUNKS__.push = function (data) {
  window.__bdmsPushed = data;
  return Array.prototype.push.call(window.__LOADABLE_LOADED_CHUNKS__, data);
};
`
