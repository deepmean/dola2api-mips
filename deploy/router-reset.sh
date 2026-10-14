#!/bin/sh
# Release every parked session on the local service.

BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf
ADMIN_KEY=$(sed -n 's/^DOLA_ADMIN_KEY="\(.*\)"/\1/p' "$CONF")

echo "=== before ==="
curl -s -m 10 "$BASE/health" |
	sed 's/.*"accounts":\[//; s/\].*//' |
	grep -o '"label":"[^"]*","available":[a-z]*,"cooldown_remaining":[0-9]*' |
	sed 's/"label":"//; s/","available":/  available=/; s/,"cooldown_remaining":/  cooldown=/'

echo
echo "=== POST /admin/reset ==="
curl -s -m 15 -X POST "$BASE/admin/reset" -H "Authorization: Bearer $ADMIN_KEY"
echo

echo
echo "=== after ==="
curl -s -m 10 "$BASE/health" |
	sed 's/.*"accounts":\[//; s/\].*//' |
	grep -o '"label":"[^"]*","available":[a-z]*,"cooldown_remaining":[0-9]*' |
	sed 's/"label":"//; s/","available":/  available=/; s/,"cooldown_remaining":/  cooldown=/'
