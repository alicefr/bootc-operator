// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
	"github.com/bootc-dev/bootc-operator/internal/config"
	"github.com/bootc-dev/bootc-operator/internal/pullsecret"
)

type stubConfigState struct{ state config.State }

func (s *stubConfigState) State() config.State { return s.state }

type stubNamespaces struct{ statuses []pullsecret.NamespaceStatus }

func (s *stubNamespaces) Statuses() []pullsecret.NamespaceStatus { return s.statuses }

func (*stubNamespaces) NamespaceSource(
	handler.TypedEventHandler[client.Object, reconcile.Request],
) source.SyncingSource {
	return nil
}

func listable(ns string) pullsecret.NamespaceStatus {
	return pullsecret.NamespaceStatus{Namespace: ns, Listable: true, WatchHealthy: true}
}

func denied(ns, reason string) pullsecret.NamespaceStatus {
	return pullsecret.NamespaceStatus{Namespace: ns, Reason: reason}
}

func configNamed(name string, generation int64) *bootcv1alpha1.BootcOperatorConfig {
	return &bootcv1alpha1.BootcOperatorConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			UID:        types.UID("uid-" + name),
			Generation: generation,
		},
	}
}

func TestDesiredConfigStatus(t *testing.T) {
	cfg := configNamed("cluster", 3)
	applied := config.State{Active: cfg.DeepCopy()}
	unhealthy := listable("team-a")
	unhealthy.WatchHealthy = false

	for _, tt := range []struct {
		name       string
		cfg        *bootcv1alpha1.BootcOperatorConfig
		state      config.State
		namespaces []pullsecret.NamespaceStatus
		wantOK     bool

		wantGeneration int64
		wantListable   []string
		wantStatus     metav1.ConditionStatus
		wantReason     string
		wantMessage    string
	}{
		{
			name: "ready", cfg: cfg, state: applied, wantOK: true,
			namespaces:     []pullsecret.NamespaceStatus{listable("team-b"), listable("operator")},
			wantGeneration: 3,
			wantListable:   []string{"operator", "team-b"},
			wantStatus:     metav1.ConditionTrue,
			wantReason:     bootcv1alpha1.ConfigReasonReady,
		},
		{
			name: "namespace without access", cfg: cfg, state: applied, wantOK: true,
			namespaces: []pullsecret.NamespaceStatus{
				listable("operator"), denied("team-a", "the controller cannot list Secrets"),
			},
			wantGeneration: 3,
			wantListable:   []string{"operator"},
			wantStatus:     metav1.ConditionFalse,
			wantReason:     bootcv1alpha1.ConfigReasonPendingNamespaces,
			wantMessage:    "team-a (the controller cannot list Secrets)",
		},
		{
			name: "watch failing", cfg: cfg, state: applied, wantOK: true,
			namespaces:     []pullsecret.NamespaceStatus{listable("operator"), unhealthy},
			wantGeneration: 3,
			wantListable:   []string{"operator", "team-a"},
			wantStatus:     metav1.ConditionFalse,
			wantReason:     bootcv1alpha1.ConfigReasonWatchUnhealthy,
			wantMessage:    "Secrets in team-a",
		},
		{
			name: "both pending and failing", cfg: cfg, state: applied, wantOK: true,
			namespaces:     []pullsecret.NamespaceStatus{unhealthy, denied("team-b", "denied")},
			wantGeneration: 3,
			wantListable:   []string{"team-a"},
			wantStatus:     metav1.ConditionFalse,
			wantReason:     bootcv1alpha1.ConfigReasonPendingNamespaces,
			wantMessage:    "watch of Secrets in team-a is failing",
		},
		{
			name: "namespaces not checked yet", cfg: cfg, state: applied, wantOK: false,
			namespaces: []pullsecret.NamespaceStatus{{Namespace: "team-a", Checking: true}},
		},
		{
			name: "generation not applied yet", cfg: configNamed("cluster", 4), state: applied,
			wantOK: false, namespaces: []pullsecret.NamespaceStatus{listable("operator")},
		},
		{
			name: "multiple configurations", cfg: cfg, wantOK: true,
			state: config.State{
				Active:    configNamed("cluster", 2),
				Conflicts: []string{"cluster", "other"},
			},
			namespaces:     []pullsecret.NamespaceStatus{listable("operator")},
			wantGeneration: 0,
			wantListable:   []string{"operator"},
			wantStatus:     metav1.ConditionFalse,
			wantReason:     bootcv1alpha1.ConfigReasonMultipleConfigs,
			wantMessage:    "cluster, other",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			status, ok := desiredConfigStatus(tt.cfg, tt.state, tt.namespaces)
			g.Expect(ok).To(Equal(tt.wantOK))
			if !tt.wantOK {
				return
			}
			g.Expect(status.ObservedGeneration).To(Equal(tt.wantGeneration))
			g.Expect(status.ListablePullSecretNamespaces).To(Equal(tt.wantListable))
			g.Expect(status.Conditions).To(ConsistOf(And(
				HaveField("Type", bootcv1alpha1.ConfigControllerReady),
				HaveField("Status", tt.wantStatus),
				HaveField("Reason", tt.wantReason),
				HaveField("Message", ContainSubstring(tt.wantMessage)),
				HaveField("ObservedGeneration", tt.cfg.Generation),
			)))
		})
	}
}

