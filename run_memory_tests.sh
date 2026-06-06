#!/bin/bash
set -e
cd "$(dirname "$0")"
GOCACHE=$PWD/.gocache go test -count=1 -run . -coverprofile=memory.coverage -covermode=atomic ./internal/queue/memory/...
go tool cover -func=memory.coverage | grep -E '(total|memory)'
rm -f memory.coverage
