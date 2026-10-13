#!/bin/sh
# One real submission, then poll it. Deliberately minimal: with the upstream
# refusing by submit rate, every extra request spends budget.

BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf
API_KEY=$(sed -n 's/^DOLA_API_KEYS="\(.*\)"/\1/p' "$CONF")

echo "=== accounts in the pool ==="
curl -s -m 10 "$BASE/health" |
	sed 's/.*"accounts":\[//; s/\].*//' |
	grep -o '"label":"[^"]*","available":[a-z]*' |
	sed 's/"label":"//; s/","available":/  available=/' |
	sed 's/^/  /'
echo

echo "=== one submission ==="
code=$(curl -s -m 60 -o /tmp/sub.json -w '%{http_code}' -X POST "$BASE/v1/videos" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
	-d '{"prompt":"a red paper boat floating down a small stream, morning light","ratio":"9:16","duration":5}')
echo "  http $code"
cat /tmp/sub.json
echo

TASK_ID=$(sed -n 's/.*"id":"\([^"]*\)".*/\1/p' /tmp/sub.json)
if [ -z "$TASK_ID" ]; then
	echo "  no task id; nothing to poll"
	exit 1
fi

echo
echo "=== poll (up to 8 minutes) ==="
i=0
while [ $i -lt 48 ]; do
	curl -s -m 15 -o /tmp/t.json "$BASE/v1/videos/$TASK_ID" -H "Authorization: Bearer $API_KEY"
	state=$(sed -n 's/.*"state":"\([^"]*\)".*/\1/p' /tmp/t.json)
	items=$(grep -o '"kind":"[^"]*"' /tmp/t.json | wc -l)
	printf '  +%3ds  state=%-9s items=%s\n' "$((i * 10))" "$state" "$items"
	if [ "$state" = "succeeded" ] || [ "$state" = "failed" ]; then
		break
	fi
	i=$((i + 1))
	sleep 10
done
echo
echo "  final:"
head -c 700 /tmp/t.json
echo
echo
echo "=== service log ==="
logread -e dola2api | tail -6
rm -f /tmp/sub.json /tmp/t.json
