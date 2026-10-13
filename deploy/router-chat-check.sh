#!/bin/sh
# Exercise the chat endpoint through the local service.

BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf
API_KEY=$(sed -n 's/^DOLA_API_KEYS="\(.*\)"/\1/p' "$CONF")

echo "=== POST /v1/chat/completions ==="
curl -s -m 90 -o /tmp/chat.json -w '  http %{http_code}  %{time_total}s\n' \
	-X POST "$BASE/v1/chat/completions" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
	-d '{"model":"seedance_v2.0","messages":[{"role":"user","content":"鐢ㄤ竴鍙ヨ瘽浠嬬粛浣犺嚜宸?}]}'
echo
echo "  body (first 1200 bytes):"
head -c 1200 /tmp/chat.json
echo
echo
echo "=== service log ==="
logread -e dola2api | tail -4
rm -f /tmp/chat.json
