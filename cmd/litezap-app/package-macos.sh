#!/bin/sh
# Builds an unsigned LiteZap.app (menu bar only, no Dock icon) from this checkout.
# Usage: cmd/litezap-app/package-macos.sh [out_dir]   (VERSION=v0.1.0 to stamp a version)
# Needs Go and the Xcode Command Line Tools (the tray uses Cocoa through CGO).
# Not signed or notarized: on first open, right-click > Open (or allow it in
# System Settings > Privacy & Security).
set -eu
cd "$(dirname "$0")/../.."
OUT="${1:-dist}"
VERSION="${VERSION:-dev}"
APP="$OUT/LiteZap.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$APP/Contents/MacOS/litezap-app" ./cmd/litezap-app
cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>LiteZap</string>
  <key>CFBundleDisplayName</key><string>LiteZap</string>
  <key>CFBundleIdentifier</key><string>dev.litezap.app</string>
  <key>CFBundleExecutable</key><string>litezap-app</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
EOF
echo "built $APP"
