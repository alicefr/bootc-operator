#!/bin/bash

# Common library functions for VM management

# Get node cluster IP address by name (for Kubernetes communication)
# Usage: get_node_ip <node_name>
# Returns: Cluster network IP (10.0.0.x on enp2s0/multicast interface)
get_node_ip() {
    local NODE_NAME="$1"

    if [ -z "$NODE_NAME" ]; then
        echo "Error: Node name is required" >&2
        return 1
    fi

    # Calculate deterministic cluster IP from node name (same logic as create-node.sh)
    local IP_SUFFIX=$((16#$(echo -n "$NODE_NAME" | md5sum | cut -c1-2) % 240 + 10))
    local CLUSTER_IP="10.0.0.$IP_SUFFIX"

    echo "$CLUSTER_IP"
}

# Get VM's SSH endpoint from container (using port forwarding)
# Usage: get_vm_ssh_endpoint <node_name>
# Returns: "localhost:2222" (container port forwarded to VM port 22)
get_vm_ssh_endpoint() {
    local NODE_NAME="$1"

    if [ -z "$NODE_NAME" ]; then
        echo "Error: Node name is required" >&2
        return 1
    fi

    # SSH is accessible via port forwarding: container localhost:2222 -> VM:22
    echo "localhost"
}

# Get SSH port for VM access from container
get_vm_ssh_port() {
    echo "2222"
}
