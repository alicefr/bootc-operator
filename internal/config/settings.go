// SPDX-License-Identifier: Apache-2.0

package config

import (
	"flag"
	"fmt"
	"slices"
	"strings"
	"time"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
)

// Settings are the operator settings that can change while the controller
// and the daemons run. Each process uses the fields of its own component and
// ignores the others.
type Settings struct {
	// AllowInsecureRegistry allows the controller to fall back to HTTP when
	// resolving image tags.
	AllowInsecureRegistry bool

	// TagResolutionInterval is how often the controller re-resolves tags.
	TagResolutionInterval time.Duration

	// PullSecretNamespaces are the namespaces, besides the operator's own,
	// that pools may reference pull secrets from.
	PullSecretNamespaces []string

	// BootcPollInterval is the daemon's fallback bootc status polling
	// interval.
	BootcPollInterval time.Duration
}

// SettingsFromFlags returns the settings given by the flags a process
// registered: the CLI values, or the flags' defaults. They are what applies
// when no BootcOperatorConfig sets a field, so take them before any
// configuration is applied. Flags the process did not register leave the
// zero value.
func SettingsFromFlags(fs *flag.FlagSet) (Settings, error) {
	var s Settings
	get := func(name string) (any, error) {
		f := fs.Lookup(name)
		if f == nil {
			return nil, nil
		}
		getter, ok := f.Value.(flag.Getter)
		if !ok {
			return nil, fmt.Errorf("--%s does not support reading its value", name)
		}
		return getter.Get(), nil
	}

	if v, err := get(AllowInsecureRegistryFlag); err != nil {
		return s, err
	} else if b, ok := v.(bool); ok {
		s.AllowInsecureRegistry = b
	}
	if v, err := get(TagResolutionIntervalFlag); err != nil {
		return s, err
	} else if d, ok := v.(time.Duration); ok {
		s.TagResolutionInterval = d
	}
	if v, err := get(BootcPollIntervalFlag); err != nil {
		return s, err
	} else if d, ok := v.(time.Duration); ok {
		s.BootcPollInterval = d
	}
	if v, err := get(PullSecretNamespacesFlag); err != nil {
		return s, err
	} else if str, ok := v.(string); ok {
		s.PullSecretNamespaces = splitNamespaces(str)
	}
	return s, nil
}

func splitNamespaces(value string) []string {
	var namespaces []string
	for _, ns := range strings.Split(value, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			namespaces = append(namespaces, ns)
		}
	}
	return namespaces
}

// Resolve returns the settings for cfg, a validated configuration or nil
// when there is none: every field cfg sets overrides the fallback, and the
// others, including an omitted field next to an explicit false or empty one,
// keep the fallback.
func Resolve(fallback Settings, cfg *bootcv1alpha1.BootcOperatorConfig) Settings {
	s := fallback
	s.PullSecretNamespaces = slices.Clone(fallback.PullSecretNamespaces)
	if cfg == nil {
		return s
	}
	if c := cfg.Spec.Controller; c != nil {
		if c.PullSecretNamespaces != nil {
			s.PullSecretNamespaces = slices.Clone(c.PullSecretNamespaces)
		}
		if c.AllowInsecureRegistry != nil {
			s.AllowInsecureRegistry = *c.AllowInsecureRegistry
		}
		if c.TagResolutionPeriodSeconds != nil {
			s.TagResolutionInterval = time.Duration(*c.TagResolutionPeriodSeconds) * time.Second
		}
	}
	if d := cfg.Spec.Daemon; d != nil && d.StatusPollPeriodSeconds != nil {
		s.BootcPollInterval = time.Duration(*d.StatusPollPeriodSeconds) * time.Second
	}
	return s
}

// Equal reports whether the settings are the same.
func (s Settings) Equal(other Settings) bool {
	return s.AllowInsecureRegistry == other.AllowInsecureRegistry &&
		s.TagResolutionInterval == other.TagResolutionInterval &&
		s.BootcPollInterval == other.BootcPollInterval &&
		slices.Equal(s.PullSecretNamespaces, other.PullSecretNamespaces)
}
