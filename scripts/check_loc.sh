#!/usr/bin/env bash
# Enforce per-file line limits: warn >= WARN_LINES, fail >= MAX_LINES.
# BASELINE files are grandfathered (ratchet): they fail only on growth.
set -euo pipefail

WARN_LINES="${WARN_LINES:-900}"
MAX_LINES="${MAX_LINES:-1000}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Grandfathered files: "path|line-count" (pinned via wc -l at install).
BASELINE=(
	"pkg/chains/evm/ethereum_client_test.go|1915"
	"pkg/server/health_test.go|1096"
	"pkg/defra/block_handler_defra_test.go|1023"
)

baseline_for() {
	local entry
	for entry in "${BASELINE[@]}"; do
		if [ "${entry%%|*}" = "$1" ]; then echo "${entry##*|}"; return; fi
	done
	echo ""
}

fail=0
while IFS= read -r file; do
	rel="${file#"$ROOT"/}"
	lines=$(wc -l < "$file" | tr -d ' ')
	base="$(baseline_for "$rel")"

	if [ -n "$base" ]; then
		# Ratchet: grandfathered files fail only if they grew.
		if [ "$lines" -gt "$base" ]; then
			echo "ERROR: $rel is $lines lines (baseline $base — it grew, split it)"
			fail=1
		elif [ "$lines" -ge "$WARN_LINES" ]; then
			echo "WARN: $rel is $lines lines (baseline $base — split me)"
		fi
	else
		if [ "$lines" -ge "$MAX_LINES" ]; then
			echo "ERROR: $rel is $lines lines (max $MAX_LINES)"
			fail=1
		elif [ "$lines" -ge "$WARN_LINES" ]; then
			echo "WARN: $rel is $lines lines (approaching max $MAX_LINES — split me)"
		fi
	fi
done < <(find "$ROOT" -name '*.go' -type f \
	-not -path '*/.defra/*' -not -path '*/vendor/*' -not -path '*/bin/*')

if [ "$fail" -eq 1 ]; then
	echo "check_loc: line-count limits exceeded (warn >= $WARN_LINES, max $MAX_LINES)"
	exit 1
fi
echo "check_loc: all Go files within limits (warn >= $WARN_LINES, max $MAX_LINES)"
