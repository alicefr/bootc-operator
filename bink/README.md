# bink - Containerized Kubernetes Cluster Manager

`bink` is a CLI tool for managing containerized Kubernetes clusters where each node is a Podman container running a VM inside.

## Requirements

- Podman (for container management)
- QEMU/KVM (provided by cluster container image)
- Fedora bootc image built and available

## Status

🚧 **Under Development** - Phase 1 (Foundation) Complete

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

### Next Phases

- **Phase 3**: Cluster Operations (SSH, kubeadm init, node join, DNS)
- **Phase 4**: API and Cleanup (API exposure, cluster stop)

## Current Command Structure

```
bink
├── cluster start               # Start cluster (network + node + init) (TODO)
├── cluster stop                # Stop cluster (TODO)
└── node join <name>            # Join worker node (TODO)
```

Clean, simple interface matching the Makefile targets.

## Building

```bash
cd /workspace/bink
go build -o bink ./cmd/bink
```

## Usage (when complete)

```bash
# Start cluster (creates network, control plane node, initializes k8s)
./bink cluster start

# Join worker nodes
./bink node join node2
./bink node join node3

# Stop cluster
./bink cluster stop
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
│   │   ├── cluster/start.go            # cluster start ✅
│   │   ├── cluster/stop.go             # cluster stop ✅
│   │   └── node/join.go                # node join ✅
│   ├── cluster/                        # Cluster orchestration (TODO)
│   ├── node/                           # Node operations (TODO)
│   ├── network/                        # Network management (TODO)
│   ├── ssh/                            # SSH operations (TODO)
│   ├── podman/                         # Podman wrapper ✅
│   ├── virsh/                          # Virsh wrapper ✅
│   ├── config/                         # Configuration ✅
│   └── util/                           # Utilities ✅
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
