#!/bin/sh
# Check the chat endpoints, streaming and not.

BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf
API_KEY=$(sed -n 's/^DOLA_API_KEYS="\(.*\)"/\1/p' "$CONF")

cat > /tmp/s.json <<'EOF'
{"stream":true,"model":"dola-chat","messages":[{"role":"user","content":"你好"}]}
EOF
cat > /tmp/n.json <<'EOF'
{"stream":false,"model":"dola-chat","messages":[{"role":"user","content":"你好"}]}
EOF

echo "=== stream=true (must not be 501 any more) ==="
curl -s -m 90 -o /tmp/s.out -w '  http %{http_code}  type=%{content_type}  bytes=%{size_download}\n' \
	-X POST "$BASE/v1/chat/completions" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
	--data-binary @/tmp/s.json
echo "  body:"
head -c 500 /tmp/s.out
echo
echo

echo "=== stream=false ==="
curl -s -m 90 -o /tmp/n.out -w '  http %{http_code}  type=%{content_type}  bytes=%{size_download}\n' \
	-X POST "$BASE/v1/chat/completions" \
	-H "Authorization: Bearer $API_KEY" -H 'Content-Type: application/json' \
	--data-binary @/tmp/n.json
echo "  body:"
head -c 400 /tmp/n.out
echo
echo

echo "=== models ==="
curl -s -m 15 "$BASE/v1/models" -H "Authorization: Bearer $API_KEY" | head -c 300
echo
rm -f /tmp/s.json /tmp/n.json /tmp/s.out /tmp/n.out
