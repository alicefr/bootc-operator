.PHONY: all build-vm-image build-cluster-image build-disk create-network cluster-start cluster-stop clean help

# Image names and tags
BOOTC_IMAGE := localhost/fedora-bootc-k8s:latest
CLUSTER_IMAGE := localhost/cluster:latest
DISK_IMAGE := fedora-bootc-k8s.qcow2
DISK_SIZE := 10G

# Directories
IMAGES_DIR := containerfiles/images
VM_DIR := containerfiles/vm
OUTPUT_DIR := vm/images

all: build-cluster-image build-vm-image build-disk

# Build the fedora-bootc-k8s VM image
build-vm-image:
	@echo "=== Building fedora-bootc-k8s VM image ==="
	podman build -t $(BOOTC_IMAGE) -f $(IMAGES_DIR)/Containerfile $(IMAGES_DIR)
	@echo "✅ VM image built: $(BOOTC_IMAGE)"

# Build the cluster container image
build-cluster-image:
	@echo "=== Building cluster container image ==="
	podman build -t $(CLUSTER_IMAGE) -f $(VM_DIR)/Containerfile $(VM_DIR)
	@echo "✅ Cluster image built: $(CLUSTER_IMAGE)"

# Convert bootc image to qcow2 disk
build-disk: build-vm-image
	@echo "=== Converting bootc image to disk ==="
	@mkdir -p $(OUTPUT_DIR)
	cd $(OUTPUT_DIR) && \
	RUST_LOG=debug bcvk to-disk -K \
		--karg 'console=tty0' \
		--karg 'console=ttyS0,115200n8' \
		--filesystem ext4 \
		--format qcow2 \
		--disk-size $(DISK_SIZE) \
		$(BOOTC_IMAGE) $(DISK_IMAGE)
	@echo "✅ Disk image created: $(OUTPUT_DIR)/$(DISK_IMAGE)"

# Clean built images and disk
clean:
	@echo "=== Cleaning up ==="
	podman rmi -f $(BOOTC_IMAGE) $(CLUSTER_IMAGE) 2>/dev/null || true
	rm -f $(OUTPUT_DIR)/$(DISK_IMAGE)
	@echo "✅ Cleaned up images and disk"

# Clean disk image only
clean-disk:
	@echo "=== Cleaning disk image ==="
	rm -f $(OUTPUT_DIR)/$(DISK_IMAGE)
	@echo "✅ Disk image removed"

# Rebuild everything from scratch
rebuild: clean all

# Create Podman network for cluster communication
create-network:
	@echo "=== Creating Podman network ==="
	./vm/create-network.sh

# Start the cluster (create network + create node1 + init cluster)
cluster-start: build-cluster-image build-disk create-network
	@echo "=== Creating control plane node (node1) ==="
	./vm/create-node.sh -n node1 -p
	@echo ""
	@echo "=== Initializing Kubernetes cluster on node1 ==="
	./vm/init-cluster.sh node1
	@echo ""
	@echo "✅ Cluster initialized on node1!"
	@echo ""
	@echo "Usage:"
	@echo "  export KUBECONFIG=./vm/kubeconfig"
	@echo "  kubectl get nodes"
	@echo ""
	@echo "To add worker nodes:"
	@echo "  ./vm/join-node.sh node2"

# Stop and remove all node containers
cluster-stop:
	@echo "=== Stopping all node containers ==="
	podman ps -a --filter "name=k8s-" --format "{{.Names}}" | xargs -r podman stop
	podman ps -a --filter "name=k8s-" --format "{{.Names}}" | xargs -r podman rm
	@echo "✅ All node containers stopped and removed"

help:
	@echo "Makefile for building and deploying Kubernetes cluster with container-per-node architecture"
	@echo ""
	@echo "Build Targets:"
	@echo "  all                 - Build both cluster and VM images, then create disk (default)"
	@echo "  build-vm-image      - Build the fedora-bootc-k8s VM container image"
	@echo "  build-cluster-image - Build the cluster container image"
	@echo "  build-disk          - Convert bootc image to qcow2 disk image"
	@echo ""
	@echo "Cluster Targets:"
	@echo "  cluster-start       - Create network, node1 container, and initialize cluster (recommended)"
	@echo "  create-network      - Create Podman network for cluster communication"
	@echo "  cluster-stop        - Stop and remove all node containers"
	@echo ""
	@echo "Clean Targets:"
	@echo "  clean               - Remove all built images and disk"
	@echo "  clean-disk          - Remove only the disk image"
	@echo "  rebuild             - Clean and rebuild everything"
	@echo ""
	@echo "Other:"
	@echo "  help                - Show this help message"
	@echo ""
	@echo "Images:"
	@echo "  VM image:      $(BOOTC_IMAGE)"
	@echo "  Cluster image: $(CLUSTER_IMAGE)"
	@echo "  Disk image:    $(OUTPUT_DIR)/$(DISK_IMAGE)"
	@echo ""
	@echo "Scripts:"
	@echo "  ./vm/create-node.sh <name> - Create a new node container with VM"
	@echo "  ./vm/join-node.sh <name>   - Create and join a worker node to cluster"
	@echo "  ./vm/ssh-vm.sh <name>      - SSH into a node's VM"
