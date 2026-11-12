// SPDX-License-Identifier: Apache-2.0

package config

import (
	"flag"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
)

func TestSettingsFromFlags(t *testing.T) {
	register := func(fs *flag.FlagSet) {
		fs.Bool(AllowInsecureRegistryFlag, false, "")
		fs.Duration(TagResolutionIntervalFlag, 5*time.Minute, "")
		fs.Duration(BootcPollIntervalFlag, 7*time.Minute, "")
		fs.String(PullSecretNamespacesFlag, "", "")
	}
	for _, tt := range []struct {
		name string
		args []string
		want Settings
	}{
		{
			name: "defaults",
			want: Settings{TagResolutionInterval: 5 * time.Minute, BootcPollInterval: 7 * time.Minute},
		},
		{
			name: "CLI values",
			args: []string{
				"--allow-insecure-registry", "--tag-resolution-interval=1500ms",
				"--bootc-poll-interval=-1s", "--pull-secret-namespaces= team-a,,team-b ",
			},
			want: Settings{
				AllowInsecureRegistry: true,
				TagResolutionInterval: 1500 * time.Millisecond,
				BootcPollInterval:     -time.Second,
				PullSecretNamespaces:  []string{"team-a", "team-b"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			fs := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
			register(fs)
			g.Expect(fs.Parse(tt.args)).To(Succeed())
			got, err := SettingsFromFlags(fs)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(got.Equal(tt.want)).To(BeTrue(), "got %+v, want %+v", got, tt.want)
		})
	}

	t.Run("unregistered flags are left zero", func(t *testing.T) {
		g := NewWithT(t)
		fs := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
		fs.Duration(BootcPollIntervalFlag, 7*time.Minute, "")
		got, err := SettingsFromFlags(fs)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(got).To(Equal(Settings{BootcPollInterval: 7 * time.Minute}))
	})
}

func TestResolve(t *testing.T) {
	fallback := Settings{
		AllowInsecureRegistry: true,
		TagResolutionInterval: 5 * time.Minute,
		BootcPollInterval:     7 * time.Minute,
		PullSecretNamespaces:  []string{"from-cli"},
	}
	cfg := func(
		c *bootcv1alpha1.OperatorControllerConfig,
		d *bootcv1alpha1.OperatorDaemonConfig,
	) *bootcv1alpha1.BootcOperatorConfig {
		return &bootcv1alpha1.BootcOperatorConfig{
			Spec: bootcv1alpha1.BootcOperatorConfigSpec{Controller: c, Daemon: d},
		}
	}
	for _, tt := range []struct {
		name string
		cfg  *bootcv1alpha1.BootcOperatorConfig
		want Settings
	}{
		{name: "no configuration", want: fallback},
		{name: "empty configuration keeps the fallback", cfg: cfg(nil, nil), want: fallback},
		{
			name: "empty sections keep the fallback",
			cfg: cfg(
				&bootcv1alpha1.OperatorControllerConfig{},
				&bootcv1alpha1.OperatorDaemonConfig{},
			),
			want: fallback,
		},
		{
			name: "explicit false overrides true",
			cfg: cfg(
				&bootcv1alpha1.OperatorControllerConfig{AllowInsecureRegistry: ptr.To(false)},
				nil,
			),
			want: Settings{
				TagResolutionInterval: 5 * time.Minute,
				BootcPollInterval:     7 * time.Minute,
				PullSecretNamespaces:  []string{"from-cli"},
			},
		},
		{
			name: "empty namespaces override the fallback",
			cfg: cfg(
				&bootcv1alpha1.OperatorControllerConfig{PullSecretNamespaces: []string{}},
				nil,
			),
			want: Settings{
				AllowInsecureRegistry: true,
				TagResolutionInterval: 5 * time.Minute,
				BootcPollInterval:     7 * time.Minute,
			},
		},
		{
			name: "all fields",
			cfg: cfg(
				&bootcv1alpha1.OperatorControllerConfig{
					AllowInsecureRegistry:      ptr.To(false),
					TagResolutionPeriodSeconds: ptr.To(int32(30)),
					PullSecretNamespaces:       []string{"team-a", "team-b"},
				},
				&bootcv1alpha1.OperatorDaemonConfig{StatusPollPeriodSeconds: ptr.To(int32(10))},
			),
			want: Settings{
				TagResolutionInterval: 30 * time.Second,
				BootcPollInterval:     10 * time.Second,
				PullSecretNamespaces:  []string{"team-a", "team-b"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			got := Resolve(fallback, tt.cfg)
			g.Expect(got.Equal(tt.want)).To(BeTrue(), "got %+v, want %+v", got, tt.want)
		})
	}

	t.Run("result does not alias its inputs", func(t *testing.T) {
		g := NewWithT(t)
		own := Resolve(fallback, nil)
		own.PullSecretNamespaces[0] = "changed"
		g.Expect(fallback.PullSecretNamespaces).To(Equal([]string{"from-cli"}))

		c := cfg(
			&bootcv1alpha1.OperatorControllerConfig{PullSecretNamespaces: []string{"team-a"}},
			nil,
		)
		own = Resolve(fallback, c)
		own.PullSecretNamespaces[0] = "changed"
		g.Expect(c.Spec.Controller.PullSecretNamespaces).To(Equal([]string{"team-a"}))
	})
}
