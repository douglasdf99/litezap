#!/bin/sh
# Builds an unsigned LiteZap.app (a window with the dashboard, Dock icon and menu bar
# icon) from this checkout and, with DMG=1, a LiteZap-macos.dmg with the app and a shortcut to /Applications.
# It also builds LiteZap.mcpb, the Claude Desktop extension (scripts/mcpb: a small shim,
# cmd/litezap-mcpb, that finds the installed app and runs `litezap-app mcp`), for macOS
# and Windows: into $OUT/LiteZap.mcpb (release asset) and into the app's Resources (the
# "Instalar extensão" option of "Conectar a IA").
#
# Usage: cmd/litezap-app/package-macos.sh [out_dir]
#   VERSION=v0.1.0   stamps the version (default: dev)
#   UNIVERSAL=1      builds arm64 + amd64 and joins them with lipo (what releases ship)
#   DMG=1            also creates $OUT/LiteZap-macos.dmg with hdiutil
#
# Needs Go and the Xcode Command Line Tools (the window and the menu bar icon use Cocoa
# and WebKit through CGO).
# Not signed or notarized: on first open, right-click > Open (or allow it in
# System Settings > Privacy & Security).
set -eu
cd "$(dirname "$0")/../.."
OUT="${1:-dist}"
VERSION="${VERSION:-dev}"
APP="$OUT/LiteZap.app"
BIN="$APP/Contents/MacOS/litezap-app"
# The oldest macOS the Go toolchain still supports (Go 1.25+: macOS 12 Monterey).
MIN_MACOS=12.0
# CFBundleShortVersionString must be numbers: v0.1.0-rc.1 -> 0.1.0
PLIST_VERSION=$(printf '%s' "$VERSION" | sed -E 's/^v//; s/[^0-9.].*$//')
[ -n "$PLIST_VERSION" ] || PLIST_VERSION=0.0.0
# VERSION goes into XML (the plist): escape it.
XML_VERSION=$(printf '%s' "$VERSION" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g")

if [ "${UNIVERSAL:-0}" = 1 ]; then
	ARCHS="arm64 amd64"
else
	ARCHS="$(go env GOARCH)"
fi

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

SLICES=""
for arch in $ARCHS; do
	# cgo passes -arch to clang, so the other architecture cross-compiles on one Mac.
	CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" CGO_CFLAGS="-O2 -mmacosx-version-min=$MIN_MACOS" CGO_LDFLAGS="-mmacosx-version-min=$MIN_MACOS" \
		go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$TMP/litezap-app-$arch" ./cmd/litezap-app
	SLICES="$SLICES $TMP/litezap-app-$arch"
done
set -- $SLICES
if [ $# -gt 1 ]; then
	lipo -create -output "$BIN" "$@"
else
	cp "$1" "$BIN"
fi

# Claude Desktop extension: universal macOS shim + Windows shim (both CGO-free).
SHIMS=""
for arch in $ARCHS; do
	CGO_ENABLED=0 GOOS=darwin GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "$TMP/litezap-mcpb-$arch" ./cmd/litezap-mcpb
	SHIMS="$SHIMS $TMP/litezap-mcpb-$arch"
done
set -- $SHIMS
if [ $# -gt 1 ]; then
	lipo -create -output "$TMP/litezap-mcpb" "$@"
else
	cp "$1" "$TMP/litezap-mcpb"
fi
codesign --force --sign - "$TMP/litezap-mcpb"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "$TMP/litezap-mcpb.exe" ./cmd/litezap-mcpb
mkdir -p "$OUT"
go run ./scripts/mcpb -version "$VERSION" -darwin "$TMP/litezap-mcpb" -windows "$TMP/litezap-mcpb.exe" -out "$OUT/LiteZap.mcpb"
cp "$OUT/LiteZap.mcpb" "$APP/Contents/Resources/LiteZap.mcpb"

go run ./scripts/appicon "$TMP/LiteZap.iconset"
iconutil -c icns -o "$APP/Contents/Resources/LiteZap.icns" "$TMP/LiteZap.iconset"

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>LiteZap</string>
  <key>CFBundleDisplayName</key><string>LiteZap</string>
  <key>CFBundleIdentifier</key><string>dev.litezap.app</string>
  <key>CFBundleExecutable</key><string>litezap-app</string>
  <key>CFBundleIconFile</key><string>LiteZap</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$PLIST_VERSION</string>
  <key>CFBundleVersion</key><string>$PLIST_VERSION</string>
  <key>CFBundleGetInfoString</key><string>LiteZap $XML_VERSION</string>
  <key>LSMinimumSystemVersion</key><string>$MIN_MACOS</string>
  <key>LSApplicationCategoryType</key><string>public.app-category.business</string>
  <key>NSAppTransportSecurity</key>
  <dict>
    <!-- The window loads the dashboard from the engine on http://127.0.0.1. -->
    <key>NSAllowsLocalNetworking</key><true/>
  </dict>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
EOF
# Ad-hoc signature: Apple Silicon refuses to run unsigned arm64 code at all. It does not
# replace a Developer ID signature (Gatekeeper still warns on first open).
codesign --force --deep --sign - "$APP"
echo "built $APP ($ARCHS)"

if [ "${DMG:-0}" = 1 ]; then
	DMG_PATH="$OUT/LiteZap-macos.dmg"
	STAGE="$TMP/dmg"
	mkdir -p "$STAGE"
	cp -R "$APP" "$STAGE/"
	ln -s /Applications "$STAGE/Applications"
	rm -f "$DMG_PATH"
	hdiutil create -volname LiteZap -srcfolder "$STAGE" -fs HFS+ -format UDZO -ov "$DMG_PATH" >/dev/null
	echo "built $DMG_PATH"
fi
