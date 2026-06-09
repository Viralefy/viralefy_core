#!/usr/bin/env bash
# security-probes.sh — smoke OWASP probes against https://api.viralefy.com.
#
# Cada probe ecoa PASS/FAIL e o exit code consolidado reflete falhas.
# Use:
#   ./security-probes.sh                   # roda contra prod (api.viralefy.com)
#   API=https://staging.viralefy.com ./security-probes.sh   # alvo custom
#
# Não levanta dados; só envia tráfego mínimo pra checar comportamento de
# rate-limit, auth, sql-injection e CORS. Seguro pra rodar em smoke CI.

set -u

API="${API:-https://api.viralefy.com}"

PASS=0
FAIL=0
RESULTS=()

probe_pass() {
  PASS=$((PASS + 1))
  RESULTS+=("PASS: $1")
  printf 'PASS  %s\n' "$1"
}

probe_fail() {
  FAIL=$((FAIL + 1))
  RESULTS+=("FAIL: $1")
  printf 'FAIL  %s\n' "$1"
}

# Probe 1: 12 logins rápidos contra /v1/auth/user/login devem disparar 429
# em algum momento. Rate-limit do router é 10/15min/IP.
probe_rate_limit() {
  local got_429=0
  local i
  for i in $(seq 1 12); do
    code=$(curl -s -o /dev/null -w '%{http_code}' \
      -X POST "$API/v1/auth/user/login" \
      -H 'Content-Type: application/json' \
      -d '{"email":"probe@example.com","password":"x"}')
    if [ "$code" = "429" ]; then
      got_429=1
      break
    fi
  done
  if [ "$got_429" = "1" ]; then
    probe_pass "rate-limit: 429 after burst on /v1/auth/user/login"
  else
    probe_fail "rate-limit: no 429 after 12 logins — rate-limit may be open"
  fi
}

# Probe 2: SQL injection probe em /v1/plans?category= deve responder 200
# ou 422, NUNCA 500. 500 = SQL error vazando.
probe_sql_injection() {
  local probe="' OR 1=1--"
  local encoded
  encoded=$(printf '%s' "$probe" | sed "s/ /%20/g;s/'/%27/g")
  code=$(curl -s -o /dev/null -w '%{http_code}' \
    "$API/v1/plans?category=$encoded")
  case "$code" in
    200|400|422)
      probe_pass "sql-injection: /v1/plans?category=<payload> → $code (safe)"
      ;;
    5*)
      probe_fail "sql-injection: /v1/plans?category=<payload> → $code (SQL error leak)"
      ;;
    *)
      # 3xx/4xx outros — informativo, mas não falha.
      probe_pass "sql-injection: /v1/plans?category=<payload> → $code (acceptable)"
      ;;
  esac
}

# Probe 3: GET /v1/me/orders sem Authorization deve responder 401.
probe_auth_bypass() {
  code=$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/me/orders")
  if [ "$code" = "401" ]; then
    probe_pass "auth-bypass: GET /v1/me/orders without token → 401"
  else
    probe_fail "auth-bypass: GET /v1/me/orders without token → $code (want 401)"
  fi
}

# Probe 4: DELETE /v1/plans (rota pública só aceita GET). Esperamos 405
# (Method Not Allowed) ou 401/404. NUNCA 200 — seria bug grave.
probe_method_tampering() {
  code=$(curl -s -o /dev/null -w '%{http_code}' \
    -X DELETE "$API/v1/plans")
  case "$code" in
    405|401|404)
      probe_pass "method-tampering: DELETE /v1/plans → $code (rejected)"
      ;;
    200|204)
      probe_fail "method-tampering: DELETE /v1/plans → $code (verb accepted!)"
      ;;
    *)
      probe_pass "method-tampering: DELETE /v1/plans → $code (non-success, OK)"
      ;;
  esac
}

# Probe 5: CORS — Origin de domínio random NÃO pode vir liberado em
# Access-Control-Allow-Origin. Backend só permite os origins
# whitelisted (viralefy.com, backoffice, localhost).
probe_cors() {
  local evil="https://attacker.example.com"
  # Usa preflight OPTIONS pra forçar CORS handler.
  hdrs=$(curl -s -i -X OPTIONS \
    -H "Origin: $evil" \
    -H 'Access-Control-Request-Method: GET' \
    "$API/v1/plans" | tr -d '\r')
  allow_origin=$(printf '%s\n' "$hdrs" | awk -F': ' 'tolower($1)=="access-control-allow-origin" {print $2; exit}')
  # Pass se: header ausente OU (não-eco do evil AND não-wildcard).
  # Fail se: header refletiu evil ou liberou wildcard a uma Origin de teste.
  if [ -z "$allow_origin" ] || { [ "$allow_origin" != "$evil" ] && [ "$allow_origin" != "*" ]; }; then
    probe_pass "cors: random Origin not echoed in Access-Control-Allow-Origin (got: ${allow_origin:-<absent>})"
  else
    probe_fail "cors: random Origin reflected → $allow_origin (CSRF/data-exfil risk)"
  fi
}

echo "=== Viralefy security probes against: $API ==="
echo

probe_rate_limit
probe_sql_injection
probe_auth_bypass
probe_method_tampering
probe_cors

echo
echo "=== Summary ==="
printf 'PASS: %d\n' "$PASS"
printf 'FAIL: %d\n' "$FAIL"
echo
for r in "${RESULTS[@]}"; do
  printf '  %s\n' "$r"
done

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