func TestDesiredConfigStatusKeepsObservedGenerationDuringConflict(t *testing.T) {
	g := NewWithT(t)
	cfg := configNamed("cluster", 5)
	cfg.Status.ObservedGeneration = 4

	status, ok := desiredConfigStatus(cfg,
		config.State{Conflicts: []string{"cluster", "other"}}, nil)

	g.Expect(ok).To(BeTrue())
	g.Expect(status.ObservedGeneration).To(Equal(int64(4)), "5 was not applied")
}

func newStatusReconciler(
	c client.Client,
	state *stubConfigState,
	namespaces *stubNamespaces,
) *ConfigStatusReconciler {
	return &ConfigStatusReconciler{Client: c, Config: state, Namespaces: namespaces}
}

func TestConfigStatusReconcilerWritesChangesOnly(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	cfg := &bootcv1alpha1.BootcOperatorConfig{ObjectMeta: metav1.ObjectMeta{Name: "status-cluster"}}
	g.Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
	t.Cleanup(func() { g.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cfg))).To(Succeed()) })

	state := &stubConfigState{state: config.State{Active: cfg.DeepCopy()}}
	namespaces := &stubNamespaces{statuses: []pullsecret.NamespaceStatus{listable("operator")}}
	r := newStatusReconciler(k8sClient, state, namespaces)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cfg)}
	get := func() *bootcv1alpha1.BootcOperatorConfig {
		var got bootcv1alpha1.BootcOperatorConfig
		g.Expect(k8sClient.Get(ctx, req.NamespacedName, &got)).To(Succeed())
		return &got
	}

	_, err := r.Reconcile(ctx, req)
	g.Expect(err).NotTo(HaveOccurred())
	first := get()
	g.Expect(first.Status.ObservedGeneration).To(Equal(first.Generation))
	g.Expect(first.Status.ListablePullSecretNamespaces).To(Equal([]string{"operator"}))
	ready := apimeta.FindStatusCondition(
		first.Status.Conditions,
		bootcv1alpha1.ConfigControllerReady,
	)
	g.Expect(ready).To(HaveField("Status", metav1.ConditionTrue))

	// Nothing changed, so nothing is written.
	_, err = r.Reconcile(ctx, req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(get().ResourceVersion).To(Equal(first.ResourceVersion))

	// A namespace loses its access: the condition and the list follow.
	namespaces.statuses = []pullsecret.NamespaceStatus{
		listable("operator"), denied("team-a", "the controller cannot list Secrets"),
	}
	_, err = r.Reconcile(ctx, req)
	g.Expect(err).NotTo(HaveOccurred())
	pending := get()
	g.Expect(pending.Status.Conditions).To(ConsistOf(And(
		HaveField("Status", metav1.ConditionFalse),
		HaveField("Reason", bootcv1alpha1.ConfigReasonPendingNamespaces),
		HaveField("Message", ContainSubstring("team-a")),
	)))
	g.Expect(pending.Status.ListablePullSecretNamespaces).To(Equal([]string{"operator"}))
	g.Expect(pending.Generation).To(Equal(first.Generation), "a status write is no spec change")

	// It recovers.
	namespaces.statuses = []pullsecret.NamespaceStatus{listable("operator"), listable("team-a")}
	_, err = r.Reconcile(ctx, req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(get().Status.ListablePullSecretNamespaces).To(Equal([]string{"operator", "team-a"}))
}

func TestConfigStatusReconcilerReportsConflictOnEveryConfig(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	var configs []*bootcv1alpha1.BootcOperatorConfig
	for _, name := range []string{"status-a", "status-b"} {
		cfg := &bootcv1alpha1.BootcOperatorConfig{ObjectMeta: metav1.ObjectMeta{Name: name}}
		g.Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		t.Cleanup(
			func() { g.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cfg))).To(Succeed()) },
		)
		configs = append(configs, cfg)
	}
	state := &stubConfigState{state: config.State{Conflicts: []string{"status-a", "status-b"}}}
	r := newStatusReconciler(k8sClient, state,
		&stubNamespaces{statuses: []pullsecret.NamespaceStatus{listable("operator")}})

	for _, cfg := range configs {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cfg)})
		g.Expect(err).NotTo(HaveOccurred())

		var got bootcv1alpha1.BootcOperatorConfig
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cfg), &got)).To(Succeed())
		g.Expect(got.Status.Conditions).To(ConsistOf(And(
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Reason", bootcv1alpha1.ConfigReasonMultipleConfigs),
		)))
	}
}

