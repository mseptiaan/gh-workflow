#!/bin/bash

# Build script for gh-workflow
# This script builds binaries for multiple OS/architecture combinations

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Project info
PROJECT_NAME="gh-workflow"
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "dev")}
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
COMMIT_HASH=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

# Build directory
BUILD_DIR="build"
DIST_DIR="dist"

# Supported platforms
PLATFORMS=(
    "linux/amd64"
    "linux/arm64"
    "linux/386"
    "darwin/amd64"
    "darwin/arm64"
    "windows/amd64"
    "windows/arm64"
    "windows/386"
)

# Build flags
LDFLAGS="-X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME} -X main.CommitHash=${COMMIT_HASH} -s -w"

echo -e "${BLUE}🔨 Building ${PROJECT_NAME} v${VERSION}${NC}"
echo -e "${BLUE}📦 Commit: ${COMMIT_HASH}${NC}"
echo -e "${BLUE}🕐 Build Time: ${BUILD_TIME}${NC}"
echo ""

# Clean previous builds
echo -e "${YELLOW}🧹 Cleaning previous builds...${NC}"
rm -rf "${BUILD_DIR}" "${DIST_DIR}"
mkdir -p "${BUILD_DIR}" "${DIST_DIR}"

# Build for each platform
for platform in "${PLATFORMS[@]}"; do
    IFS='/' read -r -a platform_split <<< "$platform"
    GOOS="${platform_split[0]}"
    GOARCH="${platform_split[1]}"
    
    # Set binary name and extension
    BINARY_NAME="${PROJECT_NAME}-${GOOS}-${GOARCH}"
    if [ "$GOOS" = "windows" ]; then
        BINARY_NAME="${BINARY_NAME}.exe"
    fi
    
    OUTPUT_PATH="${BUILD_DIR}/${BINARY_NAME}"
    
    echo -e "${BLUE}🏗️  Building for ${GOOS}/${GOARCH}...${NC}"
    
    # Build the binary
    if env GOOS="$GOOS" GOARCH="$GOARCH" go build -ldflags "$LDFLAGS" -o "$OUTPUT_PATH" .; then
        # Get file size
        if [ "$GOOS" = "darwin" ] || [ "$GOOS" = "linux" ]; then
            SIZE=$(du -h "$OUTPUT_PATH" | cut -f1)
        else
            SIZE=$(stat -c%s "$OUTPUT_PATH" 2>/dev/null | numfmt --to=iec || echo "unknown")
        fi
        
        echo -e "${GREEN}✅ Built ${BINARY_NAME} (${SIZE})${NC}"
        
        # Copy to dist directory for release
        cp "$OUTPUT_PATH" "${DIST_DIR}/${BINARY_NAME}"
    else
        echo -e "${RED}❌ Failed to build for ${GOOS}/${GOARCH}${NC}"
        exit 1
    fi
done

echo ""
echo -e "${GREEN}🎉 All builds completed successfully!${NC}"
echo -e "${YELLOW}📂 Binaries are in the '${BUILD_DIR}' directory${NC}"
echo -e "${YELLOW}📦 Release binaries are in the '${DIST_DIR}' directory${NC}"
echo ""

# List all built binaries
echo -e "${BLUE}📋 Built binaries:${NC}"
for file in "${BUILD_DIR}"/*; do
    if [ -f "$file" ]; then
        if [ "$(uname)" = "Darwin" ] || [ "$(uname)" = "Linux" ]; then
            SIZE=$(du -h "$file" | cut -f1)
        else
            SIZE=$(stat -c%s "$file" 2>/dev/null | numfmt --to=iec || echo "unknown")
        fi
        echo -e "  📄 $(basename "$file") (${SIZE})"
    fi
done

echo ""
echo -e "${BLUE}💡 To test a binary:${NC}"
echo -e "   ${BUILD_DIR}/${PROJECT_NAME}-linux-amd64 --help"
echo ""
echo -e "${BLUE}💡 To create a GitHub release:${NC}"
echo -e "   gh release create v${VERSION} ${DIST_DIR}/* --title \"Release v${VERSION}\" --notes \"Release notes here\""
echo ""
echo -e "${BLUE}💡 To upload to existing release:${NC}"
echo -e "   gh release upload v${VERSION} ${DIST_DIR}/*" 