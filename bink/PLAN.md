# Implementation Plan: Convert Scripts to "bink" Go Binary

## Context

The bootc-operator project currently uses shell scripts in `/workspace/vm/` to manage a containerized Kubernetes cluster where each node is a podman container running a VM inside. These scripts are functional but difficult to maintain, test, and extend.

**Goal:** Convert the shell scripts into a single Go binary called `bink` that provides a better user experience, is easier to maintain, and follows Go best practices.

**Out of Scope:** Building VM images (Makefile targets for `build-vm-image`, `build-disk` remain unchanged)

---

## Recommended Approach

### CLI Structure

Use Cobra framework with intuitive subcommands:

```
bink
├── network create              # Create podman network
├── cluster create              # Create network + control plane + init k8s (replaces make cluster-start)
├── cluster destroy             # Stop and remove all nodes
├── node create <name>          # Create a node container with VM
├── node add <name>             # Create and join worker node (replaces join-node.sh)
├── node ssh <name>             # SSH into node's VM
├── node list                   # List all nodes
├── api expose                  # Expose API server via SSH tunnel
└── dns add <name>              # Add DNS entry to node1

Global flags: --verbose, --debug, --cluster-name
```

**Mapping to existing scripts:**
- `bink cluster create` → `create-network.sh` + `create-node.sh node1 -p` + `init-cluster.sh`
- `bink node add node2` → `join-node.sh node2`
- `bink node ssh node1` → `ssh-vm.sh node1`
- `bink network create` → `create-network.sh`
- `bink api expose` → `expose-api.sh`
- `bink dns add node2` → `add-dns-entry.sh node2`

### Project Layout

```
/workspace/bink/
├── cmd/bink/main.go                    # CLI entry point
├── internal/
│   ├── cli/                            # Cobra command implementations
│   │   ├── cluster/                    # cluster subcommands
│   │   ├── node/                       # node subcommands
│   │   ├── network/                    # network subcommands
│   │   ├── api/                        # api subcommands
│   │   └── dns/                        # dns subcommands
│   ├── cluster/                        # Cluster orchestration logic
│   │   ├── cluster.go
│   │   ├── init.go                     # kubeadm init
│   │   └── join.go                     # kubeadm join
│   ├── node/                           # Node operations
│   │   ├── node.go                     # Core node type and methods
│   │   ├── create.go                   # Container + VM creation
│   │   ├── ip.go                       # IP calculation (MD5 hash)
│   │   ├── cloudinit.go                # Cloud-init ISO generation
│   │   └── vm.go                       # VM management
│   ├── network/network.go              # Podman network management
│   ├── dns/dns.go                      # DNS entry management (dnsmasq)
│   ├── ssh/                            # SSH operations
│   │   ├── ssh.go                      # SSH client
│   │   ├── keys.go                     # Key management
│   │   └── tunnel.go                   # Port forwarding
│   ├── podman/                         # Podman wrapper
│   │   ├── client.go                   # Podman command execution
│   │   └── types.go                    # Container/network types
│   ├── virsh/client.go                 # Virsh/libvirt wrapper
│   ├── config/                         # Configuration
│   │   ├── config.go                   # Config types
│   │   └── defaults.go                 # Default constants
│   └── util/                           # Utilities
│       ├── exec.go                     # Command execution helpers
│       ├── template.go                 # Template rendering
│       └── wait.go                     # Wait/retry helpers
├── go.mod
├── go.sum
└── README.md
```

### Key Dependencies

```
github.com/spf13/cobra              # CLI framework
github.com/spf13/viper              # Configuration management
github.com/sirupsen/logrus          # Structured logging
golang.org/x/crypto/ssh             # SSH client library
github.com/stretchr/testify         # Testing
```

Plus Go stdlib: `text/template`, `os/exec`, `context`

### Design Principles

1. **Shell out to external tools**: Use `podman`, `virsh`, `ssh` commands via `os/exec` (simpler than native libraries)
2. **Abstract execution**: Create testable wrappers in `internal/util/exec.go`
3. **Layered config**: Support flags > env vars > config file > defaults (via Viper)
4. **Clear errors**: Wrap errors with context, provide actionable messages
5. **Incremental migration**: Build bink alongside scripts, test in parallel

