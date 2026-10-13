#!/bin/sh
# One-screen summary of the installed service.

BASE=http://127.0.0.1:8790
PID=$(pgrep -f '/opt/dola2api/dola2api' | head -1)

echo "=== files ==="
ls -l /opt/dola2api/

echo
echo "=== service ==="
printf '  enabled:  '; /etc/init.d/dola2api enabled && echo yes || echo no
printf '  pid:      '; echo "${PID:-not running}"
if [ -n "$PID" ]; then
	printf '  rss:      '; awk '/^VmRSS/ {print $2 " kB"}' /proc/$PID/status
	printf '  anon:     '; awk '/^RssAnon/ {print $2 " kB"}' /proc/$PID/status
	printf '  threads:  '; awk '/^Threads/ {print $2}' /proc/$PID/status
fi
printf '  listening: '; netstat -ltn 2>/dev/null | grep -q ':8790' && echo "8790" || echo "no"

echo
echo "=== health ==="
curl -s -m 8 "$BASE/health"

echo
echo
echo "=== system ==="
free | head -2

echo
echo "=== recent log ==="
logread -e dola2api 2>/dev/null | tail -6
