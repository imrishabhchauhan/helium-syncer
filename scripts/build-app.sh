#!/bin/sh
# Builds "Helium Syncer.app" for macOS (Apple Silicon + Intel in one app).
# Needs Go and the Xcode command line tools (xcode-select --install).
# Output: bin/Helium Syncer.app (menu-bar app) with the command-line tool
# inside it at Contents/MacOS/helium-syncer.
set -eu
cd "$(dirname "$0")/.."

VERSION="${VERSION:-0.1.0-dev}"
# Go 1.26 still makes programs for macOS 12 Monterey; Go 1.27 needs macOS 13.
# Go downloads this exact version by itself the first time.
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.10}"
APP="bin/Helium Syncer.app"
MACOS="$APP/Contents/MacOS"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Only ever replace our own build output.
case "$APP" in bin/*.app) rm -rf "$APP" ;; esac
mkdir -p "$MACOS" "$APP/Contents/Resources"

for arch in arm64 amd64; do
	echo "Building for $arch…"
	CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$TMP/app-$arch" ./cmd/helium-syncer-app
	CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$TMP/cli-$arch" ./cmd/helium-syncer
done
lipo -create -output "$MACOS/helium-syncer-app" "$TMP/app-arm64" "$TMP/app-amd64"
lipo -create -output "$MACOS/helium-syncer" "$TMP/cli-arm64" "$TMP/cli-amd64"

cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Helium Syncer</string>
  <key>CFBundleDisplayName</key><string>Helium Syncer</string>
  <key>CFBundleIdentifier</key><string>com.aiwithrishabh.helium-syncer</string>
  <key>CFBundleExecutable</key><string>helium-syncer-app</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSHumanReadableCopyright</key><string>MIT licence. Unofficial, not affiliated with Helium or imput.</string>
</dict>
</plist>
EOF

# Ad-hoc signature so macOS runs it on this Mac. Public releases will be
# signed and notarised separately.
codesign --force --deep --sign - "$APP"
echo "Built $APP"
lipo -archs "$MACOS/helium-syncer-app"
