#!/bin/sh
# Verify the full API surface after a deploy.
#
# Generation itself is behind the upstream's risk gate, which is intermittent, so
# this checks what can be checked regardless: the routes exist, auth behaves, the
# config loaded, and the panel is served.

BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf
API_KEY=$(sed -n 's/^DOLA_API_KEYS="\(.*\)"/\1/p' "$CONF")
ADMIN_KEY=$(sed -n 's/^DOLA_ADMIN_KEY="\(.*\)"/\1/p' "$CONF")

check() {
	# check <label> <expected-code> <curl args...>
	label=$1; want=$2; shift 2
	got=$(curl -s -m 20 -o /tmp/body.txt -w '%{http_code}' "$@")
	if [ "$got" = "$want" ]; then
		printf '  OK    %-42s %s\n' "$label" "$got"
	else
		printf '  FAIL  %-42s got %s want %s\n' "$label" "$got" "$want"
		head -c 160 /tmp/body.txt
		echo
	fi
}

echo "=== routes exist and authenticate ==="
check "GET  /v1/models  (with key)" 200 \
	"$BASE/v1/models" -H "Authorization: Bearer $API_KEY"
check "GET  /v1/models  (no key)" 401 "$BASE/v1/models"
check "POST /v1/videos  (no key)" 401 -X POST "$BASE/v1/videos" \
	-H 'Content-Type: application/json' -d '{"prompt":"x"}'
check "POST /v1/images  (validation)" 400 -X POST "$BASE/v1/images" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' -d '{"prompt":""}'
check "POST /v1/chat/completions (validation)" 400 -X POST "$BASE/v1/chat/completions" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' -d '{}'
check "POST /v1/chat/completions (stream rejected)" 501 -X POST "$BASE/v1/chat/completions" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
	-d '{"stream":true,"messages":[{"role":"user","content":"hi"}]}'
check "POST /v1/videos/generations (alias)" 400 -X POST "$BASE/v1/videos/generations" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' -d '{}'
check "GET  /admin (panel)" 200 "$BASE/admin"
check "GET  /admin/status (api key refused)" 401 "$BASE/admin/status" \
	-H "Authorization: Bearer $API_KEY"
check "GET  /admin/status (admin key)" 200 "$BASE/admin/status" \
	-H "Authorization: Bearer $ADMIN_KEY"
check "GET  /v1/nonexistent (structured 404)" 404 "$BASE/v1/nonexistent" \
	-H "Authorization: Bearer $API_KEY"

echo
echo "=== the 404 names what was asked for ==="
curl -s -m 10 "$BASE/v1/nonexistent" -H "Authorization: Bearer $API_KEY"
echo

echo
echo "=== config in force ==="
curl -s -m 10 "$BASE/admin/status" -H "Authorization: Bearer $ADMIN_KEY" |
	sed 's/,"accounts".*//' | head -c 400
echo

echo
echo "=== footprint ==="
PID=$(pgrep -f '/opt/dola2api/dola2api' | head -1)
awk '/^(VmRSS|RssAnon|Threads)/ {print "  " $0}' /proc/$PID/status
rm -f /tmp/body.txt
