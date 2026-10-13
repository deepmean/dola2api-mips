# dola2api (MIPS32 port)

Dola Render Gateway 鐨?MIPS32 绉绘鐗堬細鎶?`coll3879xx-cyber/dola-render-gateway`
锛圥ython 3.11 + FastAPI + Playwright/Chromium锛夋敼鍐欐垚 **Go 绾爣鍑嗗簱** 鐨勫崟鏂囦欢闈欐€佷簩杩涘埗锛?璺戝湪 MIPS32 灏忕 OpenWrt 璺敱鍣ㄤ笂锛圡ediaTek MT7621 / `ramips`锛屽疄娴嬫満鍨?JCG-Q20锛夈€?
涓婃父鐢?Playwright 椹卞姩鐪熷疄娴忚鍣ㄥ仛鎻愪氦銆佹粦鍧楅獙璇佺爜姹傝В涓?30s 鎵╁睍鎷︽埅鈥斺€旇繖浜涘湪
250MB RAM銆佹棤 FPU 鐨?MT7621 涓婁笉鍙兘杩愯銆傛湰椤圭洰鍙繚鐣?*鍗忚璺緞**锛?
| 淇濈暀 | 渚濇嵁 |
| --- | --- |
| `POST /chat/completion`锛圫SE 鎻愪氦锛?| `dola_client.py:716-863` 鏄函 aiohttp 瀹炵幇锛屼笉鍚祻瑙堝櫒 |
| `POST /im/chain/single`锛堣疆璇級 | 鍚屼笂 |
| 鍙傝€冨浘涓婁紶锛坄prepare_upload` 鈫?`ApplyImageUpload` AWS4 鈫?TOS PUT锛?| `dola_client.py:72-106, 386-462` 绾?`hmac`/`hashlib` |
| 鏈按鍗?URL 鎻愬彇锛坄video_model.video_list[].main_url` base64锛?| `video_worker.py:91-114` 鍙槸瑙ｇ爜 + 姣旂爜鐜?|

