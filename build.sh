#!/bin/bash
set -e

NAME="tpeek"
mkdir -p build

# Each line: GOOS GOARCH output-suffix
TARGETS="
linux amd64 linux-amd64
linux arm64 linux-arm64
darwin amd64 macos-amd64
darwin arm64 macos-arm64
windows amd64 windows-amd64.exe
"

echo "$TARGETS" | while read -r goos goarch suffix; do
    [ -z "$goos" ] && continue
    echo "Building ${NAME}-${suffix}..."
    GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -ldflags="-s -w" -o "build/${NAME}-${suffix}"
done

echo "Done. Binaries are in the 'build' folder."