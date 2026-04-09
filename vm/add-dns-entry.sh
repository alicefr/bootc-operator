#!/bin/bash

# Add a DNS entry to the cluster DNS server (node1)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

NODE_NAME="$1"
DNS_SERVER="${2:-node1}"

if [ -z "$NODE_NAME" ]; then
    echo "Usage: $0 <node-name> [dns-server-node]"
    echo "Example: $0 node2"
    exit 1
fi

CONTAINER_NAME="k8s-$DNS_SERVER"
NODE_IP=$(get_node_ip "$NODE_NAME")

echo "=== Adding DNS entry for $NODE_NAME ==="
echo "Node IP: $NODE_IP"
echo "DNS Server: $DNS_SERVER"

SSH_HOST=$(get_vm_ssh_endpoint "$DNS_SERVER")
if [ -z "$SSH_HOST" ]; then
    echo "Error: Could not get SSH endpoint for $DNS_SERVER"
    exit 1
fi

# Check if dnsmasq is installed on DNS server
DNSMASQ_INSTALLED=$(podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
    "rpm -q dnsmasq >/dev/null 2>&1 && echo 'yes' || echo 'no'")

if [ "$DNSMASQ_INSTALLED" != "yes" ]; then
    echo "⚠️  dnsmasq is not installed on $DNS_SERVER"
    echo "This should not happen - dnsmasq is installed via cloud-init on node1"
    echo "You may need to recreate the node with the current scripts"
    echo ""
    echo "Continuing anyway - adding to hosts file..."
fi

# Ensure dnsmasq directory and file exist
podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
    "sudo bash -c 'mkdir -p /var/lib/dnsmasq && touch /var/lib/dnsmasq/cluster-hosts'"

# Add entry to dnsmasq hosts file (avoiding duplicates)
podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
    "sudo bash -c \"grep -v '^[^#]*[[:space:]]$NODE_NAME[[:space:]]' /var/lib/dnsmasq/cluster-hosts > /tmp/cluster-hosts.tmp || true && echo '$NODE_IP $NODE_NAME $NODE_NAME.cluster.local' >> /tmp/cluster-hosts.tmp && mv /tmp/cluster-hosts.tmp /var/lib/dnsmasq/cluster-hosts && chown dnsmasq:dnsmasq /var/lib/dnsmasq/cluster-hosts && chmod 644 /var/lib/dnsmasq/cluster-hosts && restorecon /var/lib/dnsmasq/cluster-hosts && if systemctl is-active dnsmasq >/dev/null 2>&1; then systemctl restart dnsmasq; fi\""

if [ $? -eq 0 ]; then
    echo "✅ DNS entry added: $NODE_NAME -> $NODE_IP"

    # Flush systemd-resolved cache on DNS server
    echo "Flushing DNS cache on $DNS_SERVER..."
    podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
        "sudo resolvectl flush-caches"

    # Show current entries
    echo ""
    echo "Current DNS entries:"
    podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
        "sudo cat /var/lib/dnsmasq/cluster-hosts"
else
    echo "❌ Failed to add DNS entry"
    exit 1
fi
