#!/bin/bash

set -e

echo "=== Voice Assistant Quick Start ==="
echo ""

if ! command -v docker &> /dev/null; then
    echo "ERROR: Docker is not installed"
    exit 1
fi

# Prefer Docker Compose v2 (plugin), fall back to the legacy v1 binary
if docker compose version &> /dev/null; then
    COMPOSE="docker compose"
elif command -v docker-compose &> /dev/null; then
    COMPOSE="docker-compose"
else
    echo "ERROR: Docker Compose is not installed"
    exit 1
fi

echo "Step 1: Building Docker image..."
$COMPOSE build

echo ""
echo "Step 2: Installing models..."
$COMPOSE run --rm voice-assistant /app/install_models.sh

echo ""
echo "Step 3: Starting voice assistant..."
$COMPOSE up -d

echo ""
echo "Step 4: Checking status..."
sleep 2
$COMPOSE ps

echo ""
echo "=== Voice Assistant Started ==="
echo "View logs: $COMPOSE logs -f"
echo "Stop: $COMPOSE down"
