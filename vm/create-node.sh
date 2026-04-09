#!/bin/bash

set -xe

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"
NODE_NAME="node1"
PUBLISH_API=false

while getopts "n:p" opt; do
  case $opt in
    n)
      NODE_NAME="$OPTARG"
      ;;
    p)
      PUBLISH_API=true
      ;;
    \?)
      echo "Usage: $0 [-n node_name] [-p] [base_disk] [memory_mb] [vcpus]" >&2
      echo "  -n: Node name (default: node1)" >&2
      echo "  -p: Publish API server port 6443 to host (for control plane)" >&2
      exit 1
      ;;
  esac
done

shift $((OPTIND-1))

BASE_DISK="${1:-/src/fedora-bootc-k8s.qcow2}"
MEMORY="${2:-8192}"
VCPUS="${3:-4}"

CONTAINER_NAME="k8s-$NODE_NAME"
NETWORK_NAME="k8s-cluster"
CLUSTER_IMAGE="localhost/cluster:latest"

echo "=== Creating Kubernetes node container ==="
echo "Node name: $NODE_NAME"
echo "Container name: $CONTAINER_NAME"
echo "Base disk: $BASE_DISK"
echo "Memory: ${MEMORY}MB"
echo "VCPUs: $VCPUS"

# Check if network exists
if ! podman network exists "$NETWORK_NAME" 2>/dev/null; then
    echo "Error: Network '$NETWORK_NAME' does not exist"
    echo "Run: ./vm/create-network.sh first"
    exit 1
fi

# Check if container already exists
if podman container exists "$CONTAINER_NAME" 2>/dev/null; then
    echo "Error: Container '$CONTAINER_NAME' already exists"
    echo "Remove it first: podman rm -f $CONTAINER_NAME"
    exit 1
fi

echo ""
echo "Creating container for $NODE_NAME..."

# Ensure directories exist
mkdir -p "./vm/images"
mkdir -p "./vm/$NODE_NAME-keys"

# Get absolute paths for volumes
IMAGES_DIR="$(cd ./vm/images && pwd)"
KEYS_DIR="$(cd ./vm/$NODE_NAME-keys && pwd)"

# Build podman run command
PODMAN_ARGS=(
    run -d
    --name "$CONTAINER_NAME"
    --network "$NETWORK_NAME"
    --device /dev/kvm
    --device /dev/fuse
    -v "$IMAGES_DIR:/src:z"
    -v "$KEYS_DIR:/var/run/cluster:Z"
)

# Add port publishing for control plane nodes
if [ "$PUBLISH_API" = true ]; then
    PODMAN_ARGS+=(-p 6443:6443)
    echo "Publishing API server port 6443 to localhost"
fi

PODMAN_ARGS+=("$CLUSTER_IMAGE")

# Create container with KVM access and connect to cluster network
podman "${PODMAN_ARGS[@]}"

echo "✓ Container $CONTAINER_NAME created and joined network $NETWORK_NAME"

