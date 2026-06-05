#!/bin/bash

~/go/bin/defradb client purge --force

go run ./cmd/build_schema | ~/go/bin/defradb client schema add -f -