#!/bin/sh
# End-to-end acceptance test, run on the device.
#
# Reads the keys from the installed config in the same shell, so they are never
# printed here or anywhere else.

BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf

API_KEY=$(sed -n 's/^DOLA_API_KEYS="\(.*\)"/\1/p' "$CONF")
ADMIN_KEY=$(sed -n 's/^DOLA_ADMIN_KEY="\(.*\)"/\1/p' "$CONF")

if [ -z "$API_KEY" ] || [ -z "$ADMIN_KEY" ]; then
	echo "FAIL: could not read the keys from $CONF"
	exit 1
fi
echo "keys read from the config (values not shown)"

echo
echo "=== 1. /health needs no key ==="
curl -s -m 10 "$BASE/health" | head -c 200
echo

echo
echo "=== 2. /v1/videos without a key must be refused ==="
code=$(curl -s -m 10 -o /tmp/r.json -w '%{http_code}' -X POST "$BASE/v1/videos" \
	-H 'Content-Type: application/json' -d '{"prompt":"a cat"}')
echo "  status: $code"
cat /tmp/r.json
echo

echo "=== 3. /v1/videos with a wrong key must be refused ==="
code=$(curl -s -m 10 -o /tmp/r.json -w '%{http_code}' -X POST "$BASE/v1/videos" \
	-H 'Authorization: Bearer wrong-key' -H 'Content-Type: application/json' -d '{"prompt":"a cat"}')
echo "  status: $code"
echo

echo "=== 4. /admin/status with an ordinary API key must be refused ==="
code=$(curl -s -m 10 -o /tmp/r.json -w '%{http_code}' "$BASE/admin/status" \
	-H "Authorization: Bearer $API_KEY")
echo "  status: $code (want 401)"
echo

echo "=== 5. /admin/status with the admin key ==="
code=$(curl -s -m 10 -o /tmp/r.json -w '%{http_code}' "$BASE/admin/status" \
	-H "Authorization: Bearer $ADMIN_KEY")
echo "  status: $code (want 200)"
head -c 320 /tmp/r.json
echo

echo
echo "=== 6. validation: an empty prompt ==="
code=$(curl -s -m 10 -o /tmp/r.json -w '%{http_code}' -X POST "$BASE/v1/videos" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' -d '{"prompt":""}')
echo "  status: $code (want 400)"
cat /tmp/r.json
echo

echo "=== 7. a real submission (this exercises the egress layer) ==="
code=$(curl -s -m 90 -o /tmp/r.json -w '%{http_code}' -X POST "$BASE/v1/videos" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
	-d '{"prompt":"a cat sitting on a windowsill, cinematic","ratio":"9:16","duration":10}')
echo "  status: $code (202 means the task was accepted)"
cat /tmp/r.json
echo

TASK_ID=$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' /tmp/r.json)
if [ -n "$TASK_ID" ]; then
	echo
	echo "=== 8. poll the task until it settles (up to 5 minutes) ==="
	i=0
	while [ $i -lt 30 ]; do
		curl -s -m 15 -o /tmp/t.json "$BASE/v1/videos/$TASK_ID" -H "Authorization: Bearer $API_KEY"
		state=$(sed -n 's/.*"state":"\([^"]*\)".*/\1/p' /tmp/t.json)
		videos=$(grep -o '"url":"[^"]*"' /tmp/t.json | wc -l)
		echo "  +$((i * 10))s  state=$state  videos=$videos"
		if [ "$state" = "succeeded" ] || [ "$state" = "failed" ]; then
			break
		fi
		i=$((i + 1))
		sleep 10
	done
	echo "  final:"
	head -c 600 /tmp/t.json
	echo
	if [ "$state" = "succeeded" ]; then
		echo
		echo "=== 8b. fetch the video through the service (follows DOLA_STORE_MODE) ==="
		curl -s -m 30 -o /dev/null -w '  /v1/videos/{id}/content -> http %{http_code}, %{size_download} bytes, type %{content_type}\n' \
			"$BASE/v1/videos/$TASK_ID/content" -H "Authorization: Bearer $API_KEY"
		echo -n "  and the URL itself answers: "
		VIDEO_URL=$(sed -n 's/.*"url":"\([^"]*\)".*/\1/p' /tmp/t.json | head -1)
		if [ -n "$VIDEO_URL" ]; then
			curl -s -m 30 -o /dev/null -w 'http %{http_code}, %{size_download} bytes\n' "$VIDEO_URL"
		else
			echo "no url in the response"
		fi
	fi
fi

echo
echo "=== 9. footprint after the exercise ==="
PID=$(pgrep -f '/opt/dola2api/dola2api' | head -1)
awk '/^(VmRSS|RssAnon)/ {print "  " $0}' /proc/$PID/status

rm -f /tmp/r.json /tmp/t.json