涓㈠純锛歅laywright/Chromium銆乣browser_pool.py` 鐨?profile 绠＄悊銆乣gap.py` 鐨?OpenCV 婊戝潡姹傝В銆?`extensions/dola30` 鎵╁睍銆乣add_account.py` 娴忚鍣ㄧ櫥褰曪紙鏀逛负 cookie 娉ㄥ叆锛夈€?
## 鎺ュ彛

浠ヤ笅涓?*褰撳墠瀹炵幇**锛堜笉鏄鍒掞級銆俙/v1/*` 闇€ `DOLA_API_KEYS`锛宍/admin/*` 闇€ `DOLA_ADMIN_KEY`锛?`/health` 鏃犻渶閴存潈锛堢洃鎺ц鐢紝涓斾笉鍚换浣曞瘑閽ワ級銆?
| 鏂规硶 | 璺緞 | 璇存槑 |
| --- | --- | --- |
| POST | `/v1/videos` | `{prompt,ratio?,model?,duration?}` 鈫?`202 {id,state,media[]}` |
| POST | `/v1/images` | 鍚屼笂锛沗{prompt,ratio?,style?}`锛屼骇鍑哄浘鐗囪€岄潪瑙嗛 |
| POST | `/v1/chat/completions` | 鏂囨湰瀵硅瘽锛圤penAI 褰㈢姸锛夈€?*涓嶈蛋浠诲姟闃熷垪**锛岃嚜甯﹀苟鍙戜笌瓒呮椂 |
| GET | `/v1/models` | 鏈湇鍔℃敮鎸佺殑妯″瀷锛坄DOLA_VIDEO_MODELS` 鍙敼锛?|
| GET | `/v1/videos/{id}` | 浠诲姟鐘舵€侊紱`state 鈭?pending/running/succeeded/failed` |
| GET | `/v1/videos/{id}/content` | 鎸?`DOLA_STORE_MODE` 浜や粯锛堥粯璁?302 鍒?CDN锛?|
| GET | `/admin` | 闈㈡澘锛堥〉闈㈠３鍏嶉壌鏉冿紝瀵嗛挜鐢辨祻瑙堝櫒鏈湴淇濆瓨锛?|
| GET | `/admin/status` | 鍑哄彛銆佸唴瀛樸€佽处鍙凤紙鍚埌鏈熷€掕鏃讹級銆侀槦鍒楁繁搴?|
| GET | `/admin/tasks?limit=N` | 鏈€杩戜换鍔★紙1鈥?00锛岄粯璁?50锛?|
| POST | `/admin/reload` | 閲嶆柊璇诲彇 `accounts.json`锛堢瓑浠蜂簬 SIGHUP锛?|
| POST | `/admin/accounts` | **鍏堝悜涓婃父楠岃瘉**鍐嶄繚瀛樹竴涓?cookie |
| DELETE | `/admin/accounts/{label}` | 鍒犻櫎涓€涓处鍙?|
| GET | `/health` | 璐﹀彿姹犮€佸嚭鍙ｇ姸鎬併€佸唴瀛樿鏁般€佺儹閲嶈浇閿欒 |

**瑙嗛涓庡浘鐗囩殑宸埆鍙湪鎻愮ず璇嶇殑鎺緸**锛堣棰戣蛋 `Generate video: <prompt>, <ratio>`锛屽浘鐗囪蛋瑁告彁绀鸿瘝锛変笌
鎶€鑳界被鍨嬶紙`DOLA_IMAGE_ABILITY`锛岄粯璁?3锛夈€傚悓涓€娆′笂娓歌皟鐢ㄣ€佸悓涓€鏉¤疆璇€佸悓涓€濂楀彇鍥炪€?
**鏈尮閰嶇殑璺緞**浼氳繑鍥炵粨鏋勫寲 404 骞?*鎶婅姹傜殑璺緞鍐欒繘鏃ュ織**鈥斺€斿鎴风瀵规帴鏃剁殑璺緞涓嶅尮閰嶆浘鏄竴鏉?娌℃湁绾跨储鐨?`404 page not found`銆?
閴存潈**澶辫触鍏抽棴**锛氭湭閰嶇疆 key 鏃?`/v1/*` 杩斿洖 `503` 鑰屼笉鏄鎵€鏈変汉寮€鏀撅紱
key 鐢ㄥ父閲忔椂闂存瘮杈冿紝閿欑殑 key 涓庣己澶辩殑 key 涓嶅彲鍖哄垎銆傛櫘閫?API key **涓嶈兘**璁块棶 `/admin/*`
鈥斺€旇兘閲嶈浇璐﹀彿鐨勪汉鍙互鎶婃湇鍔℃寚鍚戝畬鍏ㄤ笉鍚岀殑浼氳瘽銆?
浠诲姟鍒楄〃鎸?*鍗曡皟搴忓彿**鎺掑簭锛屼笉鎸夋椂闂存埑锛氬悓涓€鏃堕挓鍒诲害鍐呭垱寤虹殑浠诲姟鐢ㄦ椂闂存埑鎺掑簭涓嶇‘瀹氥€?
**闈㈡澘锛坄/web`锛夊皻鏈疄鐜般€?* 鎿嶄綔闈㈢洰鍓嶆槸 `/admin/*` 鐨?JSON 鎺ュ彛銆?
## 鍑哄彛浠ｇ悊灞?
鎵€鏈変笂娓歌姹傜粡缁熶竴鐨?egress 灞傚嚭缃戯紝澶辫触鏃?*榛樿涓嶅洖钀界洿杩?*锛坄DOLA_STRICT_PROXY=1`锛夛紝
閬垮厤璺敱鍣ㄧ湡瀹?IP 娉勬紡缁欎笂娓搞€傛敮鎸佷笁绉嶈矾寰勶細

- `vercel`锛歏ercel relay锛屽熀鍧€閲嶅啓 + `x-relay-target` / `x-relay-path` 澶达紙涓?9router 鍚屽绾︼級
- `http`锛氭爣鍑?HTTP CONNECT 浠ｇ悊
- `direct`锛氱洿杩烇紙浠呮樉寮忛厤缃垨鍛戒腑 `noProxy` 鏃讹級

## 鏋勫缓

**浜岃繘鍒剁敱 GitHub Actions 浜戠紪璇戜骇鍑猴紝鏈湴鏃犻渶瀹夎 Go銆?*

```sh
wget -O /tmp/dola2api.new \
  https://github.com/deepmean/dola2api-mips/releases/download/latest/dola2api-linux-mipsle
chmod +x /tmp/dola2api.new
```

鍥哄畾鏋勫缓鍙傛暟锛歚GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0`锛?`-trimpath -ldflags "-s -w"`銆俙GOMIPS=softfloat` 鏄?*蹇呴』鐨?*锛歁T7621 娌℃湁鍙敤 FPU锛?纭诞鐐逛簩杩涘埗鑳藉惎鍔ㄣ€侀娆＄敤娴偣鍗冲穿銆侰I 鍦ㄤ笂浼犲墠鐢?`readelf` 鏂█
`ELF32` / 灏忕 / `Machine: MIPS` / `FP ABI: Soft float` / 鏃?`PT_INTERP`銆?
CI 杩樺甫涓夋潯闂ㄧ锛?
1. `go.mod` 鏃?`require`锛堜繚鎸佺函鏍囧噯搴撱€佸彲澶嶇幇鏋勫缓锛?2. **浜х墿鏈 UPX 鍘嬬缉**锛氭柇瑷€浜岃繘鍒堕噷娌℃湁 UPX 榄旀暟銆佷笖 ELF 鑺傚ご琛ㄥ畬濂?   锛圲PX 浼氬墺鎺夎妭澶磋〃锛夈€傜敤鍐呭瓨鎹?flash 鍦ㄨ繖鍙拌澶囦笂鏄弽鐨勨€斺€攆lash 杩樺墿 44MB 涓嶇己锛?   缂虹殑鏄?RAM锛岃€?UPX 鍦?`exec` 鏃舵妸鏁翠唤闀滃儚瑙ｅ帇杩涘唴瀛樸€傛柇瑷€鍐欏湪**浜х墿**涓婅€屼笉鏄?   鎵粨搴撻噷鐨勫瓧鐪硷紝鍚﹀垯杩炶В閲婅繖鏉＄浠ょ殑鏂囨。閮戒細琚垽杩濊銆?3. 闄?`egress*.go` 澶栫姝㈠嚭鐜?`http.Transport{` 涓?`net.Dial(`锛堥槻姝㈢粫杩囧嚭鍙ｅ眰锛?   娴嬭瘯鏂囦欢闄ゅ锛屽畠浠細杩炴湰鍦?httptest 鐩戝惉锛?
## 璧勬簮棰勭畻

鍐呭瓨鏄富棰勭畻锛宖lash 鍙槸璁板綍椤癸細

| 鎸囨爣 | 鐩爣 | **鐪熸満瀹炴祴** |
| --- | --- | --- |
| 绋虫€?RSS锛堢┖闂诧級 | 鈮?12MB | **7.9MB**锛坅non 1.2MB锛屽叾浣欎负鍙洖鏀剁殑浠ｇ爜椤碉級 |
| 宄板€?RSS锛? 涓湪椋炰换鍔?+ 4MB 鍙傝€冨浘锛?| 鈮?32MB | 鎻愪氦瀹炴祴 **9.6MB** |
| 浜岃繘鍒朵綋绉紙闃茶啫鑳€闂ㄧ锛?| 鈮?9MB | 7.88MB |
| 绋虫€?`MemAvailable` 涓嬮檷 | 鈮?15MB | 璁惧 `available` 浠?63.5MB 鈫?55.5MB锛堝惈鍏朵粬鏈嶅姟娉㈠姩锛?|

OOM 鑷繚锛歚DOLA_HEAP_SOFT_LIMIT`锛堥粯璁?32MiB锛夊湪澶у垎閰嶅墠妫€鏌?`HeapAlloc`锛?瓒呴檺璁?*褰撳墠浠诲姟**浠?`507` 澶辫触锛岃€屼笉鏄鍐呮牳 OOM killer 鍘绘潃鍒殑杩涚▼銆?姣忎釜浠诲姟缁撴潫璋冧竴娆?`debug.FreeOSMemory()` 鎶婂爢杩樼粰鍐呮牳銆?
## 鐪熸満楠屾敹

鍦ㄧ洰鏍囪澶囷紙JCG-Q20 / OpenWrt 24.10.5 / ramips-mt7621 / mipsel_24kc锛変笂瀹炴祴閫氳繃锛?
| 椤?| 缁撴灉 |
| --- | --- |
| 浜岃繘鍒跺湪璁惧涓婃墽琛?| 鉁?softfloat 鏋勫缓姝ｅ父鍚姩銆佹甯告姤閿欍€佹甯告湇鍔?|
| procd 鏈嶅姟 | 鉁?`enabled`锛岀洃鍚?`8790`锛屼紭闆呭仠鏈?|
| `/health` 鍏嶉壌鏉?| 鉁?|
| 鏃?key / 閿欒 key 璁块棶 `/v1/*` | 鉁?401 |
| **鏅€?API key 璁块棶 `/admin/*`** | 鉁?**401**锛堣璁＄殑瀹夊叏灞炴€э級 |
| admin key 璁块棶 `/admin/*` | 鉁?200 鍏ㄩ噺鐘舵€?|
| 璇锋眰鏍￠獙锛堢┖ prompt锛?| 鉁?400 + 鏄庣‘鍘熷洜 |
| **鐪熷疄鎻愪氦鍏ㄩ摼璺?* | 鉁?202 鍙楃悊 鈫?浠诲姟缁撶畻锛堥壌鏉冣啋鏍￠獙鈫掓睜鈫?*鍑哄彛灞?relay**鈫掍笂娓革級 |
| 璐﹀彿鍐峰嵈 | 鉁?鎻愪氦澶辫触鍚?`available:false`銆乣cooldown_remaining:737` |

**鍞竴鏈€氱殑鏄笂娓哥鍚嶉棬妲?*锛氭彁浜よ涓婃父浠?`710022002`锛堟湇鍔￠檺娴侊級鎷掔粷鈥斺€?璇ラ敊璇?*娴忚鍣ㄨ嚜宸变篃浼氶亣鍒?*锛岃瑙?[`spikes/RESULT.md`](spikes/RESULT.md)銆?
閮ㄧ讲涓庨獙鏀惰剼鏈湪 [`deploy/`](deploy/)锛歚router-setup.sh`锛堝氨鍦扮敓鎴愬瘑閽ワ級銆?`router-update.sh`锛堝師瀛愭浛鎹?+ 鑷姩鍥炴粴锛夈€乣router-footprint.sh`銆乣router-acceptance.sh`銆?
## 閮ㄧ讲

鐩爣甯冨眬锛歚/opt/dola2api/`锛堜簩杩涘埗 + `dola2api.conf` + `accounts.json`锛?
`/etc/init.d/dola2api`锛坧rocd锛夛紝绔彛 `8790`銆?
```sh
# 浜岃繘鍒朵粠 CI 浜х墿鍙?wget -O /tmp/dola2api https://github.com/deepmean/dola2api-mips/releases/download/latest/dola2api-linux-mipsle
ssh root@192.168.26.1 'mkdir -p /opt/dola2api'
scp /tmp/dola2api                root@192.168.26.1:/opt/dola2api/dola2api
scp deploy/dola2api.conf         root@192.168.26.1:/opt/dola2api/dola2api.conf
scp accounts.json                root@192.168.26.1:/opt/dola2api/accounts.json
scp deploy/dola2api.init         root@192.168.26.1:/etc/init.d/dola2api

ssh root@192.168.26.1 '
  chmod 700 /opt/dola2api/dola2api.conf        # 鍐呭惈 API key
  chmod 600 /opt/dola2api/accounts.json        # 鍐呭惈浼氳瘽 cookie
  chmod +x /opt/dola2api/dola2api /etc/init.d/dola2api
  /etc/init.d/dola2api enable
  /etc/init.d/dola2api start
  sleep 2 && logread -e dola2api | tail -5
'
```

`accounts.json` 鏍煎紡瑙?[`accounts.json.example`](accounts.json.example)锛?`deploy/dola2api.conf` 姣忛」閮芥湁娉ㄩ噴璇存槑涓轰粈涔堝彇杩欎釜鍊笺€?
鏀瑰畬璐﹀彿**涓嶅繀閲嶅惎**锛?
```sh
ssh root@192.168.26.1 '/etc/init.d/dola2api reload'      # 绛変环浜?SIGHUP
# 鎴栵紙缃戠粶渚э紝鏃犻渶 ssh锛?curl -X POST -H "Authorization: Bearer $DOLA_ADMIN_KEY" http://192.168.26.1:8790/admin/reload
```

鐑噸杞?*淇濈暀 cookie 鏈彉鐨勮处鍙风殑褰撴棩鐢ㄩ噺**鈥斺€旀敼涓?label 鎴栧姞涓€涓处鍙凤紝涓嶄細缁欏凡鑰楀敖鐨勮处鍙峰彂鏂伴搴︺€?鏂囦欢鍐欏潖鏃?*杩愯涓殑闆嗗悎涓嶅彉**锛屽彧璁板綍閿欒锛坄/health` 涓?`/admin/status` 閲屽彲瑙侊級銆?
**娉ㄦ剰**锛歚.gitattributes` 寮哄埗 `deploy/` 涓嬫枃浠朵负 LF銆侰RLF 鐨?procd 鑴氭湰鍦ㄨ澶囦笂浼氫互
`$'\r': not found` 澶辫触銆?
## 璁稿彲

涓庝笂娓镐竴鑷达細浠呬緵瀛︿範涓庡唴閮ㄦ祴璇曘€?