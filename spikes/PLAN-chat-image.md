# PLAN — text chat and image generation on the MIPS port

Feasibility and cost plan. **No code was written for the features**; the only files added are
read-only probes under `spikes/` (listed in §9) and this document.

Evidence labels used throughout:

| label | meaning |
| --- | --- |
| **MEASURED** | I sent the request myself and read the response, or read the field out of a live response body. Raw artefacts are in `spikes/`. |
| **SERVER-CONFIG** | Read from a live *configuration* endpoint of the same account (no generation, no quota). Still live, but a config value rather than an observed generation. |
| **CLIENT-SIDE** | Read out of the web app's own JavaScript or out of a third-party client that ships against this API. Strong, but it is someone's code, not my observation. |
| **INFERRED** | Follows from the above by an argument I spell out. |
| **GUESS** | I do not know. Flagged as such; do not build on it without a live test. |

---

## 0. Verdict

| feature | feasible with the protocol we already speak? | can it be confirmed today? |
| --- | --- | --- |
| **Text chat** | **Yes, on the evidence** — same endpoint, same body shape, no signature, no new dependency, and there is a third-party client in production against exactly this endpoint. The one part I could not observe is the text-delta payload (§4.2.4). | **No.** The submit gate is closed for this account right now (§2.3). |
| **Image generation** | **Already implemented** in the working tree (`POST /v1/images`, `SubmitRequest.Kind`, `mediaOf` type-1 handling). Two concrete gaps: the URL it picks is the *watermarked* rendition, and the ability type is still hardcoded to the video skill. | **No**, same gate. The *response* side is fully measured, though, from an image the account already produced (§4.3). |

The single thing that blocks everything is not in this plan's scope: the upstream risk-control
layer currently answers `710022004` to any unsigned submit, which is exactly the state the
`74295b9` commit message describes for the throttled window (§2.3). Chat and image are behind
the *same* gate as video, so if video cannot submit today, neither can these.

---

## 1. What I ran and what I read

Probes (new, all read-only apart from the chat submits in §2.3):

| file | what it does |
| --- | --- |
| `spikes/chat_probe.py` | one `/chat/completion` submit with a chosen ability, prints every SSE frame, then polls the conversation |
| `spikes/omniroute_probe.py` | the same submit using the public OmniRoute client's exact request shape |
| `spikes/poll_dump.py` | lists the account's conversations and dumps every content block of each — **no signature, no quota** |
| `spikes/skill_probe.py` | `POST /samantha/skill/recommend` — the server's own skill list |
| `spikes/skill_pack_probe.py`, `spikes/skill_pack_all.py` | `POST /samantha/skill/pack {"skill_type":N}` — the per-skill input config |
| `spikes/_pack_dump.py` | pretty-printer for the above |

Sources read: `dola.go`, `pool.go`, `api.go`, `chat.go`, `models.go`, `store.go`, `executor.go`,
`config.go`, `deploy/dola2api.conf`; upstream `dola_client.py` (all 1321 lines);
`extensions/dola30/dola-skill-pack-response.json`; the downloaded web bundles in `%TEMP%`
(`chat_b.js`, `chunk_tu2.0b01a1caef.js`, `chunk_ity.44b20a87ee.js`, `chunk_kn`, `chunk_ng`, `chunk_gn`,
`sec_async-init-business.js`); and on the web: `diegosouzapw/OmniRoute` `open-sse/executors/doubao-web.ts`,
`ihmily/doubao-nomark`, `0xriverx/dola-reverse-report`.

---

## 2. The measured facts that matter

### 2.1 `chat_ability.ability_type` **is** the skill type — and the server says so

MEASURED, from a video conversation this port created (poll of `38418263229243665`):

```
messages[].ext.chat_ability = "{\"ability_type\":17,\"ability_param\":\"{\\\"ratio\\\":\\\"9:16\\\",...}\"}"
messages[].ext.input_skill  = "{\"skill_id\":\"17\",\"skill_type\":17}"
```

The Go port never sends `input_skill`. The server derived it from `ability_type` and wrote it back.
So on the wire, `ability_type` is the skill id, and `input_skill.skill_id` is its decimal string.

### 2.2 The skill ids, from two independent live sources plus the client

