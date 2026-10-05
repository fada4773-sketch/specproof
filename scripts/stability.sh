#!/usr/bin/env bash
# covers: TP-L3-03 TP-L3-04
# Runs the Petstore test n times, each time with a fresh
# container, and checks that results and reports do not change.
#   - results: name and status of every case from TestPetstore.json
#   - report:  TestPetstore.md without time values and the base URL
set -uo pipefail
n=${1:-20}
cd "$(dirname "$0")/../examples/petstore"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for i in $(seq 1 "$n"); do
  go test -tags=integration -count=1 -run '^TestPetstore$' ./... > "$work/out-$i.txt" 2>&1
  json=apitest-report/TestPetstore.json
  if [ ! -f "$json" ]; then
    echo "run $i: no report; is Docker/Podman running?" >&2
    cat "$work/out-$i.txt" >&2
    exit 1
  fi
  grep -E '^[[:space:]]+"(name|status)":' "$json" | paste - - > "$work/results-$i.txt"
  grep -vE '^\| (Start|Duration|Base URL) \|' apitest-report/TestPetstore.md > "$work/report-$i.md"
  echo "run $i: $(grep -c . "$work/results-$i.txt") cases"
done

status=0
for i in $(seq 2 "$n"); do
  if ! diff -u "$work/results-1.txt" "$work/results-$i.txt"; then
    echo "run $i: results differ from run 1 (TP-L3-03)" >&2
    status=1
  fi
  if ! diff -q "$work/report-1.md" "$work/report-$i.md" >/dev/null; then
    echo "run $i: report differs from run 1 (TP-L3-04):" >&2
    diff -u "$work/report-1.md" "$work/report-$i.md" | head -40 >&2
    status=1
  fi
done
[ "$status" -eq 0 ] && echo "stability: $n runs with identical results and reports"
exit "$status"
