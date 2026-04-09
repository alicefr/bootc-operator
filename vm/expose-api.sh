#!/bin/bash

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

NODE_NAME="${1:-node1}"
CONTAINER_NAME="k8s-$NODE_NAME"
KUBECONFIG_PATH="${2:-./vm/kubeconfig}"

echo "=== Exposing API server to localhost:6443 ==="

# Get VM's passt IP for SSH from container
SSH_HOST=$(get_vm_ssh_endpoint "$NODE_NAME")

if [ -z "$SSH_HOST" ]; then
    echo "Error: Could not get passt IP for VM $NODE_NAME"
    exit 1
fi

echo "SSH endpoint: $SSH_HOST"

# Check if container has port 6443 published
if ! podman port "$CONTAINER_NAME" 2>/dev/null | grep -q '6443'; then
    echo "❌ Container $CONTAINER_NAME does not have port 6443 published"
    echo "Recreate the container with: -p 6443:6443"
    echo "Or use init-cluster.sh which handles this automatically for node1"
    exit 1
fi

# Set up SSH tunnel inside container: container:6443 -> VM:6443
if podman exec "$CONTAINER_NAME" ss -tln 2>/dev/null | grep -q ':6443'; then
    echo "✓ Port 6443 is already being forwarded in container"
else
    echo "Starting SSH port forwarding inside container: 6443 -> VM:6443"

    podman exec -d "$CONTAINER_NAME" ssh -N -L 0.0.0.0:6443:localhost:6443 \
        -o StrictHostKeyChecking=no \
        -o UserKnownHostsFile=/dev/null \
        -o ServerAliveInterval=60 \
        -i /var/run/cluster/cluster.key \
        -p 2222 "core@$SSH_HOST"

    sleep 3
fi

# Verify the tunnel is working
if ! podman exec "$CONTAINER_NAME" ss -tln 2>/dev/null | grep -q ':6443'; then
    echo "❌ Failed to start SSH tunnel in container"
    exit 1
fi

echo "✅ API server exposed: localhost:6443 -> container:6443 -> VM:6443"
echo ""

# Generate kubeconfig with localhost:6443
echo "Generating kubeconfig at $KUBECONFIG_PATH..."
podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
    'cat ~/.kube/config' > "$KUBECONFIG_PATH"

# Replace server address with localhost
sed -i 's|server: https://.*:6443|server: https://localhost:6443|g' "$KUBECONFIG_PATH"

echo "✅ Kubeconfig generated at $KUBECONFIG_PATH"
echo ""
echo "Usage:"
echo "  export KUBECONFIG=$KUBECONFIG_PATH"
echo "  kubectl get nodes"
