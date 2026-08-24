#!/usr/bin/env bash
# Removes the local development environment: both kind clusters and the
# supporting containers. Safe to run more than once.
set -u

echo "stopping clusterlens..."
pkill -f 'clusterlens -config' 2>/dev/null

echo "removing containers..."
docker rm -f clusterlens-dex clusterlens-redis 2>/dev/null

echo "deleting kind clusters..."
kind delete cluster --name lens-a 2>/dev/null
kind delete cluster --name lens-b 2>/dev/null

echo "done."
