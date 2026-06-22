#!/bin/bash

# Xcode Cloud post-clone hook.
# Stamps the iOS app version from the git tag that triggered the build:
#   CI_TAG=v1.2.3 -> MARKETING_VERSION=1.2.3
# and, when present, uses Xcode Cloud's auto-incrementing CI_BUILD_NUMBER for
# CURRENT_PROJECT_VERSION.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
IOS_PROJECT_DIR="$REPO_ROOT/iOS"
IOS_PROJECT_FILE="$IOS_PROJECT_DIR/iOS.xcodeproj/project.pbxproj"

echo "== Xcode Cloud: ci_post_clone =="
echo "Repo root: $REPO_ROOT"

"$REPO_ROOT/scripts/sync-ios-app-icon.sh"

# Package plugin/macro validation can block CI before the build starts.
defaults write com.apple.dt.Xcode IDESkipPackagePluginFingerprintValidatation -bool YES || true
defaults write com.apple.dt.Xcode IDESkipMacroFingerprintValidation -bool YES || true

if [[ -n "${CI_TAG:-}" ]]; then
  VERSION="${CI_TAG#v}"

  if [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][A-Za-z0-9.]+)?$ ]]; then
    echo "Stamping iOS version from CI_TAG: $CI_TAG -> $VERSION"
    IOS_PROJECT_FILE="$IOS_PROJECT_FILE" "$REPO_ROOT/scripts/update-ios-version.sh" "$VERSION" "${CI_BUILD_NUMBER:-}"
  else
    echo "CI_TAG '$CI_TAG' is not a semver tag; skipping version stamping"
  fi
else
  echo "CI_TAG not set; skipping version stamping"
fi

if [[ -x "$REPO_ROOT/scripts/decode-env-secrets.sh" ]]; then
  "$REPO_ROOT/scripts/decode-env-secrets.sh"
else
  echo "scripts/decode-env-secrets.sh not found; skipping optional secret decoding"
fi

echo "ci_post_clone completed"
