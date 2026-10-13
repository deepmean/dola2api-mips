#!/bin/sh
# Print a task's detail from the local service, for debugging the last mile.
BASE=http://127.0.0.1:8790
CONF=/opt/dola2api/dola2api.conf
API_KEY=$(sed -n 's/^DOLA_API_KEYS="\(.*\)"/\1/p' "$CONF")

TASK=${1:-}
if [ -z "$TASK" ]; then
	echo "usage: $0 <task-id>"
	echo "recent tasks:"
	ADMIN_KEY=$(sed -n 's/^DOLA_ADMIN_KEY="\(.*\)"/\1/p' "$CONF")
	curl -s -m 10 "$BASE/admin/tasks?limit=5" -H "Authorization: Bearer $ADMIN_KEY"
	echo
	exit 1
fi

curl -s -m 15 "$BASE/v1/videos/$TASK" -H "Authorization: Bearer $API_KEY"
echo
