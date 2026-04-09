# Kubernetes Cluster Setup Guide

## Prerequisites

- Podman installed
- `bcvk` (bootc-virt-kit) for converting bootc images to qcow2
- 8GB RAM per VM minimum

## Quick Start

```bash
# Build images and create cluster
make all cluster-start

# Use kubectl from host
export KUBECONFIG=./vm/kubeconfig
kubectl get nodes

# Add worker nodes
./vm/join-node.sh node2
./vm/join-node.sh node3
```

## Architecture Overview

**Container-per-Node Design:**
- Each Kubernetes node runs in its own Podman container
- Each container hosts a single VM using libvirt (qemu:///session)
- **Dual-NIC networking** for separation of concerns:
  - **NIC 1 (enp1s0)**: passt user-mode - internet access via Podman bridge
  - **NIC 2 (enp2s0)**: multicast - cluster communication on isolated 10.0.0.0/24 network
- **DNS**: dnsmasq on node1 provides name resolution for cluster.local domain
- Fully rootless - no privileged mode or special capabilities required

### Network Topology

```
┌─────────────────────────────────────────────────────────────────────────┐
│ Host Machine                                                            │
│                                                                         │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │ Podman Network: k8s-cluster (bridge)                             │  │
│  │                                                                   │  │
│  │  ┌─────────────────────┐          ┌─────────────────────┐        │  │
│  │  │ Container: k8s-node1│          │ Container: k8s-node2│        │  │
│  │  │ IP: 10.89.4.X       │          │ IP: 10.89.4.Y       │        │  │
│  │  │                     │          │                     │        │  │
│  │  │ ┌─────────────────┐ │          │ ┌─────────────────┐ │        │  │
│  │  │ │ VM: node1       │ │          │ │ VM: node2       │ │        │  │
│  │  │ │                 │ │          │ │                 │ │        │  │
│  │  │ │ enp1s0 (passt)  │ │          │ │ enp1s0 (passt)  │ │        │  │
│  │  │ │ 10.89.4.X ──────┼─┼──────────┼─┼─ 10.89.4.Y      │ │        │  │
│  │  │ │ (internet via   │ │          │ │ (internet via   │ │        │  │
│  │  │ │  Podman bridge) │ │          │ │  Podman bridge) │ │        │  │
│  │  │ │                 │ │          │ │                 │ │        │  │
│  │  │ │ enp2s0 (mcast)  │ │          │ │ enp2s0 (mcast)  │ │        │  │
│  │  │ │ 10.0.0.32 ─────┐│ │          │ │ 10.0.0.130 ────┐│ │        │  │
│  │  │ │                ││ │          │ │                ││ │        │  │
│  │  │ │ dnsmasq:53     ││ │          │ │                ││ │        │  │
│  │  │ └────────────────┘│ │          │ └────────────────┘│ │        │  │
│  │  └───────────────────┘ │          └───────────────────┘ │        │  │
│  │          │              │                  │              │        │  │
│  └──────────┼──────────────┼──────────────────┼──────────────┘        │  │
│             │              │                  │                       │  │
│         Port 6443          │                  │                       │  │
│         (API Server)       │                  │                       │  │
│                            │                  │                       │  │
│                            └──────────────────┘                       │  │
│                        Multicast Network: 230.0.0.1:5558              │  │
│                        (Cluster Communication: 10.0.0.0/24)           │  │
└─────────────────────────────────────────────────────────────────────────┘

Network Flow:
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Internet Access:  VM → enp1s0 (passt) → Container → Podman Bridge → Host
Cluster Traffic:  VM → enp2s0 (multicast 230.0.0.1:5558) → Other VMs
DNS Resolution:   VM → 10.0.0.32:53 (dnsmasq on node1) → cluster.local
API Access:       Host:6443 → Container:6443 (SSH tunnel) → VM:6443
```

## Creating a Cluster

Build and start a single-node cluster (takes 3-5 minutes):

```bash
make all           # Build cluster container and VM image
make cluster-start # Create network, node1 container, and initialize cluster
```

The cluster is accessible from your host at `localhost:6443`. The API server's TLS certificate includes `localhost` as a Subject Alternative Name (certificate SAN), so TLS verification works without `--insecure-skip-tls-verify`.


## Adding Worker Nodes

```bash
./vm/join-node.sh node2
./vm/join-node.sh node3
```

The script creates a container and VM, generates a join token from node1, and executes `kubeadm join` automatically.

## Node Configuration

- **Container**: One per node (named `k8s-<node-name>`)
- **Networks**: 
  - Podman bridge `k8s-cluster` for internet access
  - Multicast `230.0.0.1:5558` for cluster communication
- **Resources**: 8GB RAM, 4 vCPUs per VM (configurable in create-node.sh)
- **User**: core (with passwordless sudo)
- **Networking**: 
  - **enp1s0**: passt (internet via Podman bridge, DHCP ~10.89.4.X)
  - **enp2s0**: multicast (cluster network, static 10.0.0.X, deterministic from node name hash)
- **DNS**: 
  - All nodes use node1 (10.0.0.32) as DNS server
  - dnsmasq on node1 serves cluster.local domain
  - Short names (`node2`) and FQDNs (`node2.cluster.local`) both work
- **Storage**: qcow2 overlay disks (copy-on-write, shared base image)
- **Cloud-init**: 
  - SSH key injection
  - Network configuration (dual-NIC setup)
  - Swap disabled, IP forwarding enabled
  - CRI-O and kubelet enabled
  - dnsmasq configured and enabled on node1 only (pre-installed in bootc image)
  - Scripts wait for cloud-init completion before kubeadm runs

**DNS Setup Process:**
1. node1 created → dnsmasq (pre-installed in bootc image) configured via cloud-init
2. DNS hosts file: `/var/lib/dnsmasq/cluster-hosts` created with correct permissions
3. Worker nodes join → `add-dns-entry.sh` adds entry to node1
4. systemd-resolved on all nodes configured with routing domain `~cluster.local`

## Common Operations

**SSH into VM:**
```bash
./vm/ssh-vm.sh <node-name>
```

**Stop cluster:**
```bash
make cluster-stop  # Stops and removes all node containers
```

**List node containers:**
```bash
podman ps --filter "name=k8s-"
```
