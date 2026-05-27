// SPDX-License-Identifier: Apache-2.0

package bootc

import (
	"context"
	"fmt"
	"os/exec"
)

// Executor abstracts the execution of bootc commands on the host.
// The real implementation uses nsenter to enter the host's mount and
// PID namespaces. Tests can provide a fake implementation.
type Executor interface {
	Status(ctx context.Context) ([]byte, error)
	Switch(ctx context.Context, image string, apply bool) error
}

// HostExecutor runs bootc commands on the host via nsenter.
// It requires hostPID: true and privileged: true in the pod spec.
type HostExecutor struct{}

func NewHostExecutor() *HostExecutor {
	return &HostExecutor{}
}

func (e *HostExecutor) nsenterCmd(ctx context.Context, args ...string) *exec.Cmd {
	base := []string{
		"--target", "1",
		"--mount", "--pid",
		"--setuid", "0", "--setgid", "0",
		"--env", "--",
	}
	return exec.CommandContext(ctx, "nsenter", append(base, args...)...)
}

func (e *HostExecutor) Status(ctx context.Context) ([]byte, error) {
	cmd := e.nsenterCmd(ctx, "bootc", "status", "--json", "--format-version", "1")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running bootc status: %w", err)
	}
	return out, nil
}

// Switch uses `bootc switch` which both downloads and stages. Once bootc
// supports --download-only (https://github.com/bootc-dev/bootc/issues/2137),
// use it for the non-apply path to separate download from staging.
func (e *HostExecutor) Switch(ctx context.Context, image string, apply bool) error {
	args := []string{"bootc", "switch"}
	if apply {
		args = append(args, "--apply")
	}
	args = append(args, image)

	cmd := e.nsenterCmd(ctx, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("running bootc switch: %s: %w", out, err)
	}
	return nil
}
