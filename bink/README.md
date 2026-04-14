# bink - Containerized Kubernetes Cluster Manager

`bink` is a CLI tool for managing containerized Kubernetes clusters where each node is a Podman container running a VM inside.

## Requirements

- Podman (for container management)
- QEMU/KVM (provided by cluster container image)
- Fedora bootc image built and available

## Status

✅ **Feature Complete** - All phases completed!

### Phase 1: Foundation ✅
- [x] Go module initialized
- [x] Simplified Cobra CLI (3 commands)
- [x] Configuration management (flags, env vars, config file)
- [x] Command execution utilities
- [x] Podman client wrapper
- [x] Virsh client wrapper
- [x] No comments in code (per style guide)

**Deliverable:** `./bink --help` shows full command tree ✅

### Phase 2: Network and Node Creation ✅
- [x] Network management (create, inspect)
- [x] Node IP calculation (MD5-based, matches shell script)
- [x] Node MAC calculation
- [x] Cloud-init ISO generation (meta-data, network-config, user-data)
- [x] Container creation (KVM, volumes, networking)
- [x] Overlay disk creation
- [x] VM creation (dual NIC: passt + multicast)
- [x] `bink cluster start` command wired up
- [x] Unit tests for IP calculation

**Deliverable:** Can create network and control plane node ✅

### Phase 3: Cluster Operations ✅
- [x] SSH client implementation
- [x] SSH key management
- [x] Cluster initialization (kubeadm init)
- [x] Calico CNI installation
- [x] Worker node join functionality
- [x] DNS management (dnsmasq)
- [x] Cloud-init wait logic
- [x] Interactive SSH command

**Deliverable:** Can create full cluster and add worker nodes ✅

### Phase 4: API and Cleanup ✅
- [x] SSH tunnel for API server
- [x] API expose command
- [x] Node list command
- [x] Cluster stop command
- [x] Makefile integration
- [x] Documentation updates

**Deliverable:** Feature-complete bink binary ✅

## Command Structure

```
bink
├── cluster
│   ├── start                   # Start cluster (network + node + init) ✅
│   └── stop                    # Stop and remove all nodes ✅
│       --remove-data           # Also remove overlay disks, ISOs, keys, kubeconfig
├── node
│   ├── add <name>              # Create and join worker node ✅
│   ├── join <name>             # Alias for add ✅
│   ├── ssh <name>              # SSH into node's VM ✅
│   └── list                    # List all cluster nodes ✅
└── api
    └── expose                  # Expose API server to localhost:6443 ✅
```

Clean, simple interface matching the Makefile targets.

## Building

```bash
cd /workspace/bink
go build -o bink ./cmd/bink
```

## Usage

```bash
# Start cluster (creates network, control plane node, initializes k8s)
./bink cluster start

# List nodes
./bink node list

# Join worker nodes
./bink node add node2
./bink node add node3

# SSH into a node
./bink node ssh node1

# Expose API server to localhost:6443 (generates kubeconfig)
./bink api expose

# Stop cluster (keeps data)
./bink cluster stop

# Stop cluster and remove all data
./bink cluster stop --remove-data
```

## Testing

```bash
./bink --help
./bink cluster --help
./bink node --help
```

## Project Structure

```
bink/
├── cmd/bink/main.go                    # CLI entry point ✅
├── internal/
│   ├── cli/                            # Cobra commands ✅
│   │   ├── api/                        # API commands ✅
│   │   │   ├── api.go                  # API root command ✅
│   │   │   └── expose.go               # API expose ✅
│   │   ├── cluster/                    # Cluster commands ✅
│   │   │   ├── cluster.go              # Cluster root command ✅
│   │   │   ├── create.go               # cluster create ✅
│   │   │   ├── start.go                # cluster start ✅
│   │   │   ├── stop.go                 # cluster stop ✅
│   │   │   └── destroy.go              # cluster destroy ✅
│   │   └── node/                       # Node commands ✅
│   │       ├── node.go                 # Node root command ✅
│   │       ├── add.go                  # node add ✅
│   │       ├── join.go                 # node join ✅
│   │       ├── ssh.go                  # node ssh ✅
│   │       └── list.go                 # node list ✅
│   ├── cluster/                        # Cluster orchestration ✅
│   │   ├── cluster.go                  # Core orchestration ✅
│   │   ├── init.go                     # kubeadm init ✅
│   │   └── join.go                     # kubeadm join ✅
│   ├── node/                           # Node operations ✅
│   │   ├── node.go                     # Core node type ✅
│   │   ├── create.go                   # Container + VM creation ✅
│   │   ├── ip.go                       # IP/MAC calculation ✅
│   │   └── cloudinit.go                # Cloud-init ISO ✅
│   ├── network/                        # Network management ✅
│   │   └── network.go                  # Podman network ops ✅
│   ├── dns/                            # DNS management ✅
│   │   └── dns.go                      # dnsmasq entries ✅
│   ├── ssh/                            # SSH operations ✅
│   │   ├── ssh.go                      # SSH client ✅
│   │   ├── keys.go                     # Key management ✅
│   │   └── tunnel.go                   # Port forwarding ✅
│   ├── podman/                         # Podman wrapper ✅
│   │   └── client.go                   # Podman commands ✅
│   ├── virsh/                          # Virsh wrapper ✅
│   │   └── client.go                   # Virsh commands ✅
│   ├── config/                         # Configuration ✅
│   │   ├── config.go                   # Config types ✅
│   │   └── defaults.go                 # Constants ✅
│   └── util/                           # Utilities ✅
│       └── exec.go                     # Command execution ✅
├── go.mod ✅
├── go.sum ✅
├── PLAN.md
└── README.md
```

## Dependencies

- `github.com/spf13/cobra` - CLI framework
- `github.com/spf13/viper` - Configuration management
- `github.com/sirupsen/logrus` - Structured logging

## Configuration

Configuration can be provided via:
1. Command-line flags (highest priority)
2. Environment variables (prefix: `BINK_`)
3. Config file (`~/.bink/config.yaml` or `./config.yaml`)
4. Defaults (lowest priority)

### Example config.yaml

```yaml
cluster:
  name: k8s-cluster
  network_name: k8s-cluster
  subnet: 10.89.0.0/24
  image: localhost/cluster:latest
  base_disk: /src/fedora-bootc-k8s.qcow2
  kubeconfig_path: ./vm/kubeconfig

node:
  memory: 8192
  vcpus: 4

logging:
  verbose: false
  debug: false
```

## Next Steps

See [PLAN.md](PLAN.md) for the full implementation plan.
