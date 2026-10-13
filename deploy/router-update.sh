#!/bin/sh
# Swap in a new binary and restart, keeping the previous one for rollback.
#
# Called on the device after the new build has been uploaded to
# /opt/dola2api/dola2api.new. The old binary is kept as dola2api.prev so a bad
# build is one mv away from being undone, which matters on a router where
# getting a shell back is the expensive part.

set -e
cd /opt/dola2api

if [ ! -f dola2api.new ]; then
	echo "FAIL: dola2api.new is missing"
	exit 1
fi

NEW_SIZE=$(wc -c < dola2api.new)
if [ "$NEW_SIZE" -lt 1000000 ]; then
	echo "FAIL: dola2api.new is only $NEW_SIZE bytes; refusing to install it"
	rm -f dola2api.new
	exit 1
fi

# The running binary must not be overwritten in place: on this filesystem that
# can fail with "text file busy" or leave a half-written executable.
/etc/init.d/dola2api stop || true
sleep 1

[ -f dola2api ] && mv dola2api dola2api.prev
mv dola2api.new dola2api
chmod 755 dola2api

/etc/init.d/dola2api start
sleep 4

PID=$(pgrep -f '/opt/dola2api/dola2api' | head -1)
if [ -z "$PID" ]; then
	echo "FAIL: the service did not come back; rolling back"
	/etc/init.d/dola2api stop || true
	mv dola2api dola2api.bad
	mv dola2api.prev dola2api
	/etc/init.d/dola2api start
	sleep 3
	pgrep -f '/opt/dola2api/dola2api' > /dev/null && echo "rolled back to the previous binary" || echo "rollback also failed"
	exit 1
fi

echo "running pid=$PID"
echo "version reported by /health:"
curl -s -m 10 http://127.0.0.1:8790/health | sed 's/.*"version":"\([^"]*\)".*/\1/' | sed 's/^/  /'
awk '/^(VmRSS|RssAnon)/ {print "  " $0}' /proc/$PID/status
