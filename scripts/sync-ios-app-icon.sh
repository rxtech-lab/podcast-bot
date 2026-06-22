#!/bin/bash

# Keep the compiled AppIcon catalog in sync with the Icon Composer source.
# Xcode Cloud's actool currently crashes when compiling .icon bundles directly,
# so the .icon remains the editable source while AppIcon.appiconset is the build input.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ICON_SOURCE="$PROJECT_ROOT/iOS/icon.icon/Assets/image.png"
APP_ICON_DIR="$PROJECT_ROOT/iOS/iOS/Assets.xcassets/AppIcon.appiconset"
APP_ICON_IMAGE="$APP_ICON_DIR/image.png"
APP_ICON_CONTENTS="$APP_ICON_DIR/Contents.json"

if [[ ! -f "$ICON_SOURCE" ]]; then
  echo "Icon Composer source image not found: $ICON_SOURCE" >&2
  exit 1
fi

mkdir -p "$APP_ICON_DIR"
cp "$ICON_SOURCE" "$APP_ICON_IMAGE"

cat > "$APP_ICON_CONTENTS" <<'JSON'
{
  "images" : [
    {
      "filename" : "image.png",
      "idiom" : "universal",
      "platform" : "ios",
      "size" : "1024x1024"
    }
  ],
  "info" : {
    "author" : "xcode",
    "version" : 1
  }
}
JSON

# App Store rejects icons with an alpha channel (error 90717). Fail loudly here
# rather than during "Prepare Build for App Store Connect".
if sips -g hasAlpha "$APP_ICON_IMAGE" | grep -q "hasAlpha: yes"; then
  echo "Error: $APP_ICON_IMAGE has an alpha channel; App Store icons must be opaque." >&2
  echo "Flatten the source ($ICON_SOURCE) onto an opaque background and re-run." >&2
  exit 1
fi

echo "Synced AppIcon.appiconset from icon.icon"