# Get container IP from Podman
CONTAINER_IP=$(podman inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$CONTAINER_NAME")
echo "✓ Container IP: $CONTAINER_IP (VM will inherit this via passt)"

echo ""
echo "Setting up VM inside container..."

OVERLAY_DISK="/src/${NODE_NAME}.qcow2"
ISO_PATH="/src/${NODE_NAME}-cloud-init.iso"

# Create overlay image with base as backing file
echo "Creating overlay image..."
podman exec "$CONTAINER_NAME" \
    qemu-img create \
    -f qcow2 \
    -F qcow2 \
    -b "$BASE_DISK" \
    "$OVERLAY_DISK"

echo ""
echo "Setting up SSH key..."

CLUSTER_KEY="/var/run/cluster/cluster.key"
CLUSTER_KEY_PUB="/var/run/cluster/cluster.key.pub"

# Check if cluster key exists
if ! podman exec "$CONTAINER_NAME" test -f "$CLUSTER_KEY"; then
    echo "Generating cluster SSH key..."
    podman exec "$CONTAINER_NAME" mkdir -p /var/run/cluster
    podman exec "$CONTAINER_NAME" ssh-keygen -t rsa -b 4096 -f "$CLUSTER_KEY" -N "" -C "cluster-key"
    echo "✓ Cluster SSH key created at $CLUSTER_KEY"
else
    echo "✓ Using existing cluster SSH key"
fi

echo ""
echo "Calculating cluster IP for $NODE_NAME..."
# Generate deterministic IP from node name hash (10.0.0.10-250)
IP_SUFFIX=$((16#$(echo -n "$NODE_NAME" | md5sum | cut -c1-2) % 240 + 10))
CLUSTER_IP="10.0.0.$IP_SUFFIX"
echo "Cluster IP: $CLUSTER_IP"

echo ""
echo "Creating cloud-init ISO with SSH key..."

# Get the public key
PUB_KEY=$(podman exec "$CONTAINER_NAME" cat "$CLUSTER_KEY_PUB")

# Create cloud-init files on host, then copy to container
TEMP_CI_DIR=$(mktemp -d)

# Create meta-data
cat > "$TEMP_CI_DIR/meta-data" << EOF
instance-id: $NODE_NAME
local-hostname: $NODE_NAME
EOF

# Calculate node1's IP for DNS (unless this IS node1)
NODE1_IP=$(get_node_ip "node1")

# Create network-config (separate file required by NoCloud)
cat > "$TEMP_CI_DIR/network-config" << EOF
version: 2
ethernets:
  enp1s0:
    dhcp4: true
  enp2s0:
    dhcp4: false
    dhcp6: false
    addresses:
      - $CLUSTER_IP/24
    nameservers:
      search: [cluster.local]
      addresses: [$NODE1_IP, 8.8.8.8]
    optional: true
EOF

# Conditional dnsmasq configuration for node1
if [ "$NODE_NAME" = "node1" ]; then
  DNSMASQ_FILES='  - path: /etc/dnsmasq.d/cluster.conf
    content: |
      # Listen only on cluster network interface
      interface=enp2s0
      bind-interfaces

      # Don'"'"'t read /etc/hosts
      no-hosts

      # Read additional hosts from this file
      addn-hosts=/var/lib/dnsmasq/cluster-hosts

      # Domain for cluster
      domain=cluster.local

      # Don'"'"'t forward short names
      domain-needed

      # Don'"'"'t forward addresses in private ranges
      bogus-priv

      # Use these upstream DNS servers for external queries
      server=8.8.8.8
      server=8.8.4.4

      # Cache size
      cache-size=1000
  - path: /var/lib/dnsmasq/cluster-hosts
    owner: dnsmasq:dnsmasq
    permissions: '"'"'0644'"'"'
    content: |
      # Cluster DNS entries (managed by add-dns-entry.sh)
      '"$CLUSTER_IP $NODE_NAME $NODE_NAME.cluster.local"'
'
  DNSMASQ_RUNCMD='  - chown dnsmasq:dnsmasq /var/lib/dnsmasq/cluster-hosts
  - restorecon -v /var/lib/dnsmasq/cluster-hosts
  - systemctl enable --now dnsmasq'
else
  DNSMASQ_FILES=''
  DNSMASQ_RUNCMD=''
fi

# Create unified user-data with conditional dnsmasq sections
cat > "$TEMP_CI_DIR/user-data" << EOF
#cloud-config
hostname: $NODE_NAME
users:
  - name: core
    ssh_authorized_keys:
      - $PUB_KEY
    sudo: ALL=(ALL) NOPASSWD:ALL
    groups: wheel
    shell: /bin/bash

write_files:
  - path: /etc/kubernetes/kubelet-config.yaml
    content: |
      apiVersion: kubelet.config.k8s.io/v1beta1
      kind: KubeletConfiguration
      volumePluginDir: /var/lib/kubelet/volumeplugins
  - path: /etc/sysconfig/kubelet
    content: |
      KUBELET_EXTRA_ARGS=--volume-plugin-dir=/var/lib/kubelet/volumeplugins
$DNSMASQ_FILES
runcmd:
  - swapoff -a
  - sed -i '/swap/d' /etc/fstab
  - modprobe br_netfilter
  - echo 'br_netfilter' > /etc/modules-load.d/k8s-bridge.conf
  - sysctl -w net.ipv4.ip_forward=1
  - echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-kubernetes.conf
  - mkdir -p /var/lib/kubelet/volumeplugins
  - systemctl enable --now qemu-guest-agent
  - nmcli connection modify "cloud-init enp2s0" ipv4.dns-search "~cluster.local cluster.local"
  - nmcli connection up "cloud-init enp2s0"
$DNSMASQ_RUNCMD
  - systemctl enable --now crio
  - systemctl enable kubelet
EOF

# Copy files to container and create ISO
podman cp "$TEMP_CI_DIR/meta-data" "$CONTAINER_NAME:/tmp/meta-data"
podman cp "$TEMP_CI_DIR/user-data" "$CONTAINER_NAME:/tmp/user-data"
podman cp "$TEMP_CI_DIR/network-config" "$CONTAINER_NAME:/tmp/network-config"

podman exec "$CONTAINER_NAME" genisoimage -output "$ISO_PATH" \
    -volid cidata \
    -joliet \
    -rock \
    /tmp/user-data \
    /tmp/meta-data \
    /tmp/network-config 2>&1 | grep -v 'Warning: creating filesystem' || true

# Cleanup
rm -rf "$TEMP_CI_DIR"
podman exec "$CONTAINER_NAME" rm -f /tmp/meta-data /tmp/user-data /tmp/network-config

echo "✓ Cloud-init ISO created at $ISO_PATH"

echo ""
# Generate MAC address for cluster network
MAC_SUFFIX=$(echo -n "$NODE_NAME" | md5sum | cut -c1-6)
CLUSTER_MAC="52:54:01:${MAC_SUFFIX:0:2}:${MAC_SUFFIX:2:2}:${MAC_SUFFIX:4:2}"

# Multicast address and port for cluster network
MCAST_ADDR="230.0.0.1"
MCAST_PORT="5558"

echo ""
echo "Creating and starting VM with dual-NIC (passt + multicast)..."

# Create and start VM with --xml injection for multicast config
podman exec "$CONTAINER_NAME" virt-install \
  --connect qemu:///session \
  --name "$NODE_NAME" \
  --memory "$MEMORY" \
  --vcpus "$VCPUS" \
  --disk path="$OVERLAY_DISK",format=qcow2,bus=virtio \
  --disk path="$ISO_PATH",device=cdrom \
  --import \
  --os-variant fedora-unknown \
  --network passt,model=virtio,portForward=2222:22 \
  --network mcast,model=virtio,mac="$CLUSTER_MAC" \
  --xml xpath.set=./devices/interface[2]/source/@address="$MCAST_ADDR" \
  --xml xpath.set=./devices/interface[2]/source/@port="$MCAST_PORT" \
  --channel unix,target.type=virtio,target.name=org.qemu.guest_agent.0 \
  --graphics none \
  --console pty,target_type=serial \
  --noautoconsole

echo "✓ VM $NODE_NAME created with dual-NIC networking (passt:2222→22, multicast:$MCAST_ADDR:$MCAST_PORT)"

echo ""
echo "Waiting for VM to boot and cloud-init to complete..."
sleep 15

echo ""
echo "✅ Node '$NODE_NAME' created successfully!"
echo "   Container: $CONTAINER_NAME"
echo "   NIC 1 (enp1s0): passt - internet access"
echo "   NIC 2 (enp2s0): $CLUSTER_IP - cluster communication (multicast $MCAST_ADDR:$MCAST_PORT)"
echo "   User: core (with sudo access)"
echo ""
echo "SSH access: ./vm/ssh-vm.sh $NODE_NAME"
echo "SSH key: ./vm/$NODE_NAME-keys/cluster.key"
