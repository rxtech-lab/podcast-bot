#!/bin/bash

# macOS build script for CI.
# Builds the Debate Bot macOS app without code signing.

set -e
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT_PATH="$PROJECT_ROOT/iOS/iOS.xcodeproj"
SCHEME="${SCHEME:-iOS}"
CONFIGURATION="${CONFIGURATION:-Debug}"
DESTINATION="${DESTINATION:-platform=macOS}"
BUILD_DIR="${BUILD_DIR:-$PROJECT_ROOT/.build/macos}"
LOG_FILE="${LOG_FILE:-$PROJECT_ROOT/macos-build.log}"

if [[ ! -d "$PROJECT_PATH" ]]; then
  echo "Error: $PROJECT_PATH not found"
  exit 1
fi

# Match Xcode Cloud's CI setup: package plugin and macro trust prompts block
# non-interactive runners before xcodebuild can compile package macros.
defaults write com.apple.dt.Xcode IDESkipPackagePluginFingerprintValidatation -bool YES || true
defaults write com.apple.dt.Xcode IDESkipMacroFingerprintValidation -bool YES || true
defaults delete com.apple.dt.Xcode IDEPackageEnablePrebuilts || true

mkdir -p "$(dirname "$BUILD_DIR")"

echo "======================================"
echo "Debate Bot macOS Build Script"
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
