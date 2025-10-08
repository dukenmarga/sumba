#!/bin/bash

APP_NAME="sumba"
VERSION="v0.3.2"

# Define the target platforms and architectures
PLATFORMS=(
    "linux-amd64"
    "windows-amd64"
    "darwin-arm64"
    "freebsd-amd64"
)

# Create a 'build' directory if it doesn't exist
mkdir -p build

echo "Starting multi-platform Go build..."

for PLATFORM in "${PLATFORMS[@]}"; do
    # Extract GOOS and GOARCH from the platform string
    GOOS=${PLATFORM%%-*}
    GOARCH=${PLATFORM##*-}

    echo "Building for $GOOS/$GOARCH..."

    # Set environment variables for cross-compilation
    export GOOS
    export GOARCH

    # Determine the binary extension
    BIN_EXT=""
    if [[ "$GOOS" == "windows" ]]; then
        BIN_EXT=".exe"
    fi

    # Build the executable
    # CGO_ENABLED=0 is often used to avoid CGO dependencies in cross-compiled binaries
    CGO_ENABLED=0 go build -ldflags "-w -s" -o "build/$APP_NAME-$VERSION-$GOOS-$GOARCH$BIN_EXT" .

    if [ $? -ne 0 ]; then
        echo "Error building for $GOOS/$GOARCH. Aborting."
        exit 1
    fi

    # Create a tarball of the executable
    tar -czf "build/$APP_NAME-$VERSION-$GOOS-$GOARCH.tar.gz" "build/$APP_NAME-$VERSION-$GOOS-$GOARCH$BIN_EXT"

done

echo "Multi-platform build completed successfully. Binaries are in the 'build' directory."