func TestConfigStatusReconcilerIgnoresMissingConfig(t *testing.T) {
	g := NewWithT(t)
	r := newStatusReconciler(k8sClient, &stubConfigState{}, &stubNamespaces{})
	result, err := r.Reconcile(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "never-created"}})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))
}

func TestConfigStatusReconcilerStatusWriteFailures(t *testing.T) {
	cfg := configNamed("cluster", 1)
	cfg.UID = ""
	for _, tt := range []struct {
		name        string
		err         error
		wantRequeue time.Duration
		wantErr     bool
	}{
		{
			name:        "forbidden: upgrade in progress",
			err:         apierrors.NewForbidden(bootcv1alpha1.GroupVersion.WithResource("x").GroupResource(), "cluster", errors.New("no")),
			wantRequeue: statusRetryInterval,
		},
		{
			name:        "no status subresource yet",
			err:         apierrors.NewNotFound(bootcv1alpha1.GroupVersion.WithResource("x").GroupResource(), "cluster"),
			wantRequeue: statusRetryInterval,
		},
		{
			name:    "conflict",
			err:     apierrors.NewConflict(bootcv1alpha1.GroupVersion.WithResource("x").GroupResource(), "cluster", errors.New("stale")),
			wantErr: true,
		},
		{name: "server error", err: apierrors.NewInternalError(errors.New("boom")), wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			c := fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithObjects(cfg.DeepCopy()).
				WithStatusSubresource(&bootcv1alpha1.BootcOperatorConfig{}).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(
						context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption,
					) error {
						return tt.err
					},
				}).Build()
			r := newStatusReconciler(c,
				&stubConfigState{state: config.State{Active: cfg.DeepCopy()}},
				&stubNamespaces{statuses: []pullsecret.NamespaceStatus{listable("operator")}})

			result, err := r.Reconcile(context.Background(),
				ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cfg)})

			if tt.wantErr {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result.RequeueAfter).To(Equal(tt.wantRequeue))
		})
	}
}

func TestConfigStatusReconcilerNotifyDoesNotBlock(t *testing.T) {
	g := NewWithT(t)
	r := &ConfigStatusReconciler{changes: make(chan event.GenericEvent, 1)}

	r.Notify()
	r.Notify()
	g.Expect(r.changes).To(HaveLen(1))

	g.Expect((&ConfigStatusReconciler{}).Notify).NotTo(Panic(), "before setup")
}
