#!/bin/bash

# Decodes optional base64 secrets into files used by CI / Xcode Cloud.
# Safe to run locally when the variables are not set (it simply skips them).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

decode_to_file() {
  local value="$1"
  local output="$2"
  local label="$3"

  if [[ -z "$value" ]]; then
    echo "Skipping $label (env var not set)"
    return 0
  fi

  mkdir -p "$(dirname "$output")"
  printf "%s" "$value" | base64 --decode > "$output"
  echo "Wrote $label -> $output"
}

decode_to_file "${SECRETS_XCCONFIG_BASE64:-}" \
  "$REPO_ROOT/iOS/Config/Secrets.xcconfig" \
  "ios secrets config"
