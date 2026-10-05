#!/usr/bin/env bash
# TP-L4-02 / NFR-02: the library must not depend on Testcontainers, databases
# or frameworks.
#
# Checked are (1) the direct requirements in go.mod and (2) all packages that
# are actually compiled into the library and its tests. The full module graph
# is not checked: it also contains modules that carry dependencies only for
# their own optional subpackages (e.g. kin-openapi -> gorilla/mux for
# routers/gorillamux) and are never compiled.
set -euo pipefail
cd "$(dirname "$0")/.."
forbidden='testcontainers|jackc/pgx|lib/pq|go-sql-driver|redis|gin-gonic|go-chi|labstack/echo|gofiber|gorilla/mux'
status=0
if go list -m -f '{{if not .Indirect}}{{.Path}}{{end}}' all | grep -E "$forbidden"; then
  echo "forbidden direct dependency in go.mod" >&2; status=1
fi
if go list -deps -test ./... | grep -E "$forbidden"; then
  echo "forbidden dependency in the compiled packages" >&2; status=1
fi
[ "$status" -eq 0 ] && echo "deps: ok"
exit "$status"