| id | skill | source |
| --- | --- | --- |
| **0** | FreeChat (plain text chat) | CLIENT-SIDE: `chat_b.js` @48540 and @452416 (`lg:` string map) |
| 2 | Write | CLIENT-SIDE + SERVER-CONFIG |
| **3** | **Image generation** (`圖片生成`) | SERVER-CONFIG: `latest_skills[] = {"skill_id":"3","skill_type":3,"show_name":"圖片生成","icon_url":".../ic_image.0320.svg"}`; `sidebar_skills[] = {"skill_type":3,"image_generation":{...}}`; `input_config` key `image_generation` |
| 4 | AI Search | SERVER-CONFIG |
| 5,6,7,1 | Translate, AI Reader, Webpage, Read-and-summarise | SERVER-CONFIG |
| 16 | Code assistant | CLIENT-SIDE (`SkillCodeAssistant=16`) |
| **17** | **Video generation** | MEASURED end-to-end (§2.1) + CLIENT-SIDE |

**This settles the image ability type.** `16` — the value the upstream `dola_client.py` uses for
image generation (`_build_image_body`, line 666) — is `SkillCodeAssistant`, and
`POST /samantha/skill/pack {"skill_type":16}` answers `710010702` (no such pack) while `{"skill_type":3}`
answers `code 0` with an `image` pack. The Python value was never exercised: `dola_client.DolaClient`
has no caller anywhere in the upstream repo (RESULT.md §7.1), so that branch is dead code that was
never validated. **Treat `ability_type=16` as wrong.**

### 2.3 The gate is currently closed — 7 submits, 4 request shapes, all refused

MEASURED today, all `HTTP 200` + `text/event-stream`, all ending in `STREAM_ERROR`:

| shape | result |
| --- | --- |
| `submitURL()` verbatim — no `a_bogus`, no `msToken` | `710022004` `"rate limited"`, `extra.decision.subtype = "slide"`, `from = shark_admin` |
| + `msToken` + `fp` from the cookie | `710022004` |
| + `msToken` + `fp` + the full `FAKE_HEADERS` set | `710022004` |
| OmniRoute's shape (`device_id`/`web_id`/`tea_uuid`/`real_aid`, `pc_version=3.25.3`, `Accept: text/event-stream`, `Agw-Js-Conv: str`, **no `chat_ability` at all**) | `710022004` |
| any well-formed 184-char garbage `a_bogus` | `710022002` `"目前服務使用人數較多，請稍後再試"` |

