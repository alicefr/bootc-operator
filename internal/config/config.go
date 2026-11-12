// SPDX-License-Identifier: Apache-2.0

// Package config loads the optional operator configuration at startup and
// follows it while the process runs.
package config

import (
	"flag"

	"github.com/go-logr/logr"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
)

// Flag names shared by startup configuration and the component entry points.
const (
	AllowInsecureRegistryFlag = "allow-insecure-registry"
	TagResolutionIntervalFlag = "tag-resolution-interval"
	BootcPollIntervalFlag     = "bootc-poll-interval"
	PullSecretNamespacesFlag  = "pull-secret-namespaces"
)

// ExplicitFlags snapshots flags supplied on the command line, including false
// boolean values. Call it immediately after parsing, before applying configuration.
func ExplicitFlags(fs *flag.FlagSet) map[string]bool {
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	return explicit
}

// LogSource records the selected resource and supplied CLI flags without logging
// credentials or bootstrap flags. Supported flags are supplied by the component.
func LogSource(
	log logr.Logger,
	cfg *bootcv1alpha1.BootcOperatorConfig,
	explicit map[string]bool,
	flags ...string,
) {
	overrides := make([]string, 0, len(flags))
	for _, name := range flags {
		if explicit[name] {
			overrides = append(overrides, name)
		}
	}
	if cfg == nil {
		log.Info(
			"Loaded operator configuration",
			"source",
			"defaults",
			"suppliedCLIFlags",
			overrides,
		)
		return
	}
	log.Info("Loaded operator configuration", "source", "BootcOperatorConfig/"+cfg.Name,
		"uid", cfg.UID, "generation", cfg.Generation, "suppliedCLIFlags", overrides)
}
