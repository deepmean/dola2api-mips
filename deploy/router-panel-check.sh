#!/bin/sh
# Verify the panel and the account endpoints on the device.

BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf
API_KEY=$(sed -n 's/^DOLA_API_KEYS="\(.*\)"/\1/p' "$CONF")
ADMIN_KEY=$(sed -n 's/^DOLA_ADMIN_KEY="\(.*\)"/\1/p' "$CONF")

echo "=== 1. panel page (no key needed to load the shell) ==="
curl -s -m 10 -o /tmp/panel.html -w '  status=%{http_code} bytes=%{size_download} type=%{content_type}\n' "$BASE/admin"
echo -n "  has the add form: "; grep -c 'id="add"' /tmp/panel.html
echo -n "  has the accounts table: "; grep -c 'id="accounts"' /tmp/panel.html
echo -n "  mentions the key prompt: "; grep -c 'localStorage' /tmp/panel.html

echo
echo "=== 2. /v1/models (the endpoint that was a 404) ==="
curl -s -m 10 "$BASE/v1/models" -H "Authorization: Bearer $API_KEY"
echo

echo
echo "=== 3. add an account with a cookie that cannot work ==="
curl -s -m 60 -X POST "$BASE/admin/accounts" \
	-H "Authorization: Bearer $ADMIN_KEY" -H 'Content-Type: application/json' \
	-d '{"label":"bogus","cookie":"sessionid=deadbeefdeadbeefdeadbeefdeadbeef"}'
echo
echo -n "  sessions after the refusal: "
curl -s -m 10 "$BASE/health" | sed 's/.*"accounts":\[//; s/\].*//' | grep -o '"label"' | wc -l

echo
echo "=== 4. re-add the existing session under a new label (verifies then upserts) ==="
COOKIE=$(sed -n 's/.*"cookie": "\(.*\)",*/\1/p' /opt/dola2api/accounts.json | head -1)
if [ -n "$COOKIE" ]; then
	curl -s -m 60 -X POST "$BASE/admin/accounts" \
		-H "Authorization: Bearer $ADMIN_KEY" -H 'Content-Type: application/json' \
		-d "{\"label\":\"renamed\",\"cookie\":\"$COOKIE\"}"
	echo
	echo -n "  sessions now: "
	curl -s -m 10 "$BASE/health" | grep -o '"label":"[^"]*"' | sed 's/"label":"//; s/"//' | tr '\n' ' '
	echo
else
	echo "  could not read a cookie from accounts.json"
fi

echo
echo "=== 5. remove the extra session ==="
curl -s -m 10 -X DELETE "$BASE/admin/accounts/renamed" -H "Authorization: Bearer $ADMIN_KEY"
echo
echo -n "  sessions now: "
curl -s -m 10 "$BASE/health" | grep -o '"label":"[^"]*"' | sed 's/"label":"//; s/"//' | tr '\n' ' '
echo

echo
echo "=== 6. footprint ==="
PID=$(pgrep -f '/opt/dola2api/dola2api' | head -1)
awk '/^(VmRSS|RssAnon|Threads)/ {print "  " $0}' /proc/$PID/status
rm -f /tmp/panel.html
