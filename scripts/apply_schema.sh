#!/bin/bash
set -euo pipefail

while read -r f; do
  go run ./cmd/build_schema --file "$f" | ~/go/bin/defradb client schema add -f - 2>&1 \
    | grep -v "collection already exists" || true
done < <(go run ./cmd/build_schema --list-files)
