#!/bin/bash
set -e

usage() {
    echo "Usage: $0 [-t TAG] [-p]"
    echo "  -t TAG   Image tag (default: latest)"
    echo "  -p       Push to registry after building"
    exit 1
}

IMAGE_NAME="ghcr.io/chelmertz/elly"
TAG="latest"
PUSH=false

while getopts "t:ph" opt; do
    case $opt in
        t) TAG="$OPTARG" ;;
        p) PUSH=true ;;
        h) usage ;;
        *) usage ;;
    esac
done

docker build -t "${IMAGE_NAME}:${TAG}" .

if [ "$PUSH" = true ]; then
    docker push "${IMAGE_NAME}:${TAG}"
fi
