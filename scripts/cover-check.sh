#!/usr/bin/env bash
# NFR-11: statement coverage excluding internal/testserver.
set -euo pipefail
profile=$1; min=$2
total=$(go tool cover -func="$profile" | awk '/^total:/ {sub("%","",$3); print $3}')
echo "coverage: ${total}% (minimum ${min}%)"
awk -v t="$total" -v m="$min" 'BEGIN { exit (t+0 < m+0) }'
