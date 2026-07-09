#!/usr/bin/env bash
# Run this on WSL Ubuntu, from the repo root: bash build-for-gcp.sh
# Produces ./sawt-gateway — a linux/amd64 binary ready to scp to the e2-micro VM.
set -euo pipefail

command -v go >/dev/null || { echo "Go not found. Install it first (see docs/DEPLOYMENT.md)."; exit 1; }

echo "go vet ..."
go vet ./...

echo "building (CGO_ENABLED=0, linux/amd64) ..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o sawt-gateway .

file sawt-gateway || true
echo "Done: ./sawt-gateway"
echo "Next: scp sawt-gateway .env.production <vm>:~/  (see docs/DEPLOYMENT.md for the systemd install steps)"
