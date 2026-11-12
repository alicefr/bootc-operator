// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// DefaultTagResolutionPeriodSeconds is the default interval between resolving image tags.
	DefaultTagResolutionPeriodSeconds int32 = 300

	// DefaultStatusPollPeriodSeconds is the default interval between fallback bootc status polls.
	DefaultStatusPollPeriodSeconds int32 = 300
)

// OperatorControllerConfig configures the operator controller.
type OperatorControllerConfig struct {
	// allowInsecureRegistry allows falling back to HTTP when accessing image
	// registries. Enable this only for trusted registries that do not support TLS.
	// Changes apply to the running controller, which resolves the tags of
	// all pools again.
	// +optional
	AllowInsecureRegistry *bool `json:"allowInsecureRegistry,omitempty"`

	// tagResolutionPeriodSeconds is the interval in seconds between resolving
	// image tags to digests. When omitted, use the CLI value or 300 seconds.
	// Changes apply to the running controller, which resolves the tags of
	// all pools again.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TagResolutionPeriodSeconds *int32 `json:"tagResolutionPeriodSeconds,omitempty"`

	// pullSecretNamespaces lists extra namespaces whose Secrets may be referenced
	// by pools. The operator namespace is always allowed. Omission preserves CLI
	// settings; an empty list allows no extras. Administrators must grant Secret get/list/watch access
	// in each extra namespace separately. Changes require a controller restart.
	// +optional
	// +listType=set
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	PullSecretNamespaces []string `json:"pullSecretNamespaces,omitzero"`
}

// OperatorDaemonConfig configures each node daemon.
type OperatorDaemonConfig struct {
	// statusPollPeriodSeconds is the interval in seconds between bootc status
	// polls when filesystem notifications are unavailable. When omitted, use the
	// CLI value or 300 seconds. Changes apply to the running daemons.
	// +optional
	// +kubebuilder:validation:Minimum=1
	StatusPollPeriodSeconds *int32 `json:"statusPollPeriodSeconds,omitempty"`
}

// BootcOperatorConfigSpec defines operator settings. The controller and the
// daemons apply changes while they run, without a restart.
type BootcOperatorConfigSpec struct {
	// controller configures the controller. Omitted settings use CLI values or built-in defaults.
	// +optional
	// +kubebuilder:default={}
	Controller *OperatorControllerConfig `json:"controller,omitempty"`

	// daemon configures all node daemons. Omitted settings use CLI values or built-in defaults.
	// +optional
	// +kubebuilder:default={}
	Daemon *OperatorDaemonConfig `json:"daemon,omitempty"`
}

// BootcOperatorConfig condition types and reasons.
const (
	// ConfigControllerReady is true when the controller applies the
	// configuration and can use everything it asks for: it can list the
	// Secrets in every pull secret namespace, and their watches are healthy.
	ConfigControllerReady string = "ControllerReady"

	// ConfigReasonReady means the controller applies the configuration in full.
	ConfigReasonReady string = "Ready"

	// ConfigReasonPendingNamespaces means the controller cannot list the
	// Secrets in some pull secret namespaces yet. The administrator has to
	// grant it access there. It tries again about every 30 seconds, and applies
	// the rest of the configuration meanwhile.
	ConfigReasonPendingNamespaces string = "PendingNamespaces"

	// ConfigReasonWatchUnhealthy means the watch of the Secrets in some
	// pull secret namespaces is failing. The controller reads them from the
	// API server until it recovers.
	ConfigReasonWatchUnhealthy string = "WatchUnhealthy"

	// ConfigReasonMultipleConfigs means more than one BootcOperatorConfig
	// exists. The controller keeps the settings it had, and applies none of them,
	// until exactly one remains.
	ConfigReasonMultipleConfigs string = "MultipleConfigs"
)

// BootcOperatorConfigStatus is what the controller reports about the
// configuration. Only the controller writes it, and it says nothing about the
// daemons, the aggregated pull secret or the nodes.
type BootcOperatorConfigStatus struct {
	// observedGeneration is the generation of the configuration the controller
	// last applied.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe whether the controller applies the configuration in
	// full. The known condition type is "ControllerReady".
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// listablePullSecretNamespaces lists the namespaces, the operator's own
	// included, whose Secrets the controller can list right now. A namespace in
	// the configuration that is missing here is named in the ControllerReady
	// condition.
	// +listType=set
	// +optional
	ListablePullSecretNamespaces []string `json:"listablePullSecretNamespaces,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// BootcOperatorConfig configures the controller and node daemons in a cluster.
// The optional resource is administrator-owned and may have any valid name.
// The operator rejects multiple configurations at startup. While it runs with
// more than one, it keeps the settings it had until exactly one remains.
// Changes apply to the running controller and daemons; supplied fields take
// precedence over CLI flags, and omitted ones, or all of them once the
// configuration is deleted, revert to them.
type BootcOperatorConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the configuration the operator applies.
	// +required
	Spec BootcOperatorConfigSpec `json:"spec"`

	// status reports what the controller does with the configuration.
	// +optional
	Status BootcOperatorConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// BootcOperatorConfigList contains a list of BootcOperatorConfig.
type BootcOperatorConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BootcOperatorConfig `json:"items"`
}