---

## Critical Files to Create

### Phase 1: Foundation

**Priority 1 (Core Infrastructure):**
1. `/workspace/bink/go.mod` - Initialize Go module
2. `/workspace/bink/cmd/bink/main.go` - CLI entry point with Cobra
3. `/workspace/bink/internal/config/defaults.go` - Constants (network names, IPs, etc.)
4. `/workspace/bink/internal/config/config.go` - Configuration types and loading
5. `/workspace/bink/internal/util/exec.go` - Command execution wrapper with context

**Priority 2 (Podman Integration):**
6. `/workspace/bink/internal/podman/client.go` - Podman operations (network, container, exec)
7. `/workspace/bink/internal/virsh/client.go` - Virsh operations (virt-install)

### Phase 2: Node and Network

**Priority 3 (Network):**
8. `/workspace/bink/internal/network/network.go` - Network creation/management
9. `/workspace/bink/internal/cli/network/create.go` - `bink network create` command

**Priority 4 (Node Core):**
10. `/workspace/bink/internal/node/node.go` - Node type and core methods
11. `/workspace/bink/internal/node/ip.go` - Cluster IP calculation (MD5 hash)
12. `/workspace/bink/internal/node/cloudinit.go` - Cloud-init ISO generation with templates
13. `/workspace/bink/internal/node/create.go` - Container + VM creation logic
14. `/workspace/bink/internal/cli/node/create.go` - `bink node create` command

### Phase 3: Cluster Operations

**Priority 5 (SSH):**
15. `/workspace/bink/internal/ssh/ssh.go` - SSH client (exec, interactive, scp)
16. `/workspace/bink/internal/ssh/keys.go` - SSH key management
17. `/workspace/bink/internal/cli/node/ssh.go` - `bink node ssh` command

**Priority 6 (Cluster):**
18. `/workspace/bink/internal/cluster/cluster.go` - Cluster type and orchestration
19. `/workspace/bink/internal/cluster/init.go` - kubeadm init logic
20. `/workspace/bink/internal/cluster/join.go` - kubeadm join logic
21. `/workspace/bink/internal/dns/dns.go` - DNS entry management (dnsmasq)
22. `/workspace/bink/internal/cli/cluster/create.go` - `bink cluster create` command
23. `/workspace/bink/internal/cli/node/add.go` - `bink node add` command

### Phase 4: Polish

**Priority 7 (Remaining Commands):**
24. `/workspace/bink/internal/ssh/tunnel.go` - SSH port forwarding
25. `/workspace/bink/internal/cli/api/expose.go` - `bink api expose` command
26. `/workspace/bink/internal/cli/cluster/destroy.go` - `bink cluster destroy` command
27. `/workspace/bink/internal/cli/node/list.go` - `bink node list` command
28. `/workspace/bink/internal/cli/dns/add.go` - `bink dns add` command

### Supporting Files

29. `/workspace/Makefile` - Add `build-bink` target and update `cluster-start`
30. `/workspace/README.md` - Update with bink usage
31. `/workspace/bink/internal/util/template.go` - Template helpers for cloud-init
32. `/workspace/bink/internal/util/wait.go` - Wait/retry logic for cloud-init, SSH

---

## Implementation Status

**Current Phase:** Phase 3 - ✅ COMPLETED  
**Last Updated:** 2026-04-13

### Progress Summary

- ✅ **Phase 1: Foundation** - Complete
- ✅ **Phase 2: Network and Node Creation** - Complete  
- ✅ **Phase 3: Cluster Operations** - Complete
- ⏳ **Phase 4: API and Cleanup** - Not Started

---

## Implementation Phases

### Phase 1: Foundation ✅ COMPLETED
- Initialize Go module
- Set up Cobra CLI skeleton with placeholder commands
- Implement `internal/config/` with all constants from scripts
- Implement `internal/util/exec.go` with context-aware command execution
- Implement basic `internal/podman/client.go` and `internal/virsh/client.go`
- **Deliverable:** `./bink --help` shows full command tree

