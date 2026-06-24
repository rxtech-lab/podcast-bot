#!/bin/bash

# iOS build script for CI.
# Builds the Debate Bot iOS app for the iOS Simulator without code signing.

set -e
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT_PATH="$PROJECT_ROOT/iOS/iOS.xcodeproj"
SCHEME="${SCHEME:-iOS}"
CONFIGURATION="${CONFIGURATION:-Debug}"
BUILD_DIR="${BUILD_DIR:-$PROJECT_ROOT/.build/ios}"
LOG_FILE="${LOG_FILE:-$PROJECT_ROOT/build.log}"

if [[ ! -d "$PROJECT_PATH" ]]; then
  echo "Error: $PROJECT_PATH not found"
  exit 1
fi

# Build against a CONCRETE iOS simulator (not `generic/platform=iOS Simulator`)
# and do NOT pass `-sdk iphonesimulator`. Both force macro plugin targets
# (e.g. EquatableMacros, a host/macOS tool pulled in via SwiftStreamingMarkdown)
# to compile for the simulator SDK, which then can't find the macOS-only
# prebuilt SwiftSyntax module and fails the build. A concrete destination lets
# the toolchain build macros for the host while building the app for the sim.
if [[ -z "${DESTINATION:-}" ]]; then
  SIMULATOR_NAME=$(xcrun simctl list devices available --json \
    | jq -r '.devices | to_entries | .[] | select(.key | contains("iOS")) | .value[] | select(.isAvailable == true) | .name' \
    | head -1)
  if [[ -z "$SIMULATOR_NAME" ]]; then
    echo "Error: No available iOS simulator found. Install one via Xcode > Settings > Platforms."
    exit 1
  fi
  DESTINATION="platform=iOS Simulator,name=$SIMULATOR_NAME,OS=latest"
fi

# Match Xcode Cloud's CI setup: package plugin and macro trust prompts block
# non-interactive runners before xcodebuild can compile package macros.
defaults write com.apple.dt.Xcode IDESkipPackagePluginFingerprintValidatation -bool YES || true
defaults write com.apple.dt.Xcode IDESkipMacroFingerprintValidation -bool YES || true
defaults delete com.apple.dt.Xcode IDEPackageEnablePrebuilts || true

mkdir -p "$(dirname "$BUILD_DIR")"

echo "======================================"
echo "Debate Bot iOS Build Script"
echo "======================================"
echo "Project: $PROJECT_PATH"
echo "Scheme: $SCHEME"
echo "Configuration: $CONFIGURATION"
echo "Destination: $DESTINATION"
echo "Build directory: $BUILD_DIR"
echo "Log file: $LOG_FILE"
echo ""

set +e

xcodebuild build \
  -project "$PROJECT_PATH" \
  -scheme "$SCHEME" \
  -configuration "$CONFIGURATION" \
  -destination "$DESTINATION" \
  -derivedDataPath "$BUILD_DIR" \
  -skipPackagePluginValidation \
  -skipMacroValidation \
  CODE_SIGN_IDENTITY="" \
  CODE_SIGNING_REQUIRED=NO \
  CODE_SIGNING_ALLOWED=NO \
  2>&1 | tee "$LOG_FILE"
BUILD_EXIT_CODE=${PIPESTATUS[0]}

set -e

echo ""
echo "======================================"

if [[ $BUILD_EXIT_CODE -eq 0 ]]; then
  echo "Build succeeded"
  exit 0
fi

echo "Build failed"
exit "$BUILD_EXIT_CODE"
