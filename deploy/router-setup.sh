#!/bin/sh
# Finish the dola2api install on the router.
#
# Runs on the device. Generates the API keys here rather than shipping them in,
# so they never pass through the machine that uploads the files.
set -e

cd /opt/dola2api
umask 077

# BusyBox on this build has no base64, so pull alphanumerics straight out of
# /dev/urandom. 40 characters of [A-Za-z0-9] is about 238 bits.
gen_key() {
	tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 40
}

KEY=$(gen_key)
ADMIN=$(gen_key)

# Only replace the two key lines; everything else in the file is the commented
# template and stays as it is.
sed -i "s|^DOLA_API_KEYS=.*|DOLA_API_KEYS=\"$KEY\"|" dola2api.conf
sed -i "s|^DOLA_ADMIN_KEY=.*|DOLA_ADMIN_KEY=\"$ADMIN\"|" dola2api.conf

# accounts.json is built from the uploaded cookies.txt, so the session cookie is
# never written by hand.
if [ -f cookies.txt ]; then
	COOKIE=$(grep -v '^#' cookies.txt | grep -v '^[[:space:]]*$' | head -1)
	cat > accounts.json <<EOF
[
  {
    "label": "primary",
    "cookie": "$COOKIE",
    "enabled": true
  }
]
EOF
	rm -f cookies.txt
	chmod 600 accounts.json
fi

chmod 600 dola2api.conf
chmod 755 dola2api

echo "=== installed files ==="
ls -l /opt/dola2api

echo "=== key sanity (lengths only, not values) ==="
echo "api key length:   $(grep '^DOLA_API_KEYS=' dola2api.conf | sed 's/.*="\(.*\)"/\1/' | wc -c)"
echo "admin key length: $(grep '^DOLA_ADMIN_KEY=' dola2api.conf | sed 's/.*="\(.*\)"/\1/' | wc -c)"

echo "=== accounts.json validity ==="
if [ -f accounts.json ]; then
	echo "entries: $(grep -c '"cookie"' accounts.json)"
	echo "cookie has sessionid: $(grep -c 'sessionid=' accounts.json)"
else
	echo "MISSING"
fi

echo "=== config load test (runs the binary, expects it to reject nothing) ==="
# A dry parse: start it with an invalid port so it exits before binding, and
# check that it fails on the port rather than on the config.
DOLA_PORT=99999 /opt/dola2api/dola2api 2>&1 | head -3 || true