### Phase 2: Network and Node Creation ✅ COMPLETED
- Implement `internal/network/network.go`
- Implement `internal/node/ip.go` (must match shell script MD5 logic exactly)
- Implement `internal/node/cloudinit.go` with templates (replicate user-data, meta-data, network-config)
- Implement `internal/node/create.go` (container + overlay disk + cloud-init ISO + VM)
- Wire up `bink network create` and `bink node create` commands
- **Deliverable:** Can create network and control plane node

### Phase 3: Cluster Operations ✅ COMPLETED
- ✅ Implement `internal/ssh/ssh.go` (exec, interactive, scp operations)
- ✅ Implement `internal/ssh/keys.go` (key management)
- ✅ Implement `internal/cluster/cluster.go` (cluster orchestration)
- ✅ Implement `internal/cluster/init.go` (kubeadm init + Calico + kubeconfig)
- ✅ Implement `internal/cluster/join.go` (join command generation + execution)
- ✅ Implement `internal/dns/dns.go` (dnsmasq host file management)
- ✅ Wire up `bink cluster create` command
- ✅ Wire up `bink node add` command  
- ✅ Wire up `bink node ssh` command
- **Deliverable:** Can create full cluster and add worker nodes
- **Completed:** 2026-04-13

**Phase 3 Files Created:**
- `/workspace/bink/internal/ssh/ssh.go` - SSH client with exec, interactive, and SCP
- `/workspace/bink/internal/ssh/keys.go` - SSH key management and configuration
- `/workspace/bink/internal/cluster/cluster.go` - Cluster orchestration and cloud-init waiting
- `/workspace/bink/internal/cluster/init.go` - Kubeadm initialization with Calico CNI
- `/workspace/bink/internal/cluster/join.go` - Worker node join functionality
- `/workspace/bink/internal/dns/dns.go` - DNS entry management via dnsmasq
- `/workspace/bink/internal/cli/cluster/create.go` - Full cluster creation command
- `/workspace/bink/internal/cli/node/add.go` - Worker node addition command
- `/workspace/bink/internal/cli/node/ssh.go` - Interactive SSH command

### Phase 4: API and Cleanup ⏳ NOT STARTED
- Implement `internal/ssh/tunnel.go` (SSH port forwarding for API server)
- Wire up `bink api expose`, `bink cluster destroy`, `bink node list`
- Update Makefile to build bink and use it in `cluster-start`
- Update documentation
- Side-by-side testing with existing scripts
- **Deliverable:** Feature-complete bink binary

---

## Key Implementation Details

### 1. IP Calculation (must match shell script)

```go
// internal/node/ip.go
func CalculateClusterIP(nodeName string) string {
    hash := md5.Sum([]byte(nodeName))
    suffix := int(hash[0]) % 240 + 10
    return fmt.Sprintf("10.0.0.%d", suffix)
}

func CalculateClusterMAC(nodeName string) string {
    hash := md5.Sum([]byte(nodeName))
    return fmt.Sprintf("52:54:01:%02x:%02x:%02x", hash[0], hash[1], hash[2])
}
```

### 2. Cloud-Init Templates

Replicate exact templates from `create-node.sh`:
- `meta-data`: instance-id, hostname
- `network-config`: dual NIC (enp1s0: DHCP, enp2s0: static cluster IP)
- `user-data`: user setup, SSH keys, dnsmasq config (node1 only), kubelet config

**Critical:** Node1 must include dnsmasq configuration, other nodes must not.

### 3. VM Creation

Exact `virt-install` command from script:
```go
virsh.VirtInstall(ctx, VirtInstallOptions{
    Name:     nodeName,
    Memory:   memory,
    VCPUs:    vcpus,
    Disk:     overlayDisk,
    CDROM:    cloudInitISO,
    Networks: []Network{
        {Type: "passt", PortForward: "2222:22"},
        {Type: "mcast", MAC: clusterMAC, Address: "230.0.0.1", Port: "5558"},
    },
})
```

