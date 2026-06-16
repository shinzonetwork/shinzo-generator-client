#!/bin/bash

go run ./cmd/build_schema | ~/go/bin/defradb client schema add -f -
