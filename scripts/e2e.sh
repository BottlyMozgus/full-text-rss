#!/bin/sh
# End-to-end checks for full-text-rss.
#
# Modes:
#   sh scripts/e2e.sh                 build the binary, start it locally, test it
#   E2E_TARGET=http://host:port sh scripts/e2e.sh
#                                     test an already-running instance (e.g. a
#                                     container). Caller must start the fixture
#                                     site and make it reachable as E2E_FIXTURE_BASE.
#
# Env:
#   E2E_TARGET        base URL of the running service (skips build/start)
#   E2E_PASSWORD      admin password for the target (default devpass)
#   E2E_FIXTURE_BASE  base URL the SERVICE uses to reach the fixture site
#                     (default http://127.0.0.1:9988)
set -eu
cd "$(dirname "$0")/.."

PASSWORD="${E2E_PASSWORD:-devpass}"
FIXTURE_BASE="${E2E_FIXTURE_BASE:-http://127.0.0.1:9988}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/ftrss-e2e.XXXXXX")"
PIDS=""

cleanup() {
  for p in $PIDS; do kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

fail() { echo "E2E FAIL: $1" >&2; exit 1; }

# render_fixture DEST BASE copies scripts/fixtures to DEST, replacing the
# FIXTURE_HOST placeholder so article links resolve for the service.
render_fixture() {
  mkdir -p "$1"
  for f in scripts/fixtures/*; do
    sed "s#FIXTURE_HOST#$2#g" "$f" > "$1/$(basename "$f")"
  done
}

if [ -n "${E2E_TARGET:-}" ]; then
  BASE="$E2E_TARGET"
  echo "== target: $BASE (external) =="
  echo "== note: fixture must be served by the caller from a rendered copy (see CI) =="
else
  echo "== building binary =="
  BIN="$WORK/ftrss"
  go build -o "$BIN" ./cmd/full-text-rss

  echo "== fixture site =="
  render_fixture "$WORK/site" "http://127.0.0.1:9988"
  python3 -m http.server 9988 --bind 127.0.0.1 --directory "$WORK/site" >"$WORK/fixture.log" 2>&1 &
  PIDS="$PIDS $!"
  sleep 1

  echo "== starting server =="
  mkdir -p "$WORK/data"
  FTRSS_ADMIN_PASSWORD="$PASSWORD" \
  FTRSS_ADDR=127.0.0.1:9977 \
  FTRSS_DATA_DIR="$WORK/data" \
  FTRSS_FETCH_SPACING=10ms FTRSS_FETCH_JITTER=10ms FTRSS_FETCH_TIMEOUT=5s \
    "$BIN" >"$WORK/server.log" 2>&1 &
  SRVPID=$!
  PIDS="$PIDS $SRVPID"
  BASE="http://127.0.0.1:9977"
  sleep 1
fi

wait_healthy() {
  i=0
  while [ "$i" -lt 30 ]; do
    if curl -sf "$BASE/healthz" >/dev/null 2>&1; then return 0; fi
    i=$((i + 1)); sleep 1
  done
  fail "service did not become healthy at $BASE"
}
wait_healthy

COOKIES="$WORK/cookies.txt"

echo "== health =="
curl -sf "$BASE/healthz" | grep -q ok || fail "healthz"

echo "== auth =="
code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/admin/feeds")
case "$code" in 302|303) ;; *) fail "unauthed /admin/feeds expected redirect, got $code" ;; esac
curl -sf -c "$COOKIES" -d "password=$PASSWORD" "$BASE/login" >/dev/null || fail "login POST"
code=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIES" "$BASE/admin/feeds")
[ "$code" = "200" ] || fail "authed /admin/feeds expected 200, got $code"

echo "== create feed via API =="
FEEDJSON=$(curl -sf -b "$COOKIES" -H "Content-Type: application/json" \
  -d "{\"source_url\":\"$FIXTURE_BASE/feed.xml\",\"title\":\"Fixture Feed\"}" \
  "$BASE/api/feeds") || fail "create feed"
FEEDID=$(echo "$FEEDJSON" | jq -r .id)
TOKEN=$(echo "$FEEDJSON" | jq -r .token)
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || fail "no token in response"
echo "feed id=$FEEDID"

echo "== refresh and wait for status =="
curl -sf -b "$COOKIES" -X POST "$BASE/api/feeds/$FEEDID/refresh" >/dev/null || fail "refresh"
i=0
while [ "$i" -lt 30 ]; do
  ST=$(curl -sf -b "$COOKIES" "$BASE/api/feeds/$FEEDID/status" | jq -r .last_status)
  [ "$ST" = "ok" ] && break
  i=$((i + 1)); sleep 1
done
STATUS=$(curl -sf -b "$COOKIES" "$BASE/api/feeds/$FEEDID/status")
echo "$STATUS" | jq -r '.last_status, (.items[]? | "\(.guid) -> \(.state)")'
echo "$STATUS" | jq -e '.last_status == "ok"' >/dev/null || fail "feed not ok"
echo "$STATUS" | jq -e '[.items[]? | select(.state=="extracted")] | length >= 2' >/dev/null || fail "expected >=2 extracted items"
echo "$STATUS" | jq -e '[.items[]? | select(.state=="summary-fallback")] | length >= 1' >/dev/null || fail "expected >=1 summary-fallback item"

echo "== served feed XML =="
code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/feed/$TOKEN.xml")
[ "$code" = "200" ] || fail "token feed expected 200, got $code"
curl -sf "$BASE/feed/$TOKEN.xml" > "$WORK/feed-out.xml"
grep -q "xmlns:content" "$WORK/feed-out.xml" || fail "content namespace missing"
grep -q "summary-fallback" "$WORK/feed-out.xml" || fail "fallback comment missing"
grep -q "full body of the first article" "$WORK/feed-out.xml" || fail "extracted content missing"
grep -q "actual content lives in this div" "$WORK/feed-out.xml" || fail "rule-based content missing"
! grep -qi "<script" "$WORK/feed-out.xml" || fail "script leaked into output"
grep -q "This one fails extraction" "$WORK/feed-out.xml" || fail "fallback summary missing"
echo "XML checks passed ($(wc -c < "$WORK/feed-out.xml") bytes)"

echo "== wrong token 404 =="
code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/feed/deadbeef000000000000000000000000.xml")
[ "$code" = "404" ] || fail "wrong token expected 404, got $code"

echo "== ETag/304 =="
ETAG=$(curl -sfI "$BASE/feed/$TOKEN.xml" | tr -d '\r' | awk 'tolower($1)=="etag:"{print $2}')
[ -n "$ETAG" ] || fail "no ETag header"
code=$(curl -s -o /dev/null -w "%{http_code}" -H "If-None-Match: $ETAG" "$BASE/feed/$TOKEN.xml")
[ "$code" = "304" ] || fail "If-None-Match expected 304, got $code"

echo "== admin pages =="
for p in /admin/feeds "/admin/feeds/$FEEDID" /admin/feeds/new /admin/rules /admin/rules/new /admin/ondemand; do
  code=$(curl -s -o /dev/null -w "%{http_code}" -b "$COOKIES" "$BASE$p")
  [ "$code" = "200" ] || fail "GET $p expected 200, got $code"
done

echo "== on-demand endpoint =="
curl -sf -b "$COOKIES" "$BASE/on-demand?url=$(printf '%s' "$FIXTURE_BASE/feed.xml" | jq -sRr @uri)" > "$WORK/ondemand.xml" || fail "on-demand request"
grep -q "actual content lives in this div" "$WORK/ondemand.xml" || fail "on-demand rule content missing"
grep -q "summary-fallback" "$WORK/ondemand.xml" || fail "on-demand fallback missing"

echo "== persistence across restart (local mode only) =="
if [ -z "${E2E_TARGET:-}" ]; then
  kill "$SRVPID" 2>/dev/null || true
  sleep 1
  FTRSS_ADMIN_PASSWORD="$PASSWORD" FTRSS_ADDR=127.0.0.1:9977 FTRSS_DATA_DIR="$WORK/data" \
    "$BIN" >>"$WORK/server.log" 2>&1 &
  PIDS="$PIDS $!"
  wait_healthy
  curl -sf "$BASE/feed/$TOKEN.xml" | grep -q "full body of the first article" || fail "content lost after restart"
  echo "restart persistence OK"
else
  echo "skipped (external target)"
fi

echo ""
echo "E2E PASS"
