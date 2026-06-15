#!/bin/bash
set -euo pipefail

~/go/bin/defradb client purge --force

while read -r f; do
  go run ./cmd/build_schema --file "$f" | ~/go/bin/defradb client schema add -f -
done < <(go run ./cmd/build_schema --list-files)
