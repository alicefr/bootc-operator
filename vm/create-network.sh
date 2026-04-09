#!/bin/bash

set -e

NETWORK_NAME="k8s-cluster"

echo "=== Creating Podman network for Kubernetes cluster ==="

# Check if network already exists
if podman network exists "$NETWORK_NAME" 2>/dev/null; then
    echo "✓ Network '$NETWORK_NAME' already exists"
    SUBNET=$(podman network inspect "$NETWORK_NAME" --format '{{range .Subnets}}{{.Subnet}}{{end}}')
    echo "  Subnet: $SUBNET"
else
    echo "Creating network '$NETWORK_NAME'..."
    # Try to create with preferred subnet, fall back to auto-assigned if conflict
    if ! podman network create "$NETWORK_NAME" --subnet 10.89.0.0/24 2>/dev/null; then
        echo "⚠ Subnet 10.89.0.0/24 already in use, letting Podman assign subnet..."
        podman network create "$NETWORK_NAME"
    fi
    SUBNET=$(podman network inspect "$NETWORK_NAME" --format '{{range .Subnets}}{{.Subnet}}{{end}}')
    echo "✓ Network '$NETWORK_NAME' created with subnet $SUBNET"
fi

echo ""
echo "✅ Network setup complete"
echo "All node containers will join this network for cluster communication"
