#!/bin/bash

set -e

echo "=== Voice Assistant Quick Start ==="
echo ""

if ! command -v docker &> /dev/null; then
    echo "ERROR: Docker is not installed"
    exit 1
fi

if ! command -v docker-compose &> /dev/null; then
    echo "ERROR: Docker Compose is not installed"
    exit 1
fi

echo "Step 1: Building Docker image..."
docker-compose build

echo ""
echo "Step 2: Installing models..."
docker-compose run --rm voice-assistant ./install_models.sh

echo ""
echo "Step 3: Starting voice assistant..."
docker-compose up -d

echo ""
echo "Step 4: Checking status..."
sleep 2
docker-compose ps

echo ""
echo "=== Voice Assistant Started ==="
echo "View logs: docker-compose logs -f"
echo "Stop: docker-compose down"
