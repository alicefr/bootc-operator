#!/bin/bash

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

NODE_NAME="${1}"
CONTAINER_NAME="k8s-$NODE_NAME"

if [ -z "$NODE_NAME" ]; then
    echo "Usage: $0 <node-name>"
    exit 1
fi

# Check if container exists
if ! podman container exists "$CONTAINER_NAME" 2>/dev/null; then
    echo "Error: Container $CONTAINER_NAME does not exist"
    exit 1
fi

# Get SSH endpoint and port
SSH_HOST=$(get_vm_ssh_endpoint "$NODE_NAME")
SSH_PORT=$(get_vm_ssh_port)
CLUSTER_IP=$(get_node_ip "$NODE_NAME")

echo "Connecting to $NODE_NAME (SSH: $SSH_HOST:$SSH_PORT, cluster: $CLUSTER_IP) as user core"
podman exec -ti "$CONTAINER_NAME" ssh -p "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i /var/run/cluster/cluster.key "core@$SSH_HOST"