### 4. Cluster Initialization

Replicate exact kubeadm config from `init-cluster.sh`:
```yaml
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
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
volumePluginDir: "/var/lib/kubelet/volumeplugins"
```

### 5. Error Handling

Every external command should:
1. Use context for timeouts
2. Capture stdout/stderr
3. Wrap errors with context
4. Provide actionable error messages

Example:
```go
if err := podman.NetworkCreate(ctx, name, subnet); err != nil {
    if strings.Contains(err.Error(), "already exists") {
        log.Infof("Network %s already exists", name)
        return nil
    }
    return fmt.Errorf("creating podman network: %w", err)
}
```

---

## Verification Plan

### Unit Tests
- IP calculation: Test known node names produce expected IPs
- Template rendering: Test cloud-init templates generate correct output
- Config loading: Test flag/env var precedence

### Integration Tests
- Mock `os/exec` to test podman/virsh command generation
- Verify command arguments match shell script logic

### Manual Testing
1. **Create cluster with bink:**
   ```bash
   cd /workspace
   make build-disk build-cluster-image
   cd bink && go build -o bink ./cmd/bink && cd ..
   ./bink/bink cluster create
   export KUBECONFIG=./vm/kubeconfig
   kubectl get nodes  # Should show node1 Ready
   ```

2. **Add worker with bink:**
   ```bash
   ./bink/bink node add node2
   kubectl get nodes  # Should show node1 and node2
   ```

3. **Compare with scripts:**
   ```bash
   # Destroy bink cluster
   ./bink/bink cluster destroy
   
   # Create with scripts
   make cluster-start
   
   # Compare: node IPs, container config, VM config, k8s cluster
   ```

4. **Verify all operations:**
   - Network creation
   - Node creation (control plane and worker)
   - Cluster initialization
   - Worker join
   - SSH access
   - API exposure
   - DNS entries

### Acceptance Criteria

✅ `bink cluster create` produces identical cluster to `make cluster-start`  
✅ `bink node add node2` produces identical result to `./vm/join-node.sh node2`  
✅ All shell script functionality is available  
✅ Error messages are clear and actionable  
✅ Performance is comparable to scripts  
✅ Documentation is complete  

---

## Makefile Integration

Update `/workspace/Makefile`:

```makefile
# Add Go binary target
BINK_BINARY := bink/bink

build-bink:
	@echo "=== Building bink binary ==="
	cd bink && go build -o bink ./cmd/bink
	@echo "✅ bink built successfully"

# Update cluster-start to use bink
cluster-start: build-cluster-image build-disk build-bink
	@echo "=== Creating cluster with bink ==="
	./$(BINK_BINARY) cluster create
	@echo ""
	@echo "✅ Cluster created!"
	@echo ""
	@echo "Usage:"
	@echo "  export KUBECONFIG=./vm/kubeconfig"
	@echo "  kubectl get nodes"
	@echo ""
	@echo "To add worker nodes:"
	@echo "  ./$(BINK_BINARY) node add node2"

cluster-stop:
	@echo "=== Destroying cluster ==="
	./$(BINK_BINARY) cluster destroy

# Keep old targets as cluster-start-legacy for compatibility
cluster-start-legacy: build-cluster-image build-disk create-network
	./vm/create-node.sh -n node1 -p
	./vm/init-cluster.sh node1
```

---

## Migration Strategy

1. **Week 1:** Build bink alongside scripts (both work)
2. **Week 2:** Update Makefile to use bink by default
3. **Week 3:** Deprecate scripts with notice pointing to bink
4. **Week 4:** Keep scripts for 1 release as fallback
5. **Week 5:** Remove scripts after validation period

---

## Success Metrics

- ✅ Cluster creation time: Within 10% of script version
- ✅ Code coverage: >70% for core packages
- ✅ User feedback: Positive on CLI UX
- ✅ Zero regression: All script functionality preserved
- ✅ Documentation: Complete command reference and examples
