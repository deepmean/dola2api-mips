#!/bin/sh
# Report the service's real footprint on the device.
#
# A separate file rather than an inline ssh command: the quoting survives
# PowerShell, ssh and the remote shell badly enough that a pipe inside a grep
# pattern was being executed as a command.

PID=$(pgrep -f '/opt/dola2api/dola2api' | head -1)
if [ -z "$PID" ]; then
	echo "not running"
	exit 1
fi

echo "pid: $PID"
echo
echo "=== /proc/$PID/status ==="
awk '/^(Name|VmPeak|VmSize|VmRSS|RssAnon|RssFile|RssShmem|Threads)/ {print "  " $0}' /proc/$PID/status
echo
echo "=== open sockets ==="
ls -l /proc/$PID/fd 2>/dev/null | grep -c socket | sed 's/^/  socket fds: /'
echo
echo "=== system memory ==="
free
echo
echo "=== uptime / load ==="
uptime
