#!/bin/sh
set -eu
npm ci
npm run build
go test ./...
go vet ./...
mkdir -p dist/bin
CGO_ENABLED=0 GOOS=linux go build -trimpath -o dist/bin/server ./cmd/server

python3 scripts/check.py
python3 scripts/package.py
