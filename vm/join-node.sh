#!/bin/bash

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

NODE_NAME="${1}"
CONTROL_PLANE="${2:-node1}"

if [ -z "$NODE_NAME" ]; then
    echo "Usage: $0 <new-node-name> [control-plane-node]"
    echo "Example: $0 node2"
    echo "Example: $0 node3 node1"
    exit 1
fi

echo "=== Creating worker node $NODE_NAME ==="
echo ""

# Create the new node
./vm/create-node.sh -n "$NODE_NAME"

if [ $? -ne 0 ]; then
    echo "Error: Failed to create node $NODE_NAME"
    exit 1
fi

echo ""
echo "=== Generating join command from $CONTROL_PLANE ==="

# Get control plane SSH endpoint
CONTROL_PLANE_SSH=$(get_vm_ssh_endpoint "$CONTROL_PLANE")
if [ -z "$CONTROL_PLANE_SSH" ]; then
    echo "Error: Could not get SSH endpoint for control plane $CONTROL_PLANE"
    exit 1
fi

echo ""
echo "=== Adding DNS entry for $NODE_NAME ==="
./vm/add-dns-entry.sh "$NODE_NAME" "$CONTROL_PLANE"

# Generate a fresh join command from the control plane
JOIN_COMMAND=$(podman exec "k8s-$CONTROL_PLANE" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key "core@$CONTROL_PLANE_SSH" -p 2222 \
    'sudo kubeadm token create --print-join-command' 2>/dev/null)

if [ -z "$JOIN_COMMAND" ]; then
    echo "Error: Failed to generate join command from $CONTROL_PLANE"
    exit 1
fi

echo "Join command: $JOIN_COMMAND"

echo ""
echo "=== Waiting for $NODE_NAME to be ready ==="
# Get SSH endpoint for cloud-init status checks
NODE_SSH=$(get_vm_ssh_endpoint "$NODE_NAME")

echo "Waiting for cloud-init to complete on $NODE_NAME..."
for i in {1..60}; do
    STATUS=$(podman exec "k8s-$NODE_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i /var/run/cluster/cluster.key -p 2222 "core@$NODE_SSH" \
        "cloud-init status 2>/dev/null | head -1 | awk '{print \$2}'" || echo "waiting")

    # Accept "done" (done with or without warnings is OK)
    if [[ "$STATUS" == "done" ]]; then
        echo "✓ cloud-init completed on $NODE_NAME"
        break
    fi

    if [ $i -eq 60 ]; then
        echo "❌ Timeout waiting for cloud-init to complete on $NODE_NAME"
        echo "Status: $STATUS"
        echo "Full status:"
        podman exec "k8s-$NODE_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            -i /var/run/cluster/cluster.key -p 2222 "core@$NODE_SSH" \
            "cloud-init status --long"
        exit 1
    fi
    echo -n "."
    sleep 5
done
echo ""

echo ""
echo "=== Joining $NODE_NAME to the cluster ==="

# Reconfirm SSH endpoint
NODE_SSH=$(get_vm_ssh_endpoint "$NODE_NAME")
if [ -z "$NODE_SSH" ]; then
    echo "Error: Could not get SSH endpoint for node $NODE_NAME"
    exit 1
fi

# Execute the join command on the new node
podman exec "k8s-$NODE_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key "core@$NODE_SSH" -p 2222 \
    "sudo $JOIN_COMMAND"

if [ $? -eq 0 ]; then
    echo ""
    echo "✅ Node $NODE_NAME successfully joined the cluster!"
    echo ""
    echo "Verify with:"
    echo "  ./vm/ssh-vm.sh $CONTROL_PLANE"
    echo "  kubectl get nodes"
else
    echo ""
    echo "❌ Failed to join $NODE_NAME to the cluster"
    exit 1
fi
