#!/usr/bin/env bash
# Lists test-plan IDs from mydocs/plan.md that no test covers. mydocs/ is a
# local folder and not part of the repository.
# An ID counts as covered by a test named TestTPL1_10... (for TP-L1-10), or
# by an explicit "covers: TP-L3-01" in a test file, a script or the Makefile
# (for checks that run outside of go test, e.g. with a container).
set -euo pipefail
cd "$(dirname "$0")/.."
if [ ! -f mydocs/plan.md ]; then
  echo "trace: mydocs/plan.md not found (local document, not in the repository)"
  exit 0
fi
missing=0
for id in $(grep -oE 'TP-L[1-4]-[0-9]{2}' mydocs/plan.md | sort -u); do
  name=$(echo "$id" | sed -E 's/TP-(L[1-4])-([0-9]{2})/TestTP\1_\2/; s/-//g')
  if grep -rqE "func ${name}(_|\()" --include='*_test.go' . ; then
    continue
  fi
  if grep -rqE "covers:.*${id}\b" --include='*_test.go' --include='*.sh' --include=Makefile . ; then
    continue
  fi
  echo "missing: $id ($name)"; missing=$((missing+1))
done
echo "$missing TP IDs without a test"
