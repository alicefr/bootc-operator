package cluster

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/bootc-dev/bink/internal/cluster"
	"github.com/bootc-dev/bink/internal/network"
	"github.com/bootc-dev/bink/internal/node"
)

func newCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new Kubernetes cluster",
		Long:  "Create network, control plane node, and initialize Kubernetes cluster with kubeadm",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := logrus.New()
			return runCreate(cmd.Context(), logger)
		},
	}

	return cmd
}

func runCreate(ctx context.Context, logger *logrus.Logger) error {
	logger.Info("=== Creating Kubernetes cluster ===")
	logger.Info("")

	// Step 1: Create network
	logger.Info("Step 1: Creating cluster network...")
	netMgr := network.NewManager()
	if err := netMgr.EnsureClusterNetwork(ctx); err != nil {
		return fmt.Errorf("ensuring cluster network: %w", err)
	}
	logger.Info("")

	// Step 2: Create control plane node
	logger.Info("Step 2: Creating control plane node (node1)...")
	controlPlane := node.New("node1", true)

	exists, err := controlPlane.Exists(ctx)
	if err != nil {
		return fmt.Errorf("checking if node exists: %w", err)
	}

	if exists {
		return fmt.Errorf("node1 already exists. Run 'bink cluster destroy' first")
	}

	if err := controlPlane.Create(ctx); err != nil {
		return fmt.Errorf("creating control plane node: %w", err)
	}
	logger.Info("")

	// Step 3: Initialize Kubernetes cluster
	logger.Info("Step 3: Initializing Kubernetes cluster...")
	clusterMgr := cluster.New(cluster.Config{
		Name:         "bink",
		ControlPlane: "node1",
		Logger:       logger,
	})

	if err := clusterMgr.Init(ctx, cluster.InitOptions{
		NodeName: "node1",
	}); err != nil {
		return fmt.Errorf("initializing cluster: %w", err)
	}

	logger.Info("")
	logger.Info("=== Exposing API server to localhost:6443 ===")
	// TODO: Implement API exposure
	logger.Warn("API server exposure not yet implemented")
	logger.Warn("Use: ./vm/expose-api.sh node1")

	logger.Info("")
	logger.Info("✅ Cluster created successfully!")
	logger.Info("")
	logger.Info("Usage:")
	logger.Info("  export KUBECONFIG=./vm/kubeconfig")
	logger.Info("  kubectl get nodes")
	logger.Info("")
	logger.Info("To add worker nodes:")
	logger.Info("  bink node add node2")

	return nil
}
