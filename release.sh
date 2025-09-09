#!/bin/bash

# Release script for gh-workflow
# This script helps create and upload GitHub releases

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
PROJECT_NAME="gh-workflow"
DIST_DIR="dist"
BUILD_SCRIPT="./build.sh"

# Functions
usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -v, --version VERSION    Release version (required)"
    echo "  -t, --title TITLE       Release title (optional)"
    echo "  -n, --notes NOTES       Release notes (optional)"
    echo "  -d, --draft             Create as draft release"
    echo "  -p, --prerelease        Create as prerelease"
    echo "  -b, --build             Build binaries before release"
    echo "  -u, --upload-only       Only upload to existing release"
    echo "  -h, --help              Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0 -v v1.0.0 -t \"Release v1.0.0\" -n \"Initial release\""
    echo "  $0 -v v1.0.1 -b -d"
    echo "  $0 -v v1.0.0 -u"
}

check_requirements() {
    if ! command -v gh &> /dev/null; then
        echo -e "${RED}❌ GitHub CLI (gh) is required but not installed${NC}"
        echo "Install it from: https://cli.github.com/"
        exit 1
    fi
    
    if ! command -v go &> /dev/null; then
        echo -e "${RED}❌ Go is required but not installed${NC}"
        exit 1
    fi
    
    if [ ! -f "$BUILD_SCRIPT" ]; then
        echo -e "${RED}❌ Build script not found: $BUILD_SCRIPT${NC}"
        exit 1
    fi
}

build_binaries() {
    echo -e "${BLUE}🔨 Building binaries...${NC}"
    if [ -x "$BUILD_SCRIPT" ]; then
        $BUILD_SCRIPT
    else
        echo -e "${RED}❌ Build script is not executable: $BUILD_SCRIPT${NC}"
        exit 1
    fi
}

create_release() {
    local version="$1"
    local title="$2"
    local notes="$3"
    local draft="$4"
    local prerelease="$5"
    
    echo -e "${BLUE}📦 Creating GitHub release: ${version}${NC}"
    
    # Build gh release create command
    local cmd="gh release create \"$version\""
    
    if [ -n "$title" ]; then
        cmd="$cmd --title \"$title\""
    fi
    
    if [ -n "$notes" ]; then
        cmd="$cmd --notes \"$notes\""
    else
        cmd="$cmd --generate-notes"
    fi
    
    if [ "$draft" = true ]; then
        cmd="$cmd --draft"
    fi
    
    if [ "$prerelease" = true ]; then
        cmd="$cmd --prerelease"
    fi
    
    # Add binaries if they exist
    if [ -d "$DIST_DIR" ] && [ "$(ls -A "$DIST_DIR")" ]; then
        cmd="$cmd \"$DIST_DIR\"/*"
    fi
    
    echo -e "${YELLOW}🚀 Executing: $cmd${NC}"
    eval $cmd
}

upload_to_release() {
    local version="$1"
    
    echo -e "${BLUE}📤 Uploading binaries to existing release: ${version}${NC}"
    
    if [ ! -d "$DIST_DIR" ] || [ ! "$(ls -A "$DIST_DIR")" ]; then
        echo -e "${RED}❌ No binaries found in $DIST_DIR${NC}"
        echo "Run with -b flag to build binaries first"
        exit 1
    fi
    
    gh release upload "$version" "$DIST_DIR"/*
}

# Parse command line arguments
VERSION=""
TITLE=""
NOTES=""
DRAFT=false
PRERELEASE=false
BUILD=false
UPLOAD_ONLY=false

while [[ $# -gt 0 ]]; do
    case $1 in
        -v|--version)
            VERSION="$2"
            shift 2
            ;;
        -t|--title)
            TITLE="$2"
            shift 2
            ;;
        -n|--notes)
            NOTES="$2"
            shift 2
            ;;
        -d|--draft)
            DRAFT=true
            shift
            ;;
        -p|--prerelease)
            PRERELEASE=true
            shift
            ;;
        -b|--build)
            BUILD=true
            shift
            ;;
        -u|--upload-only)
            UPLOAD_ONLY=true
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo -e "${RED}❌ Unknown option: $1${NC}"
            usage
            exit 1
            ;;
    esac
done

# Validate required arguments
if [ -z "$VERSION" ]; then
    echo -e "${RED}❌ Version is required${NC}"
    usage
    exit 1
fi

# Check requirements
check_requirements

# Build binaries if requested
if [ "$BUILD" = true ]; then
    build_binaries
fi

# Create or upload release
if [ "$UPLOAD_ONLY" = true ]; then
    upload_to_release "$VERSION"
else
    create_release "$VERSION" "$TITLE" "$NOTES" "$DRAFT" "$PRERELEASE"
fi

echo -e "${GREEN}🎉 Release process completed successfully!${NC}"
echo -e "${BLUE}🔗 View release: https://github.com/$(gh repo view --json owner,name -q '.owner.login + "/" + .name')/releases/tag/${VERSION}${NC}" 