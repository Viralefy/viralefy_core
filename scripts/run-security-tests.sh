#!/usr/bin/env bash
# run-security-tests.sh — orquestra a suite de segurança:
#   1) testes Go (TestSecurity_*) que exercitam middlewares + handlers
#      contra payloads OWASP top-10
#   2) probes HTTP contra o ambiente live (default api.viralefy.com)
#
# Use:
#   ./run-security-tests.sh                # roda tudo
#   SKIP_PROBES=1 ./run-security-tests.sh  # só Go tests (sem rede)
#   API=https://staging.viralefy.com ./run-security-tests.sh

set -u

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
API_DIR=$(cd "$SCRIPT_DIR/.." && pwd)

GO_OK=0
PROBES_OK=0

echo "=== [1/2] Go security tests ==="
if (cd "$API_DIR" && PATH="/usr/local/go/bin:$PATH" go test -run TestSecurity ./internal/interface/http/...); then
  echo "Go tests: PASS"
  GO_OK=1
else
  echo "Go tests: FAIL"
  GO_OK=0
fi

echo
if [ "${SKIP_PROBES:-0}" = "1" ]; then
  echo "=== [2/2] HTTP probes — SKIPPED (SKIP_PROBES=1) ==="
  PROBES_OK=1
else
  echo "=== [2/2] HTTP probes against ${API:-https://api.viralefy.com} ==="
  if bash "$SCRIPT_DIR/security-probes.sh"; then
    PROBES_OK=1
  else
    PROBES_OK=0
  fi
fi

echo
echo "=== Final summary ==="
if [ "$GO_OK" = "1" ]; then
  echo "  Go tests: PASS"
else
  echo "  Go tests: FAIL"
fi
if [ "$PROBES_OK" = "1" ]; then
  echo "  Probes:   PASS"
else
  echo "  Probes:   FAIL"
fi

if [ "$GO_OK" = "1" ] && [ "$PROBES_OK" = "1" ]; then
  exit 0
fi
exit 1
