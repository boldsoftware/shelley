#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
PATH="$(scripts/with-node):$PATH"

echo "==> Running Shelley CI Tests"
echo ""

echo "==> Installing UI dependencies..."
cd ui
pnpm install --frozen-lockfile
cd ..

echo ""
echo "==> Running TypeScript type check..."
cd ui
pnpm run type-check
cd ..

echo ""
echo "==> Running ESLint..."
cd ui
pnpm run lint
cd ..

echo ""
echo "==> Building UI..."
cd ui
pnpm run build
cd ..

echo ""
echo "==> Running Go tests..."
go test -v ./...

echo ""
echo "==> Running E2E tests (LazyCue; needs Chrome, Chromium or Headless Shell)..."
LAZYCUE_INTEGRATION=1 go test ./test/ -run 'TestNewPage|TestE2E' -count=1 -timeout 60m

echo ""
echo "==> All Shelley tests passed! ✓"
