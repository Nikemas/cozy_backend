#!/usr/bin/env bash
# Read-only smoke test of a deployed Cozy stack (storefront + public API).
#
#   bash scripts/smoke.sh https://cozy.erpsystemsales.com
#   bash scripts/smoke.sh https://cozy.erpsystemsales.com muzhskoe   # category slug
#   bash scripts/smoke.sh <BASE_URL> <category-slug> <product-slug>
#
# Only GET requests, no cookies kept, nothing is written. Each check prints
# PASS/FAIL with the reason; exit code is the number of failed checks
# (capped at 125), 0 = all green. Runs on the operator's laptop or in CI:
# needs bash (3.2+ — stock macOS works), curl, grep, sed, mktemp.
#
# Every HTML page is also checked for leaked template syntax ("{{") and
# unfilled content placeholders ("[ЗАПОЛНИТЬ"). Language: the site picks it
# from ?lang=ru|ky (then the cozy_lang cookie), and <html lang="..">
# reflects it, so both languages are fetched with ?lang= and that attribute
# is asserted.
#
# Settings (env vars, optional):
#   SMOKE_TIMEOUT  seconds per request (default 15)
set -u

BASE="${1:-}"
CATEGORY="${2:-}"
PRODUCT="${3:-}"
TIMEOUT="${SMOKE_TIMEOUT:-15}"
STATIC_PAGES="about contacts delivery privacy terms"
LANGS="ru ky"
MAX_EXIT=125

if [ -z "$BASE" ]; then
  sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
fi
case "$BASE" in
  http://* | https://*) ;;
  *)
    echo "smoke: BASE_URL must start with http:// or https://" >&2
    exit 2
    ;;
esac
case "$TIMEOUT" in
  '' | *[!0-9]*)
    echo "smoke: SMOKE_TIMEOUT must be a whole number of seconds" >&2
    exit 2
    ;;
esac
command -v curl >/dev/null 2>&1 || {
  echo "smoke: curl is not installed" >&2
  exit 2
}
BASE="${BASE%/}"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/cozy-smoke.XXXXXX")" || exit 2
trap 'rm -rf "$WORK"' EXIT
BODY="$WORK/body"
HEADERS="$WORK/headers"

passed=0
failed=0
failures=""

pass() {
  passed=$((passed + 1))
  printf 'PASS  %s\n' "$1"
}
fail() {
  failed=$((failed + 1))
  failures="$failures
  - $1: $2"
  printf 'FAIL  %s — %s\n' "$1" "$2"
}

# fetch PATH: GET $BASE$PATH without following redirects. Sets CODE
# ("000" on a network error) and CTYPE; body in $BODY, headers in $HEADERS.
fetch() {
  CODE="$(curl -sS -m "$TIMEOUT" -o "$BODY" -D "$HEADERS" -w '%{http_code}' "$BASE$1" 2>"$WORK/err")" || CODE="000"
  CTYPE="$(grep -i '^content-type:' "$HEADERS" 2>/dev/null | tail -n1 | tr -d '\r' | sed 's/^[^:]*:[[:space:]]*//')"
}

# body_problems [NEEDLE...]: prints why the body is bad, nothing if it's
# fine. Each NEEDLE must occur (fixed string).
body_problems() {
  local needle
  for needle in "$@"; do
    grep -qF -- "$needle" "$BODY" || printf 'missing "%s"; ' "$needle"
  done
}

# html_problems [NEEDLE...]: body_problems plus the HTML-only leak checks.
html_problems() {
  local n
  case "$CTYPE" in
    text/html*) ;;
    *) printf 'content-type "%s", want text/html; ' "$CTYPE" ;;
  esac
  body_problems "$@"
  if grep -qF '{{' "$BODY"; then
    printf 'template syntax "{{" leaked (%s line(s)); ' "$(grep -cF '{{' "$BODY")"
  fi
  n="$(grep -oF '[ЗАПОЛНИТЬ' "$BODY" | wc -l | tr -d ' ')"
  [ "$n" -eq 0 ] || printf 'placeholder "[ЗАПОЛНИТЬ" x%s; ' "$n"
}