This is byte-for-byte the signature the `74295b9` commit message gives for the throttled window.
The corroboration that unsigned submits *do* normally work is now stronger than before: a shipped
third-party client posts to this endpoint with no `a_bogus` at all and no `msToken`
([OmniRoute `doubao-web.ts`](https://github.com/diegosouzapw/OmniRoute/blob/3c6f7177/open-sse/executors/doubao-web.ts)),
and the same file hard-codes the English text of `710022002` as a **busy/429** condition:

```ts
export function isDolaBusyMessage(content: string): boolean {
  return normalized.includes("a lot of people are using the app right now") && normalized.includes("try again later");
}
```

So: **not a shape problem, a session/IP risk-control state.** Nothing about chat or image can be
confirmed end-to-end while it holds, and no amount of code will change that.

### 2.4 The poll path needs no signature and costs no quota

MEASURED: `/im/chain/recent_conv` (cmd 3200) and `/im/chain/single` (cmd 3100) both answer `200`
through the relay with `status_code: 0` and real data. That is how §2.5 and §4.3 were obtained.

### 2.5 Image generation already happened on this account, and the response shape is fully known

MEASURED, poll of conversation `38416996481149201` (name `Dola`, 8 messages): a `block_type 2074`
block carrying a `type: 1` creation, i.e. an image. Its message `ext.chat_ability` is `"{}"` — **no
skill was selected**. So a plain-text conversation can end in a generated image; the port's own
comment in `wirePrompt` records the same observation from the other direction
("a request that says anything else comes back as a batch of images").

---

## 3. Feasibility, feature by feature

### 3.1 Text chat — feasible, one unobserved payload

Everything the submit needs is already in the port:

* same endpoint, method and query (`/chat/completion`), MEASURED live (§2.3 — I got an SSE stream back
  with `SSE_HEARTBEAT` / `STREAM_ERROR` / `SSE_REPLY_END` every time, so the framing is confirmed);
* no signature, no browser, no new dependency;
* the body differs from the video body in one field (`chat_ability`), and one public client omits it
  entirely;
* the reply is streamed on the same connection, so no polling and no media store are involved.

The gap is **the exact location of the text delta**, which I could not observe because the stream
never got past the gate. Two independent code bases agree on the shape (§4.2.4), and the web app's
own enum confirms the event names, so I rate this INFERRED-with-two-corroborating-implementations
rather than GUESS. It is the first thing a live test must check.

### 3.2 Image generation — feasible, and already 70% built

* The **response** side is MEASURED from a real generated image (§2.5, §4.3). No guessing at all.
* The **request** side has two candidate routes, and the port is already using the second:
  1. `chat_ability = {ability_type: 3, ability_param: "{\"ratio\":...,\"style\":...}"}` — the
     dedicated image skill. Keys MEASURED via the server's own pack (§4.3.1). The *values* are
     MEASURED too (`ratio` from the pack, `style` from the pack's 13 i18n keys).
  2. keep `ability_type: 17` and just phrase the prompt as a non-video request — what the port does
     today, and what the port's author observed working.
* The two known defects are in the response mapping, not the request: the watermarked URL (§4.3.2)
  and the video-shaped `validate()` (§7, step 2).

---

## 4. Exact protocol

### 4.1 The submit request, shared by all three features

MEASURED (the port's own `submitURL`/`buildSubmitBody`, reproduced by me against the live endpoint):

```
POST /chat/completion?<query>            via relay: POST https://vcrelay.dmean.dpdns.org/
                                         headers x-relay-target: https://www.dola.com
                                                 x-relay-path:   /chat/completion?<query>
Content-Type: application/json
agw-js-conv:  str, str
Accept:       */*
Cookie:       <account cookie>

query: aid=495671&channel=g&device_platform=web&language=zh-Hant&region=JP&sys_region=JP
       &samantha_web=1&use-olympus-account=1&version_code=20800&web_platform=browser
       &web_tab_id=<uuid>
       (no a_bogus — see §2.3)
```

`Accept: text/event-stream` and `Agw-Js-Conv: str` are what the public client sends
(CLIENT-SIDE); both forms reached the upstream fine in my tests, so this is a preference, not a
requirement I could prove.

Body skeleton (identical for chat, image and video; only `chat_ability` and the prompt text differ):

```json
{
  "client_meta": {"local_conversation_id":"local_<ms>","conversation_id":"",
                  "bot_id":"7339470689562525703","last_section_id":"","last_message_index":null},
  "messages": [{"local_message_id":"<uuid>",
    "content_block":[{"block_type":10000,
      "content":{"text_block":{"text":"<prompt>","icon_url":"","icon_url_dark":"","summary":""},
                 "pc_event_block":""},
      "block_id":"<uuid>","parent_id":"","meta_info":[],"append_fields":[]}],
    "message_status":0}],
  "option": {"create_time_ms":<ms>,"need_create_conversation":true,
             "conversation_init_option":{"need_ack_conversation":true},
             "unique_key":"<uuid>","need_deep_think":0,"is_user_chat_input":true,
             "sse_recv_event_options":{"support_chunk_delta":true}, ... },
  "chat_ability": <see below>,
  "user_context": [],
  "ext": {"fp":"<s_v_web_id>","use_deep_think":"0","sub_conv_firstmet_type":"1",
          "collection_id":"","conversation_init_option":"{\"need_ack_conversation\":true}",
          "commerce_credit_config_enable":"0"}
}
```

`ability_param` is a **JSON string inside the JSON**, not a nested object. MEASURED from the stored
conversation (§2.1) and from the web client, which does `JSON.stringify({ability_type, ability_param})`
with an already-stringified `ability_param` (`chunk_gn.26970ce1ab.js` @40809,
`createChatAbilityExt:(e,t)=>void 0!==e?JSON.stringify({ability_type:e,ability_param:t}):""`).

### 4.2 Text chat

#### 4.2.1 `chat_ability`

| source | value |
| --- | --- |
| OmniRoute `doubao-web.ts` (in production) | the field is **absent** |
| upstream `dola_client._build_chat_body` | `{"ability_type": 0}` (never executed — dead code) |
| web app default (`chat_b.js` @433533) | `{skill_id:"0", skill_type:0}`, i.e. FreeChat=0 |

**Recommendation:** omit `chat_ability` (mirrors the only client known to work), or send
`{"ability_type":0}`. Either way do **not** send `ability_type: 17`, which is what `chat.go` does
today by accident of reusing the video body.

#### 4.2.2 `option.need_deep_think`

* OmniRoute: `0`, or **`3`** for its `dola-pro` / `dola-deep-think` models.
* upstream Python: `1` for deep think, `0` otherwise.
* web app: a boolean-ish switch (`deepThinkingSwitchValue`) plus `use_auto_cot`.

**GUESS:** `1` and `3` are both plausible and I could not test which. Send `0`, and put the deep-think
value behind a config knob so it can be corrected without a rebuild (the port already uses that
pattern for `DOLA_VIDEO_MODELS`).

#### 4.2.3 Response framing — MEASURED

```
id: 0
event: SSE_HEARTBEAT
data: {}

id: 0
event: STREAM_ERROR
data: {"error_code":710022004,"error_msg":"rate limited","extra":{...}}

id: 1
event: SSE_REPLY_END
data: {"end_type":3}
```

Frames are separated by `\n\n`, each frame has an `id:` (sequence), an `event:` name and one `data:`
line. The event-name set, from the web app's own enum (`chat_b.js` @21228,
`chunk_tu2.0b01a1caef.js` @15813):

| name | enum value | meaning |
| --- | --- | --- |
| `SSE_HEARTBEAT` | 100001 | keep-alive, empty data |
| `SSE_ACK` | 100010 | carries `ack_client_meta.conversation_id` — the conversation is created |
| `STREAM_MSG_NOTIFY` | 100011 | first message of the reply |
| `STREAM_CHUNK` | 100012 | subsequent content |
| `FULL_MSG_NOTIFY` | 100013 | whole message, not a delta |
| `SSE_REPLY_END` | 100005 | end of reply; `{"end_type":3}` = `ReplyEndType_ReplyFinish` |
| `STREAM_ERROR` | 100006 | refusal |
| `CHUNK_DELTA` | 1000010 | delta form, requested by `sse_recv_event_options.support_chunk_delta` |
| `SSE_LOADING_STATE` | 100019 | — |
| `DOWNLINK_CMD` | 100 | out-of-band command |

`end_type: 3` matching `ReplyEndType_ReplyFinish=3` in the same enum is a nice cross-check that these
names are the real wire names and not an internal taxonomy.

#### 4.2.4 Where the reply text is — INFERRED (two corroborating implementations, not measured)

From OmniRoute `extractDolaTextDeltas`, which is the only client I know of that is in production
against this endpoint:

```
data            = JSON.parse(frame.data)
root            = data
payload         = data.data                      // some frames wrap everything in "data"
blocks          = root.content.content_block  ?? payload.content.content_block  ?? []
patchOps        = root.patch_op               ?? payload.patch_op               ?? []
for block in blocks:          emit textOf(block)
for op in patchOps:
    for block in op.patch_value.content_block: emit textOf(block)

textOf(block) = block.content.text_block.text          // "" if absent
boundary      = block.block_type == 10040 && block.is_finish == true
                // a finished thinking block: drop everything buffered before it
```

The upstream Python `_extract_text` agrees on the shape and adds `content.thinking_block.text` as a
second source, treating `STREAM_MSG_NOTIFY` as "first token" and `STREAM_CHUNK` as "subsequent delta".

Error text: `data.message` ?? `data.data.message` ?? `data.data.error_msg` (OmniRoute
`extractDolaError`). Note the port's `handleEvent` currently reads `error_code`/`error_msg` off the
frame root, which is what I actually saw in the live refusals, so both forms occur.

**Consequence for the design:** the extractor must be tolerant — accept `content.content_block` and
`data.content_block`, accept `patch_op` at either level, accept `text_block` and `thinking_block`,
and ignore unknown block types. A strict struct would silently yield an empty reply, which is the
worst possible failure mode here: it looks like a working feature that answers nothing.

### 4.3 Image generation

#### 4.3.1 The request — SERVER-CONFIG for the param keys, MEASURED for the method

`POST /samantha/skill/pack {"skill_type": 3}` → `code 0`:

```json
{"data": {"image": {"meta": {
  "option_list": [
    {"type": 3, "value": "ratio",
     "options": [{"value":"1:1"},{"value":"2:3"},{"value":"3:4"},{"value":"4:3"},
                 {"value":"9:16"},{"value":"16:9"}]},
    {"type": 3, "value": "style",
     "options": [{"value":"skill_image_styles_portrait"},{"value":"skill_image_styles_landscape"},
                 {"value":"skill_image_styles_japanese_anime"},{"value":"skill_image_styles_3d"},
                 {"value":"skill_image_styles_cyberpunk"},{"value":"skill_image_styles_oil_painting"},
                 {"value":"skill_image_styles_watercolor"},{"value":"skill_image_styles_flat_illustration"},
                 {"value":"skill_image_styles_children_illustration"},{"value":"skill_image_styles_pixel_style"},
                 {"value":"skill_image_styles_colored_pencil"},{"value":"skill_image_styles_ink_wash_painting"},
                 {"value":"skill_image_styles_ink_print"}]}],
  "render_mode": 0,
  "category_list": [{"category_id":1,"category_name":"精選"}, ... 8 entries]
}}}}
```

The method is self-validating: `{"skill_type": 17}` returns
`option_keys = ["model","ratio","duration"]` and `model_capability = ["ic_mini","seedance_v2.0"]`,
which is **exactly** the `ability_param` of the video body that is measured to work. So an
`option_list[].value` is an `ability_param` key. By the same rule:

```
image ability_param keys = ["ratio", "style"]
```

`style`'s values are i18n keys, not prompt fragments — note the parallel with
`input_config.image_generation.style[]` in `/samantha/skill/recommend`, whose 13 entries carry the
English fragments (`"Cyberpunk"`, `"3D anime style"`, …). **GUESS:** I believe the pack's option
`value` (the i18n key) is what goes on the wire, because that is how `ratio`/`model`/`duration`
work for video; but the client could equally resolve the key locally to the English fragment. Try
the key first, the fragment second. `style` is probably optional.

So the two candidate request bodies for image are:

```
A. dedicated image skill (preferred, unverified end to end)
   "chat_ability": {"ability_type": 3,
                    "ability_param": "{\"ratio\":\"1:1\",\"style\":\"skill_image_styles_cyberpunk\"}"}
   "messages[0]...text_block.text": "<the prompt, verbatim>"

B. what the port does today (observed by the port author, not by me)
   "chat_ability": {"ability_type": 17,
                    "ability_param": "{\"ratio\":\"1:1\",\"model\":\"seedance_v2.0\",\"duration\":10}"}
   "messages[0]...text_block.text": "<the prompt, verbatim, with no 'Generate video:' prefix>"
```

#### 4.3.2 The response — MEASURED (a real generated image, polled live)

```json
{"block_type": 2074,
 "content": {"creation_block": {"creations": [
   {"type": 1, "id": "872539315217",
    "image": {
      "key": "tos-mya-i-3rsxbfecgb/rc_gen_image/<hash>.jpeg",
      "status": 2, "tips": "",
      "image_thumb":   {"url":"...~tplv-0es2k971ck-downsize_watermark_1_5.png",        "width":384,  "height":682},
      "image_preview": {"url":"...~tplv-0es2k971ck-image_pre_watermark_1_5.png",       "width":1080, "height":1920},
      "image_ori":     {"url":"...~tplv-0es2k971ck-image_dld_watermark_1_5.png",       "width":1080, "height":1920},
      "image_ori_raw": {"url":"...~tplv-0es2k971ck-image_raw.png",                     "width":1080, "height":1920}
    }}]}}}
```

* `creation.type == 1` is the image (`CreationTypeImage=1`, `CreationTypeVideo=2`,
  `CreationTypeLocalImage=3`, `CreationTypeLocalVideo=4`, CLIENT-SIDE `chunk_ity.44b20a87ee.js` @2041).
* `image.status`: `1` Generating, `2` Succeed, `3` Banned, `4` Failed (`ImageStatus*`, same file).
  The measured value is `2`.
* **`image_ori_raw.url` is the unwatermarked original.** Its rendition is `~tplv-...-image_raw.png`,
  whereas `image_ori` is `~tplv-...-image_dld_watermark_1_5.png`. Independently confirmed by
  [`ihmily/doubao-nomark`](https://github.com/ihmily/doubao-nomark/blob/main/doubao_parser/image.py),
  a project whose entire purpose is watermark removal and which explicitly supports `dola.com`: it
  reads `creation["image"]["image_ori_raw"]["url"]` and nothing else.
* There is **no `image_raw` key** in the measured response. `dola_client._poll_image` tries
  `image_raw` first and falls back to `image_ori`, so on this platform it silently returns the
  watermarked URL. That is the bug to fix in the port's `mediaOf`.
* While generating, the assistant's message carries `block_type 10010` (`BLOCK_GEN_IMAGE`); the
  finished picture arrives as `2074`. `block_type 10012` is `BLOCK_IMAGE`, `10052` is
  `BLOCK_ATTACHMENT` (the user's uploaded reference image), `10040` is the thinking block.

---

## 5. Added overhead on the router

Current state: binary **8,257,727 B** (gate **9 MB** → **742,273 B** headroom), steady RSS
**7.9 MB** (anon ~1.2 MB), device 250 MB total / ~55 MB available, flash ~44 MB free.
Deployed `deploy/dola2api.conf`: `DOLA_MAX_CONCURRENCY=1`, `DOLA_MAX_PENDING_TASKS=4`,
`DOLA_VIDEO_TIMEOUT=600`, `DOLA_POLL_INTERVAL=10`, `DOLA_HEAP_SOFT_LIMIT=33554432`.

| budget | chat | image |
| --- | --- | --- |
| **new dependency** | **none.** `bufio`, `encoding/json`, `net/http`, `time` are all already linked. The `go.mod` has no `require` and stays that way. | none |
| **binary size** | estimate **+12–30 KB** (0.15–0.4%). The marginal cost is `encoding/json`'s per-type metadata for the new structs plus one streaming reader; the encoder/decoder itself is already in the image. Comfortably inside the 742 KB headroom. **Estimate, not a measurement** — verify by building before/after; CI already asserts ≤9 MB. | **+1–2 KB** (one struct field `image_ori_raw`, one constant) |
| **steady RSS** | **+0** if chat adds no background goroutine, which it need not | **+0** |
| **per in-flight request** | ≤ ~100 KB: a 32 KiB stream buffer plus a bounded accumulation of the reply (recommend capping the reply at 64 KiB and failing loudly past it). The guard's `DOLA_HEAP_SOFT_LIMIT` still applies. | **+0** — the existing poll path already parses this |
| **goroutines** | **+0 steady state, +0 per request.** The handler goroutine already exists and does the reading; a streamed reply is written from that same goroutine. | +0 |
| **CPU** | one `json.Unmarshal` per SSE frame, same order as one poll response. The dominant cost on this device remains the 10 s poll tick, not parsing. | +0 |
| **flash** | +0 new files; `/opt/dola2api` changes only in the binary. Two new env vars are optional. | +0 |
| **disk** | +0. Chat has no media to mirror; image media already flows through `DOLA_STORE_MODE=redirect`, which sends the client a 302 and moves no bytes through the router. | +0 |

**The one real cost is not memory — it is serialisation.** With `DOLA_MAX_CONCURRENCY=1` and
`DOLA_VIDEO_TIMEOUT=600`, a chat request routed through `Pool` would sit behind a running video for
up to ten minutes, and `DOLA_MAX_PENDING_TASKS=4` means two chat requests can occupy half the pending
queue. **Recommendation: do not put chat through the Pool.** Chat needs no task record, no polling
and no media store — it is a synchronous one-shot that reads one response to the end. Give it its own
small semaphore (`DOLA_CHAT_CONCURRENCY`, default 2) and its own deadline (`DOLA_CHAT_TIMEOUT`,
default 60 s), and keep the video pool exactly as it is. That also keeps the "one upstream submission
at a time" property that `pool.go`'s comment gives as the reason for its bound.

---

## 6. Risks and unknowns

Ordered by how much they would hurt.

1. **The gate (§2.3) — this is the dominant risk and it is shared with video.** Until unsigned
   submits are accepted again, chat and image are as dead as video already is, and every hour spent
   on them is unverifiable. Anything that fixes or routes around the gate is worth more than either
   feature.
2. **The chat text-delta payload is inferred, not measured (§4.2.4).** If it is wrong, the symptom is
   an empty assistant reply with a valid `conversation_id` — indistinguishable from success. Mitigate
   by logging the first N frames verbatim on the first live run, and by making the extractor tolerant
   rather than strict.
3. **A wrong `ability_type` looks exactly like a working feature** (the brief's own warning). For
   image the *response* side is measured, so a wrong ability shows up as "accepted, but no `2074`
   block ever appears" — a poll timeout. Mitigate with a bounded poll and an explicit
   "accepted but produced nothing" error rather than a bare timeout.
4. **`need_deep_think` is a GUESS** (§4.2.2). Cheap to make configurable; do that rather than guess
   harder.
5. **The image `style` value form is a GUESS** (§4.3.1): i18n key vs English fragment. Two attempts
   is the whole cost.
6. **Chat may consume the same daily allowance as generation.** The account has a measured 2-video/day
   cap; whether text chat draws on the same pool is **unknown**. If it does, adding chat could starve
   video. Watch the first few chat replies for a quota message (the port already has
   `CREDIT_FAIL_PATTERN`-style detection in the poll path) and, if it is shared, put chat behind its
   own toggle.
7. **Abuse surface.** A text-chat proxy on a home router is a much more attractive free-LLM endpoint
   than a video API. Auth already exists and fails closed, but a per-key rate limit is not there and
   would become worth having.
8. **`image_ori` vs `image_ori_raw`.** The port currently serves the watermarked image. Not a
   correctness risk for the pipeline, but it silently gives the operator a worse asset than the
   upstream produces.
9. **Fragility of the `style`/`ratio` lists.** They come from a config endpoint that the upstream can
   change without notice. The port already treats `DOLA_VIDEO_MODELS` as operator-correctable for
   exactly this reason; do the same for image ratios/styles.
10. **The stale `README.md`.** It does not document `/v1/images` or `/v1/chat/completions`, and its
    status section still says the only unpassed thing is the signature gate. That was written before
    `/v1/images` existed.

---

## 7. Recommended implementation order

Sizes are rough; "code" excludes tests.

**Step 0 — verify the gate (0 code, do this first).**
Re-run `python spikes/confirm_gate.py cookies.txt` and
`python spikes/chat_probe.py cookies.txt --ability 0 --prompt hi --seconds 20`. Success criterion:
the `none` variant returns `SSE_ACK` with a `conversation_id`. If it is still `710022004`, stop here —
steps 1–5 cannot be validated and step 0's finding is the deliverable.

**Step 1 — image correctness (`dola.go`, ~20 lines).**
Add `image_ori_raw` to the `creation.Image` struct and prefer it in `mediaOf`, with `image_ori` →
`image_preview` → `image_thumb` as the existing fallbacks. Keep the watermarked URL in
`Media.WatermarkedURL` so nothing is lost. Update `TestExtractPollFindsImages` to assert that
`image_ori_raw` wins when present. This is the cheapest real improvement in the whole plan and it is
justified by a measured field name plus an independent project.

**Step 2 — image request and validation (`api.go`, `models.go`, `dola.go`, ~60–100 lines).**
Decide the ability type: make it a config value (`DOLA_IMAGE_ABILITY`, default 3) rather than a
constant, so the two candidates can be tried without a rebuild; when `Kind == "image"` build
`ability_param` from `{ratio, style}` instead of `{ratio, model, duration}`. Give `validate` an
image branch: the image ratio set is the six measured values, `duration` is irrelevant and should be
ignored rather than rejected, and `model` should not be validated against the video whitelist. Add
`POST /v1/images/generations` as an alias if an OpenAI-shaped client is wanted. Tests: the body
builder for both kinds, the ratio whitelist, and that an image request never carries `duration`.

**Step 3 — the chat upstream call (`dola.go` + a new `chatstream.go`, ~200–280 lines).**
`Upstream.Chat(ctx, cookie, messages, opts)` that builds the body with `chat_ability` omitted
(or `ability_type: 0`), POSTs through the existing egress layer, and reads the SSE stream to the end
with a bounded buffer and a deadline, returning the accumulated text plus the conversation id. The
frame reader can reuse `readAck`'s line-splitting idea but must be a streaming reader, not a
first-match scanner. Keep the extractor permissive (§4.2.4) and unit-test it against synthetic
frames covering: `content.content_block`, `data.content_block`, `patch_op`, `data.patch_op`,
`thinking_block`, a `10040` boundary, `FULL_MSG_NOTIFY`, and an unknown block type. Also add a
`--frames` style debug log of the first frames at notice level so the first live run is diagnosable.

**Step 4 — the chat HTTP surface (`chat.go`, ~120–180 lines).**
Replace the current "map a chat request onto a video task" handler with a real one: read the
OpenAI-shaped body, keep only the last user turn plus (optionally) a folded transcript, call
`Upstream.Chat`, and return an OpenAI-shaped completion. Do **not** route it through `Pool` (§5);
give it its own semaphore and deadline. Keep `stream: true` returning a clear error for now, or
implement it in step 5. Delete `describeTask`/`awaitTask`'s use from this path; the video adapter
can keep them if a separate endpoint still wants the "video as a chat answer" behaviour.

**Step 5 — streaming passthrough (`chat.go`, ~60–100 lines).**
If wanted: write `data: {"choices":[{"delta":{"content":...}}]}\n\n` per delta with `http.Flusher`,
then `data: [DONE]`. Costs one write per delta and no goroutine. Note the relay's 25 s
first-byte budget does not apply once the stream has started, but the router's own client timeout
does — keep `DOLA_CHAT_TIMEOUT` in front of it.

**Step 6 — config, docs, and the evidence trail (~40 lines + prose).**
Two env vars (`DOLA_CHAT_CONCURRENCY`, `DOLA_CHAT_TIMEOUT`), `DOLA_IMAGE_ABILITY`, and the image
ratio/style lists; entries in `deploy/dola2api.conf` with the reason each value is what it is.
Update `README.md`'s endpoint table (it is missing `/v1/images` and `/v1/chat/completions`) and
append the measured image/chat findings to `spikes/RESULT.md`, including the §2.3 gate table so the
next reader does not re-derive it.

**Total: roughly 460–700 lines of Go plus roughly 300–450 lines of tests, no new dependency, no new
background goroutine.**

Tests worth having beyond the per-step ones: a golden test that the chat body contains no
`ability_type: 17`; a test that an image submit's `ability_param` has no `duration` key; a test that
the chat extractor returns "" rather than panicking on a truncated frame (the relay can cut a stream
mid-frame — `maxPollBody` and `maxSubmitBody` already exist for that reason).

---

## 8. What would make a feature not worth doing

* **If the gate stays closed** (step 0 fails and keeps failing), neither feature is reachable. The
  honest conclusion then is that the port has one blocker, not three features, and effort belongs on
  the gate — not on chat or image.
* **If chat draws on the same 2/day allowance as video**, chat is worth having only as an explicitly
  separate toggle; otherwise it eats the resource the service exists for.
* **If the reply text turns out to arrive only as `FULL_MSG_NOTIFY` snapshots** (no deltas), chat is
  still feasible but loses streaming, which halves its value; the cost estimate does not change.
* **Image is worth doing regardless of the gate**, because step 1 (the watermarked URL) is a
  correctness fix to code that already exists and is justified by a measured field name.

---

## 9. Artefacts produced by this study

New under `spikes/`: `chat_probe.py`, `omniroute_probe.py`, `poll_dump.py`, `skill_probe.py`,
`skill_pack_probe.py`, `skill_pack_all.py`, `_pack_dump.py`, plus their recorded results
`chat_probe_result.json`, `omniroute_result.json`, `poll_dump.json`, `poll_dump_image.json`,
`skill_probe.json`, `skill_pack.json`, `skill_pack_all.json` and the `.txt` transcripts.
All of them are read-only except the `chat_probe`/`omniroute_probe` submits noted in §2.3.
