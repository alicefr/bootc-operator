package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/bootc-dev/bink/internal/network"
	"github.com/bootc-dev/bink/internal/node"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func newStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a new cluster",
		Long:  "Create network, control plane node, and initialize Kubernetes cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStart(cmd.Context())
		},
	}

	return cmd
}

func runStart(ctx context.Context) error {
	logrus.Info("Starting cluster creation")

	netMgr := network.NewManager()
	if err := netMgr.EnsureClusterNetwork(ctx); err != nil {
		return fmt.Errorf("ensuring cluster network: %w", err)
	}

	logrus.Info("Creating control plane node (node1)")
	controlPlane := node.New("node1", true)

	exists, err := controlPlane.Exists(ctx)
	if err != nil {
		return fmt.Errorf("checking if node exists: %w", err)
	}

	if exists {
		return fmt.Errorf("node1 already exists. Run 'bink cluster stop' first")
	}

	if err := controlPlane.Create(ctx); err != nil {
		return fmt.Errorf("creating control plane node: %w", err)
	}

	logrus.Info("Waiting for VM to boot and cloud-init to complete...")
	time.Sleep(15 * time.Second)

	logrus.Info("Cluster creation complete!")
	logrus.Info("")
	logrus.Info("Next steps:")
	logrus.Info("  1. Initialize Kubernetes (not yet implemented)")
	logrus.Info("  2. Join worker nodes: bink node join node2")
	logrus.Info("")
	logrus.Infof("SSH access: ./vm/ssh-vm.sh node1")
	logrus.Infof("SSH key: ./vm/node1-keys/cluster.key")

	return nil
}
