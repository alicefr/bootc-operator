// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"time"

	"github.com/distribution/reference"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
	"github.com/bootc-dev/bootc-operator/internal/config"
)

// insecureSetter is implemented by tag resolvers that can change whether they
// fall back to HTTP.
type insecureSetter interface {
	SetAllowInsecure(allowInsecure bool)
}

// ApplySettings applies the registry settings of next while the controller
// runs: whether to allow insecure registries, and how often to re-resolve
// tags. When one of them differs from prev, every pool with a tag-based
// image ref is reconciled right away and resolves its tag again, under the new
// settings, without waiting for the schedule the old ones set. Digest-based
// pools do not resolve anything and are left alone.
func (r *BootcNodePoolReconciler) ApplySettings(prev, next config.Settings) {
	if prev.AllowInsecureRegistry == next.AllowInsecureRegistry &&
		prev.TagResolutionInterval == next.TagResolutionInterval {
		return
	}

	r.settingsMu.Lock()
	r.TagResolutionInterval = next.TagResolutionInterval
	r.settingsMu.Unlock()
	if setter, ok := r.TagResolver.(insecureSetter); ok {
		setter.SetAllowInsecure(next.AllowInsecureRegistry)
	}

	// One pending event is enough: the mapper looks at all the pools when it
	// runs.
	select {
	case r.settingsCh <- event.GenericEvent{Object: &bootcv1alpha1.BootcNodePool{}}:
	default:
	}
}

func (r *BootcNodePoolReconciler) tagResolutionInterval() time.Duration {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	return r.TagResolutionInterval
}

// takeForceResolve reports whether the pool must resolve its tag now because
// the settings changed, and clears that.
func (r *BootcNodePoolReconciler) takeForceResolve(pool string) bool {
	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	_, ok := r.forceResolve[pool]
	delete(r.forceResolve, pool)
	return ok
}

// mapSettingsToPoolRequests enqueues the pools with a tag-based image ref, and
// marks them to resolve it on that reconcile.
func (r *BootcNodePoolReconciler) mapSettingsToPoolRequests(
	ctx context.Context,
	_ client.Object,
) []reconcile.Request {
	var pools bootcv1alpha1.BootcNodePoolList
	if err := r.List(ctx, &pools); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list BootcNodePools after a settings change")
		return nil
	}

	r.settingsMu.Lock()
	defer r.settingsMu.Unlock()
	if r.forceResolve == nil {
		r.forceResolve = map[string]struct{}{}
	}
	var requests []reconcile.Request
	for i := range pools.Items {
		pool := &pools.Items[i]
		if !isTagBased(pool) {
			continue
		}
		r.forceResolve[pool.Name] = struct{}{}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: pool.Name},
		})
	}
	return requests
}

// isTagBased reports whether the pool's image ref names a tag rather than a
// digest. A ref that does not parse is not: it fails on its own at reconcile.
func isTagBased(pool *bootcv1alpha1.BootcNodePool) bool {
	ref, err := parseImageRef(pool.Spec.Image.Ref)
	if err != nil {
		return false
	}
	_, digested := ref.(reference.Digested)
	return !digested
}
