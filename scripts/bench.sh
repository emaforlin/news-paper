#!/bin/sh
# Runs the full pipeline offline against tools/fixtureserver.
#   PAGES=<dir of <name>/source.html> scripts/bench.sh
#   LLM=fail exercises the heuristic fallback.
set -eu

: "${PAGES:?set PAGES to a directory of <name>/source.html files}"
PAGES=$(cd "$PAGES" && pwd)
cd "$(dirname "$0")/.."

GO=${GO:-go}
ADDR=127.0.0.1:8099 # must match testdata/bench.config.json

mkdir -p dist
"$GO" build -o dist/news-paper ./cmd/news-paper
"$GO" build -o dist/fixtureserver ./tools/fixtureserver

dist/fixtureserver -addr "$ADDR" -pages "$PAGES" -llm "${LLM:-ok}" &
server=$!
trap 'kill "$server" 2>/dev/null || true' EXIT
trap 'exit 1' INT TERM

tries=0
while :; do
	# The pause lets a failed bind show before something else answers for us.
	sleep 0.2
	kill -0 "$server" 2>/dev/null || { echo "fixtureserver exited; is $ADDR in use?" >&2; exit 1; }
	curl --noproxy '*' --max-time 1 -fsS "http://$ADDR/hn/search" >/dev/null 2>&1 && break
	tries=$((tries + 1))
	[ "$tries" -lt 10 ] || { echo "fixtureserver did not answer on $ADDR" >&2; exit 1; }
done

rm -f dist/bench-state.json
OPENROUTER_API_KEY=bench dist/news-paper -config testdata/bench.config.json \
	run -dry-run -out dist/bench.epub
