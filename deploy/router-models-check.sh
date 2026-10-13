#!/bin/sh
# Show the model listing and exercise the chat endpoint with a proper JSON body
# (the body is written to a file, so shell quoting cannot mangle it).

BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf
API_KEY=$(sed -n 's/^DOLA_API_KEYS="\(.*\)"/\1/p' "$CONF")

echo "=== GET /v1/models ==="
curl -s -m 15 "$BASE/v1/models" -H "Authorization: Bearer $API_KEY"
echo
echo

echo "=== POST /v1/chat/completions (chat model from the listing) ==="
cat > /tmp/req.json <<'EOF'
{"model":"dola-chat","messages":[{"role":"user","content":"用一句话介绍你自己"}]}
EOF
curl -s -m 90 -o /tmp/chat.json -w '  http %{http_code}  %{time_total}s\n' \
	-X POST "$BASE/v1/chat/completions" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
	--data-binary @/tmp/req.json
echo "  body (first 900 bytes):"
head -c 900 /tmp/chat.json
echo
echo

echo "=== POST /v1/images (image model from the listing) ==="
cat > /tmp/img.json <<'EOF'
{"model":"dola-image","prompt":"a red paper boat on a small stream","ratio":"1:1"}
EOF
curl -s -m 60 -o /tmp/img_out.json -w '  http %{http_code}\n' \
	-X POST "$BASE/v1/images" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
	--data-binary @/tmp/img.json
head -c 300 /tmp/img_out.json
echo
echo

echo "=== service log ==="
logread -e dola2api | tail -5
rm -f /tmp/req.json /tmp/chat.json /tmp/img.json /tmp/img_out.json