# check NAME PATH WANT_CODE KIND [NEEDLE...]: KIND is html, json or text.
check() {
  local name="$1" path="$2" want="$3" kind="$4" problems
  shift 4
  fetch "$path"
  if [ "$CODE" = "000" ]; then
    fail "$name" "GET $path: network error: $(tr -d '\r' <"$WORK/err" | head -n1)"
    return 1
  fi
  if [ "$CODE" != "$want" ]; then
    fail "$name" "GET $path: HTTP $CODE, want $want"
    return 1
  fi
  case "$kind" in
    html) problems="$(html_problems "$@")" ;;
    json)
      problems="$(body_problems "$@")"
      case "$CTYPE" in
        application/json*) ;;
        *) problems="${problems}content-type \"$CTYPE\", want application/json; " ;;
      esac
      ;;
    *) problems="$(body_problems "$@")" ;;
  esac
  if [ -n "$problems" ]; then
    fail "$name" "GET $path: ${problems%; }"
    return 1
  fi
  pass "$name ($path $CODE)"
}

# first_link PREFIX: first href="PREFIX<slug>" in $BODY, slug only.
first_link() {
  grep -o "href=\"$1[^\"?#]*\"" "$BODY" | head -n1 | sed -e "s#^href=\"$1##" -e 's#"$##'
}

echo "smoke: $BASE ($(date '+%F %T'))"

# --- probes -------------------------------------------------------------
check "healthz" /healthz 200 text
check "readyz" /readyz 200 json '"status":"ok"' '"db":"ok"' '"minio":"ok"'

# --- catalog ------------------------------------------------------------
if check "home" "/?lang=ru" 200 html '<html lang="ru"' 'href="/catalog/' 'href="/product/'; then
  [ -n "$CATEGORY" ] || CATEGORY="$(first_link /catalog/)"
  [ -n "$PRODUCT" ] || PRODUCT="$(first_link /product/)"
fi

if [ -n "$CATEGORY" ]; then
  if check "category" "/catalog/$CATEGORY" 200 html 'href="/product/'; then
    # Prefer a product that the category really lists.
    [ -n "${3:-}" ] || PRODUCT="$(first_link /product/)"
  fi
else
  fail "category" "no category slug (none on the home page and none given as argument 2)"
fi

if [ -n "$PRODUCT" ]; then
  check "product" "/product/$PRODUCT" 200 html 'application/ld+json' 'og:image'
else
  fail "product" "no product slug (none found and none given as argument 3)"
fi

# --- cart (guest): sent to /profile to log in, by design ------------------
fetch /cart
location="$(grep -i '^location:' "$HEADERS" 2>/dev/null | tail -n1 | tr -d '\r' | sed 's/^[^:]*:[[:space:]]*//')"
case "$CODE:$location" in
  30[23]:*/profile*) pass "cart guest (/cart $CODE -> $location)" ;;
  200:*) pass "cart guest (/cart 200)" ;;
  *) fail "cart guest" "GET /cart: HTTP $CODE location \"$location\", want 303 -> /profile or 200" ;;
esac
check "profile guest" /profile 200 html

# --- info pages, both languages -----------------------------------------
for lang in $LANGS; do
  for page in $STATIC_PAGES; do
    check "$page [$lang]" "/$page?lang=$lang" 200 html "<html lang=\"$lang\""
  done
done
check "branches" "/branches?lang=ru" 200 html '<html lang="ru"'

# --- SEO ----------------------------------------------------------------
check "sitemap" /sitemap.xml 200 text '<urlset' '<loc>' '/product/'
check "robots" /robots.txt 200 text 'User-agent:' 'Sitemap:'

# --- public API ---------------------------------------------------------
check "api points" /api/v1/points 200 json '"items"'
check "api app config" /api/v1/app/config 200 json '"min_version"' '"delivery_fee"'

# --- 404 ----------------------------------------------------------------
check "404 html" "/smoke-no-such-page-$$" 404 html '<html'
check "404 api json" "/api/v1/smoke-no-such-endpoint-$$" 404 json '"code":"not_found"'

# --- summary ------------------------------------------------------------
echo
echo "smoke: $passed passed, $failed failed"
if [ "$failed" -gt 0 ]; then
  printf 'smoke: failures:%s\n' "$failures"
  [ "$failed" -gt "$MAX_EXIT" ] && failed="$MAX_EXIT"
  exit "$failed"
fi
exit 0
