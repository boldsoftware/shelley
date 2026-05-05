#!/bin/bash
set -euo pipefail

ENV_FILE="$(dirname "$0")/gemini-api-key.env"

if [ ! -f "$ENV_FILE" ]; then
  echo "Error: $ENV_FILE not found" >&2
  exit 1
fi

set -a
source "$ENV_FILE"
set +a

exec /usr/local/go/bin/go test -v -run TestGemini "$@" shelley.exe.dev/llm/gem
