#!/bin/sh
# End-to-end smoke test: fixture site + built binary + curl checks.
# Usage: scripts/e2e.sh
set -e
cd "$(dirname "$0")/.."
ROOT="$PWD"

echo "== building =="
go build -o /tmp/opencode/ftrss-test ./cmd/full-text-rss

echo "== fixture site =="
sh scripts/fixture-server.sh 9988 &
FIXPID=$!
sleep 1

DATA="$(mktemp -d /tmp/opencode/ftrss-data.XXXXXX)"
export FTRSS_ADMIN_PASSWORD=devpass
export FTRSS_ADDR=127.0.0.1:9977
export FTRSS_DATA_DIR="$DATA"
export FTRSS_FETCH_SPACING=10ms
export FTRSS_FETCH_JITTER=10ms
export FTRSS_FETCH_TIMEOUT=5s

echo "== starting server =="
/tmp/opencode/ftrss-test >"$DATA/server.log" 2>&1 &
SRVPID=$!
sleep 1

cleanup() {
  kill "$SRVPID" 2>/dev/null || true
  kill 0 2>/dev/null || true
}
trap cleanup EXIT INT TERM

fail() { echo "E2E FAIL: $1"; exit 1; }

echo "== health =="
curl -sf http://127.0.0.1:9977/healthz | grep -q ok || fail "healthz"

echo "== auth =="
code=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:9977/admin/feeds)
[ "$code" = "302" ] || fail "unauthed /admin/feeds expected 302, got $code"
curl -sf -c "$DATA/cookies.txt" -d "password=devpass" http://127.0.0.1:9977/login >/dev/null || fail "login POST"
code=$(curl -s -o /dev/null -w "%{http_code}" -b "$DATA/cookies.txt" http://127.0.0.1:9977/admin/feeds)
[ "$code" = "200" ] || fail "authed /admin/feeds expected 200, got $code"

echo "== create feed via API =="
FEEDJSON=$(curl -sf -b "$DATA/cookies.txt" -H "Content-Type: application/json" \
  -d '{"source_url":"http://127.0.0.1:9988/feed.xml","title":"Fixture Feed"}' \
  http://127.0.0.1:9977/api/feeds) || fail "create feed"
FEEDID=$(echo "$FEEDJSON" | jq -r .id)
TOKEN=$(echo "$FEEDJSON" | jq -r .token)
[ -n "$TOKEN" ] && [ "$TOKEN" != "null" ] || fail "no token in response"
echo "feed id=$FEEDID token=$TOKEN"

echo "== trigger refresh =="
curl -sf -b "$DATA/cookies.txt" -X POST http://127.0.0.1:9977/api/feeds/$FEEDID/refresh >/dev/null || fail "refresh"
sleep 2

echo "== status =="
STATUS=$(curl -sf -b "$DATA/cookies.txt" http://127.0.0.1:9977/api/feeds/$FEEDID/status)
echo "$STATUS" | jq -r '.last_status, (.items[] | "\(.guid) -> \(.state)")'
echo "$STATUS" | jq -e '.last_status == "ok"' >/dev/null || fail "feed not ok"

echo "== served feed XML =="
code=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:9977/feed/$TOKEN.xml")
[ "$code" = "200" ] || fail "token feed expected 200, got $code"
curl -sf "http://127.0.0.1:9977/feed/$TOKEN.xml" > "$DATA/feed-out.xml"
grep -q "xmlns:content" "$DATA/feed-out.xml" || fail "content namespace missing"
grep -q "summary-fallback" "$DATA/feed-out.xml" || fail "fallback comment missing"
grep -q "full body of the first article" "$DATA/feed-out.xml" || fail "extracted content missing"
grep -q "actual content lives in this div" "$DATA/feed-out.xml" || fail "rule-based content missing"
! grep -qi "<script" "$DATA/feed-out.xml" || fail "script leaked into output"
grep -q "This one fails extraction" "$DATA/feed-out.xml" || fail "fallback summary missing"
echo "XML checks passed ($(wc -c < "$DATA/feed-out.xml") bytes)"

echo "== wrong token 404 =="
code=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:9977/feed/deadbeef000000000000000000000000.xml")
[ "$code" = "404" ] || fail "wrong token expected 404, got $code"

echo "== ETag/304 =="
ETAG=$(curl -sfI "http://127.0.0.1:9977/feed/$TOKEN.xml" | tr -d '\r' | awk 'tolower($1)=="etag:"{print $2}')
if [ -n "$ETAG" ]; then
  code=$(curl -s -o /dev/null -w "%{http_code}" -H "If-None-Match: $ETAG" "http://127.0.0.1:9977/feed/$TOKEN.xml")
  [ "$code" = "304" ] || fail "If-None-Match expected 304, got $code"
else
  echo "WARN: no ETag header"
fi

echo "== admin pages =="
for p in /admin/feeds "/admin/feeds/$FEEDID" /admin/feeds/new /admin/rules /admin/rules/new /admin/ondemand; do
  code=$(curl -s -o /dev/null -w "%{http_code}" -b "$DATA/cookies.txt" "http://127.0.0.1:9977$p")
  [ "$code" = "200" ] || fail "GET $p expected 200, got $code"
done
ITEMPREVIEW=$(curl -sf -b "$DATA/cookies.txt" "http://127.0.0.1:9977/admin/feeds/$FEEDID" | grep -o 'href="[^"]*/items/[0-9]*"' | head -1 | sed 's/href="//;s/"$//')
[ -n "$ITEMPREVIEW" ] && code=$(curl -s -o /dev/null -w "%{http_code}" -b "$DATA/cookies.txt" "http://127.0.0.1:9977$ITEMPREVIEW") && [ "$code" = "200" ] || echo "WARN: no item preview link found"

echo "== on-demand endpoint =="
curl -sf -b "$DATA/cookies.txt" "http://127.0.0.1:9977/on-demand?url=http%3A%2F%2F127.0.0.1%3A9988%2Ffeed.xml" > "$DATA/ondemand.xml"
grep -q "actual content lives in this div" "$DATA/ondemand.xml" || fail "on-demand rule content missing"
grep -q "summary-fallback" "$DATA/ondemand.xml" || fail "on-demand fallback missing"
echo "on-demand OK"

echo "== persistence across restart =="
kill "$SRVPID" 2>/dev/null || true
sleep 1
/tmp/opencode/ftrss-test >>"$DATA/server.log" 2>&1 &
SRVPID=$!
sleep 1
code=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:9977/feed/$TOKEN.xml")
[ "$code" = "200" ] || fail "feed after restart expected 200, got $code"
curl -sf "http://127.0.0.1:9977/feed/$TOKEN.xml" | grep -q "full body of the first article" || fail "content lost after restart"
echo "restart persistence OK"

echo ""
echo "E2E PASS"
