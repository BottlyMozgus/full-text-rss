#!/bin/sh
# Fixture site for e2e tests: serves a small RSS feed + two article pages.
# Article A: clean structure readability handles. Article B: needs a site rule.
set -e
PORT="${1:-9988}"
DIR="$(cd "$(dirname "$0")" && pwd)/fixtures"
cd "$DIR"
trap 'kill 0' EXIT INT TERM
python3 -m http.server "$PORT" --bind 127.0.0.1 >/dev/null 2>&1 &
wait
