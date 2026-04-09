#!/bin/bash

# Source common functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/lib.sh"

NODE_NAME="${1:-node1}"
CONTAINER_NAME="k8s-$NODE_NAME"

if [ -z "$NODE_NAME" ]; then
    echo "Usage: $0 <node-name>"
    exit 1
fi

echo "=== Setting up kubeadm config on $NODE_NAME ==="

# Get VM's passt IP for SSH (from container to VM)
SSH_HOST=$(get_vm_ssh_endpoint "$NODE_NAME")
if [ -z "$SSH_HOST" ]; then
    echo "Error: Could not get passt IP for VM $NODE_NAME"
    exit 1
fi

# Get cluster IP for Kubernetes communication
CLUSTER_IP=$(get_node_ip "$NODE_NAME")

echo "SSH endpoint: $SSH_HOST (for SSH from container)"
echo "VM cluster IP: $CLUSTER_IP (for Kubernetes communication)"

echo "Waiting for SSH to be ready on $SSH_HOST..."
for i in {1..30}; do
    if podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o ConnectTimeout=2 -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" true 2>/dev/null; then
        echo "✓ SSH is ready"
        break
    fi
    if [ $i -eq 30 ]; then
        echo "❌ Timeout waiting for SSH to be ready"
        echo "Check VM console: podman exec -ti $CONTAINER_NAME virsh -c qemu:///session console $NODE_NAME"
        exit 1
    fi
    echo -n "."
    sleep 2
done
echo ""

echo "Waiting for cloud-init to complete..."
for i in {1..60}; do
    STATUS=$(podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
        "cloud-init status 2>/dev/null | head -1 | awk '{print \$2}'" || echo "waiting")

    # Accept "done" (done with or without warnings is OK)
    if [[ "$STATUS" == "done" ]]; then
        echo "✓ cloud-init completed"
        break
    fi

    if [ $i -eq 60 ]; then
        echo "❌ Timeout waiting for cloud-init to complete"
        echo "Status: $STATUS"
        echo "Full status:"
        podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
            "cloud-init status --long"
        exit 1
    fi
    echo -n "."
    sleep 5
done
echo ""

# Create the kubeadm config file in the node container first
podman exec "$CONTAINER_NAME" bash -c 'cat > /tmp/kubeadm-config.yaml << "KUBEADM"
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
nodeRegistration:
  criSocket: "unix:///var/run/crio/crio.sock"
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
apiServer:
  certSANs:
  - "localhost"
  - "127.0.0.1"
controllerManager:
  extraArgs:
    flex-volume-plugin-dir: "/var/lib/kubelet/volumeplugins"
  extraVolumes:
  - name: flexvolume-dir
    hostPath: "/var/lib/kubelet/volumeplugins"
    mountPath: "/var/lib/kubelet/volumeplugins"
    readOnly: false
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
volumePluginDir: "/var/lib/kubelet/volumeplugins"
KUBEADM
'

# Copy the config file to the VM
podman exec "$CONTAINER_NAME" scp -P 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key \
    /tmp/kubeadm-config.yaml \
    "core@$SSH_HOST:/tmp/kubeadm-config.yaml"

# Move it to the proper location
podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
    'sudo mkdir -p /etc/kubernetes && sudo mv /tmp/kubeadm-config.yaml /etc/kubernetes/kubeadm-config.yaml'

echo ""
echo "✓ kubeadm config created at /etc/kubernetes/kubeadm-config.yaml"
echo ""
echo "=== Initializing Kubernetes cluster on $NODE_NAME ==="
echo ""

podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
    'sudo kubeadm init --config /etc/kubernetes/kubeadm-config.yaml'

echo ""
echo "=== Setting up kubectl for core user ==="
podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
    'mkdir -p $HOME/.kube && sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config && sudo chown $(id -u):$(id -g) $HOME/.kube/config'

echo ""
echo "=== Installing Calico CNI plugin ==="
podman exec "$CONTAINER_NAME" ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -i /var/run/cluster/cluster.key -p 2222 "core@$SSH_HOST" \
    'kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml'

echo ""
echo "✅ Cluster initialized on $NODE_NAME with Calico CNI"

echo ""
echo "✅ Cluster DNS server already configured via cloud-init"
echo "   Node $NODE_NAME will serve DNS on $CLUSTER_IP:53"

echo ""
echo "=== Exposing API server to localhost:6443 ==="
./vm/expose-api.sh "$NODE_NAME"
