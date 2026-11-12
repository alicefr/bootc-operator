// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
	"github.com/bootc-dev/bootc-operator/internal/config"
	"github.com/bootc-dev/bootc-operator/internal/pullsecret"
)

// statusRetryInterval is how long to wait before writing a status again after
// the controller was refused: it may lack the permission during an upgrade.
const statusRetryInterval = time.Minute

// ConfigState is what the controller currently applies of the configuration.
type ConfigState interface {
	State() config.State
}

// PullSecretNamespaces reports on the pull secret namespaces.
type PullSecretNamespaces interface {
	Statuses() []pullsecret.NamespaceStatus
	NamespaceSource(
		h handler.TypedEventHandler[client.Object, reconcile.Request],
	) source.SyncingSource
}

// ConfigStatusReconciler reports on every BootcOperatorConfig what the
// controller does with it: the generation it applied, whether it can use
// everything the configuration asks for, and the namespaces it can list
// Secrets in. Only the elected controller writes the status, and only when it
// changes. Applying the configuration never waits for it.
type ConfigStatusReconciler struct {
	client.Client
	Config     ConfigState
	Namespaces PullSecretNamespaces

	// changes receives an event when the applied configuration changes.
	changes chan event.GenericEvent
}

// +kubebuilder:rbac:groups=node.bootc.dev,resources=bootcoperatorconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=node.bootc.dev,resources=bootcoperatorconfigs/status,verbs=get;update;patch

// SetupWithManager sets up the reconciler with the Manager.
func (r *ConfigStatusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.changes = make(chan event.GenericEvent, 1)
	enqueueAll := handler.EnqueueRequestsFromMapFunc(r.mapToAllConfigs)
	return ctrl.NewControllerManagedBy(mgr).
		Named("bootcoperatorconfig").
		// Status writes do not change the generation, so they cause no loop.
		For(&bootcv1alpha1.BootcOperatorConfig{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WatchesRawSource(source.Channel(r.changes, enqueueAll)).
		WatchesRawSource(r.Namespaces.NamespaceSource(enqueueAll)).
		Complete(r)
}

// Notify reports that the configuration the controller applies changed. It
// does not block, so a Watcher can call it.
func (r *ConfigStatusReconciler) Notify() {
	if r.changes == nil {
		return
	}
	select {
	case r.changes <- event.GenericEvent{Object: &bootcv1alpha1.BootcOperatorConfig{}}:
	default:
	}
}

// mapToAllConfigs enqueues every configuration: the status of each depends on
// what applies to all of them.
func (r *ConfigStatusReconciler) mapToAllConfigs(
	ctx context.Context,
	_ client.Object,
) []reconcile.Request {
	var configs bootcv1alpha1.BootcOperatorConfigList
	if err := r.List(ctx, &configs); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list BootcOperatorConfigs")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(configs.Items))
	for i := range configs.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&configs.Items[i]),
		})
	}
	return requests
}

// Reconcile writes the status of a configuration if it differs from what the
// controller does.
func (r *ConfigStatusReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cfg bootcv1alpha1.BootcOperatorConfig
	if err := r.Get(ctx, req.NamespacedName, &cfg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !cfg.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	status, ok := desiredConfigStatus(&cfg, r.Config.State(), r.Namespaces.Statuses())
	if !ok || equality.Semantic.DeepEqual(cfg.Status, status) {
		return ctrl.Result{}, nil
	}

	cfg.Status = status
	if err := r.Status().Update(ctx, &cfg); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			// Deleted meanwhile, or the CRD has no status subresource yet.
			log.Info("Not writing the BootcOperatorConfig status", "reason", err.Error())
			return ctrl.Result{RequeueAfter: statusRetryInterval}, nil
		case apierrors.IsForbidden(err):
			log.Info("Not allowed to write the BootcOperatorConfig status, upgrade the RBAC",
				"error", err.Error())
			return ctrl.Result{RequeueAfter: statusRetryInterval}, nil
		}
		// A conflict, or a failure that may pass: try again with backoff.
		return ctrl.Result{}, fmt.Errorf("updating BootcOperatorConfig status: %w", err)
	}
	return ctrl.Result{}, nil
}

// desiredConfigStatus returns the status the configuration should have, or
// false when it is not known yet: the namespaces were not checked, or the
// controller did not apply this generation of the configuration, in which case
// the watcher reports once it does.
func desiredConfigStatus(
	cfg *bootcv1alpha1.BootcOperatorConfig,
	state config.State,
	namespaces []pullsecret.NamespaceStatus,
) (bootcv1alpha1.BootcOperatorConfigStatus, bool) {
	status := *cfg.Status.DeepCopy()
	condition := metav1.Condition{
		Type:               bootcv1alpha1.ConfigControllerReady,
		ObservedGeneration: cfg.Generation,
	}

	var listable, pending, unhealthy []string
	for _, ns := range namespaces {
		switch {
		case ns.Checking:
			return status, false
		case !ns.Listable:
			pending = append(pending, fmt.Sprintf("%s (%s)", ns.Namespace, ns.Reason))
		default:
			listable = append(listable, ns.Namespace)
			if !ns.WatchHealthy {
				unhealthy = append(unhealthy, ns.Namespace)
			}
		}
	}
	slices.Sort(listable)
	status.ListablePullSecretNamespaces = listable

	switch {
	case slices.Contains(state.Conflicts, cfg.Name):
		condition.Status = metav1.ConditionFalse
		condition.Reason = bootcv1alpha1.ConfigReasonMultipleConfigs
		condition.Message = fmt.Sprintf(
			"Multiple BootcOperatorConfigs exist (%s). The controller keeps the settings it "+
				"had until exactly one remains.",
			strings.Join(state.Conflicts, ", "),
		)
	case !appliesConfig(state, cfg):
		return status, false
	default:
		status.ObservedGeneration = cfg.Generation
		switch {
		case len(pending) > 0:
			condition.Status = metav1.ConditionFalse
			condition.Reason = bootcv1alpha1.ConfigReasonPendingNamespaces
			condition.Message = "The controller cannot list Secrets in: " +
				strings.Join(pending, ", ") + ". Grant it get, list and watch on Secrets there."
			if len(unhealthy) > 0 {
				condition.Message += " The watch of Secrets in " +
					strings.Join(unhealthy, ", ") + " is failing."
			}
		case len(unhealthy) > 0:
			condition.Status = metav1.ConditionFalse
			condition.Reason = bootcv1alpha1.ConfigReasonWatchUnhealthy
			condition.Message = "The watch of Secrets in " + strings.Join(unhealthy, ", ") +
				" is failing. The controller reads them from the API server meanwhile."
		default:
			condition.Status = metav1.ConditionTrue
			condition.Reason = bootcv1alpha1.ConfigReasonReady
			condition.Message = "The controller applies the configuration."
		}
	}
	apimeta.SetStatusCondition(&status.Conditions, condition)
	return status, true
}

// appliesConfig reports whether the state's settings come from this
// generation of the configuration.
func appliesConfig(state config.State, cfg *bootcv1alpha1.BootcOperatorConfig) bool {
	active := state.Active
	return active != nil && active.Name == cfg.Name && active.UID == cfg.UID &&
		active.Generation == cfg.Generation
}
