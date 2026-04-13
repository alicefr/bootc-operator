package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bootc-dev/bink/internal/config"
	"github.com/bootc-dev/bink/internal/podman"
	"github.com/bootc-dev/bink/internal/util"
	"github.com/bootc-dev/bink/internal/virsh"
	"github.com/sirupsen/logrus"
)

func (n *Node) createContainer(ctx context.Context) error {
	exists, err := n.Exists(ctx)
	if err != nil {
		return err
	}

	if exists {
		return fmt.Errorf("container %s already exists", n.ContainerName)
	}

	logrus.Infof("Creating container %s", n.ContainerName)

	keysDir, err := filepath.Abs(n.KeysDir)
	if err != nil {
		return fmt.Errorf("getting absolute path for keys dir: %w", err)
	}

	if err := os.MkdirAll(keysDir, 0755); err != nil {
		return fmt.Errorf("creating keys directory: %w", err)
	}

	imagesDir := filepath.Join(config.ClusterKeysHostPath, "images")
	imagesDirAbs, err := filepath.Abs(imagesDir)
	if err != nil {
		return fmt.Errorf("getting absolute path for images dir: %w", err)
	}

	if err := os.MkdirAll(imagesDirAbs, 0755); err != nil {
		return fmt.Errorf("creating images directory: %w", err)
	}

	opts := &podman.ContainerCreateOptions{
		Name:    n.ContainerName,
		Image:   config.DefaultClusterImage,
		Network: config.DefaultNetworkName,
		Devices: []string{"/dev/kvm", "/dev/fuse"},
		Volumes: []string{
			fmt.Sprintf("%s:/src:z", imagesDirAbs),
			fmt.Sprintf("%s:/var/run/cluster:Z", keysDir),
		},
	}

	if n.IsControlPlane {
		opts.Ports = []string{"6443:6443"}
	}

	containerID, err := n.podman.ContainerCreate(ctx, opts)
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}

	logrus.Infof("Container %s created: %s", n.ContainerName, containerID)

	containerIP, err := n.podman.ContainerInspect(ctx, n.ContainerName, "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	if err != nil {
		return fmt.Errorf("getting container IP: %w", err)
	}

	logrus.Infof("Container IP: %s (VM will inherit this via passt)", containerIP)
	return nil
}

func (n *Node) setupSSHKeys(ctx context.Context) error {
	keyPath := filepath.Join(n.KeysDir, "cluster.key")

	if _, err := os.Stat(keyPath); err == nil {
		logrus.Info("Using existing cluster SSH key")
		return nil
	}

	logrus.Info("Generating cluster SSH key")

	if err := util.RunCommandQuiet(ctx, "ssh-keygen", "-t", "rsa", "-b", "4096",
		"-f", keyPath, "-N", "", "-C", "cluster-key"); err != nil {
		return fmt.Errorf("generating SSH key: %w", err)
	}

	logrus.Infof("Cluster SSH key created at %s", keyPath)
	return nil
}

func (n *Node) createOverlayDisk(ctx context.Context) error {
	overlayPath := fmt.Sprintf("/src/%s.qcow2", n.Name)

	logrus.Infof("Creating overlay disk for %s", n.Name)

	opts := &virsh.QemuImgCreateOptions{
		Path:          overlayPath,
		Format:        "qcow2",
		BackingFile:   n.BaseDisk,
		BackingFormat: "qcow2",
	}

	if err := n.virsh.QemuImgCreate(ctx, opts); err != nil {
		return fmt.Errorf("creating overlay disk: %w", err)
	}

	logrus.Infof("Overlay disk created at %s", overlayPath)
	return nil
}

func (n *Node) createVM(ctx context.Context) error {
	logrus.Infof("Creating VM %s", n.Name)

	overlayDisk := fmt.Sprintf("path=/src/%s.qcow2,format=qcow2,bus=virtio", n.Name)
	isoPath := fmt.Sprintf("path=/src/%s-cloud-init.iso,device=cdrom", n.Name)

	opts := &virsh.VirtInstallOptions{
		Name:   n.Name,
		Memory: n.Memory,
		VCPUs:  n.VCPUs,
		Disks:  []string{overlayDisk, isoPath},
		Networks: []virsh.NetworkConfig{
			{
				Type:        "passt",
				Model:       "virtio",
				PortForward: "2222:22",
			},
			{
				Type:  "mcast",
				Model: "virtio",
				MAC:   n.ClusterMAC,
			},
		},
		XMLModifications: []string{
			"xpath.set=./devices/interface[2]/source/@address=" + config.MulticastAddr,
			fmt.Sprintf("xpath.set=./devices/interface[2]/source/@port=%d", config.MulticastPort),
		},
	}

	if err := n.virsh.VirtInstall(ctx, opts); err != nil {
		return fmt.Errorf("creating VM with virt-install: %w", err)
	}

	logrus.Infof("VM %s created with dual-NIC networking", n.Name)
	logrus.Infof("  NIC 1 (enp1s0): passt - internet access")
	logrus.Infof("  NIC 2 (enp2s0): %s - cluster communication", n.ClusterIP)

	return nil
}
