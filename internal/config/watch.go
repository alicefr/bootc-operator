// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/go-logr/logr"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
)

// Informers provides the informer of a type. A controller-runtime cache is
// one.
type Informers interface {
	GetInformer(
		ctx context.Context,
		obj client.Object,
		opts ...cache.InformerGetOption,
	) (cache.Informer, error)
}

// State is what a Watcher currently applies.
type State struct {
	// Settings are the effective settings.
	Settings Settings

	// Active is the BootcOperatorConfig Settings come from, or nil when
	// none exists and the fallback applies.
	Active *bootcv1alpha1.BootcOperatorConfig

	// Conflicts holds the sorted names of the BootcOperatorConfigs when
	// more than one exists. The settings are then those of the last time
	// exactly one did, since there is no way to tell which one is meant.
	Conflicts []string
}

// changed reports whether s differs from other in a way a consumer cares
// about.
func (s State) changed(other State) bool {
	return !s.Settings.Equal(other.Settings) ||
		!slices.Equal(s.Conflicts, other.Conflicts) ||
		activeIdentity(s.Active) != activeIdentity(other.Active)
}

func activeIdentity(cfg *bootcv1alpha1.BootcOperatorConfig) string {
	if cfg == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%d", cfg.Name, cfg.UID, cfg.Generation)
}

// Watcher follows the BootcOperatorConfig resources while the process runs and
// derives the effective Settings from them. It is a manager Runnable.
//
// Without a configuration the settings are the fallback, which are the CLI
// values. With exactly one they are that configuration on top of the
// fallback. While more than one exists, or the single one is invalid, the
// last effective settings stay. API outages never change the settings: the
// informer relists after one, and only what the relist shows is applied, so a
// deletion is acted on once it is confirmed.
type Watcher struct {
	fallback  Settings
	informers Informers
	log       logr.Logger

	mu       sync.RWMutex
	state    State
	onChange []func(State)

	// handleMu serializes handle, so state changes are applied and reported
	// in the order of the events. It guards configs and synced.
	handleMu sync.Mutex
	configs  map[string]*bootcv1alpha1.BootcOperatorConfig
	synced   bool
}

// NewWatcher returns a Watcher that starts from initial, the configuration
// loaded at startup (see Load), or nil when there was none.
func NewWatcher(
	fallback Settings,
	initial *bootcv1alpha1.BootcOperatorConfig,
	informers Informers,
	log logr.Logger,
) *Watcher {
	return &Watcher{
		fallback:  fallback,
		informers: informers,
		log:       log,
		state: State{
			Settings: Resolve(fallback, initial),
			Active:   initial.DeepCopy(),
		},
		configs: map[string]*bootcv1alpha1.BootcOperatorConfig{},
	}
}

// State returns the current state.
func (w *Watcher) State() State {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.copyState()
}

// OnChange registers fn to be called whenever the state changes. Register
// before Start. fn runs on the watcher's goroutine, so it must not block.
func (w *Watcher) OnChange(fn func(State)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onChange = append(w.onChange, fn)
}

func (w *Watcher) copyState() State {
	s := w.state
	s.Settings.PullSecretNamespaces = slices.Clone(s.Settings.PullSecretNamespaces)
	s.Conflicts = slices.Clone(s.Conflicts)
	s.Active = s.Active.DeepCopy()
	return s
}

// NeedLeaderElection returns false: every replica applies the configuration,
// not only the leader.
func (w *Watcher) NeedLeaderElection() bool { return false }

// Start follows the configurations until ctx is done.
func (w *Watcher) Start(ctx context.Context) error {
	informer, err := w.informers.GetInformer(ctx, &bootcv1alpha1.BootcOperatorConfig{})
	if err != nil {
		return fmt.Errorf("getting BootcOperatorConfig informer: %w", err)
	}
	registration, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) { w.handle(obj, false) },
		UpdateFunc: func(_, obj any) {
			w.handle(obj, false)
		},
		DeleteFunc: func(obj any) { w.handle(obj, true) },
	})
	if err != nil {
		return fmt.Errorf("watching BootcOperatorConfig: %w", err)
	}
	defer func() { _ = informer.RemoveEventHandler(registration) }()

	if !toolscache.WaitForCacheSync(ctx.Done(), registration.HasSynced) {
		return nil
	}
	// The initial list was recorded without acting on it. A configuration
	// that was deleted after the startup read has no event, so settle on
	// what exists now.
	w.handle(nil, false)

	<-ctx.Done()
	return nil
}

// handle records an event and applies the configurations that exist. A nil
// obj only applies them, and does so once the initial list is in.
func (w *Watcher) handle(obj any, deleted bool) {
	w.handleMu.Lock()
	defer w.handleMu.Unlock()

	if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	if cfg, ok := obj.(*bootcv1alpha1.BootcOperatorConfig); ok {
		if deleted {
			delete(w.configs, cfg.Name)
		} else {
			w.configs[cfg.Name] = cfg
		}
	}
	if obj == nil {
		w.synced = true
	}
	if !w.synced {
		return
	}
	w.apply()
}

func (w *Watcher) apply() {
	w.mu.RLock()
	next := w.copyState()
	w.mu.RUnlock()

	switch len(w.configs) {
	case 0:
		next.Settings = Resolve(w.fallback, nil)
		next.Active = nil
		next.Conflicts = nil
	case 1:
		var cfg *bootcv1alpha1.BootcOperatorConfig
		for _, c := range w.configs {
			cfg = c
		}
		if err := validate(cfg); err != nil {
			w.log.Error(
				err,
				"Ignoring invalid operator configuration, keeping the current settings",
			)
			next.Conflicts = nil
			break
		}
		next.Settings = Resolve(w.fallback, cfg)
		next.Active = cfg.DeepCopy()
		next.Conflicts = nil
	default:
		names := make([]string, 0, len(w.configs))
		for name := range w.configs {
			names = append(names, name)
		}
		sort.Strings(names)
		next.Conflicts = names
	}

	w.mu.Lock()
	if !next.changed(w.state) {
		w.mu.Unlock()
		return
	}
	w.state = next
	callbacks := slices.Clone(w.onChange)
	published := w.copyState()
	w.mu.Unlock()

	if len(next.Conflicts) > 0 {
		w.log.Info("Multiple BootcOperatorConfigs exist, keeping the current settings until "+
			"exactly one remains", "configs", next.Conflicts)
	} else {
		w.log.Info("Applied operator configuration", "active", activeIdentity(next.Active))
	}
	for _, fn := range callbacks {
		fn(published)
	}
}
