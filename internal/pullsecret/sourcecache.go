// SPDX-License-Identifier: Apache-2.0

package pullsecret

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// SecretGetter reads one Secret.
type SecretGetter interface {
	Get(
		ctx context.Context,
		key client.ObjectKey,
		obj client.Object,
		opts ...client.GetOption,
	) error
}

// SourceSecrets gives access to the source secrets that pools reference: it
// reads them and reports their changes to a controller.
type SourceSecrets interface {
	SecretGetter

	// Source returns a controller source that delivers events for the
	// source Secrets to h.
	Source(h handler.TypedEventHandler[client.Object, reconcile.Request]) source.SyncingSource
}

// SourceCache is the SourceSecrets of the controller. It keeps one Secret
// cache per allowed namespace, owned by the controller rather than the
// manager, so a namespace's informer is the only thing that lists and
// watches Secrets there and can be started and stopped on its own.
type SourceCache struct {
	caches map[string]cache.Cache
}

var (
	_ SourceSecrets    = (*SourceCache)(nil)
	_ manager.Runnable = (*SourceCache)(nil)
)

// NewSourceCache returns a SourceCache for the given namespaces and adds it
// to the manager. The manager's own Secret cache is not involved.
func NewSourceCache(mgr ctrl.Manager, namespaces []string) (*SourceCache, error) {
	c := &SourceCache{caches: make(map[string]cache.Cache, len(namespaces))}
	for _, ns := range namespaces {
		nsCache, err := cache.New(mgr.GetConfig(), cache.Options{
			Scheme:            mgr.GetScheme(),
			HTTPClient:        mgr.GetHTTPClient(),
			Mapper:            mgr.GetRESTMapper(),
			DefaultNamespaces: map[string]cache.Config{ns: {}},
		})
		if err != nil {
			return nil, fmt.Errorf("creating Secret cache for namespace %q: %w", ns, err)
		}
		c.caches[ns] = nsCache
	}
	if err := mgr.Add(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Get reads a Secret from the cache of its namespace.
func (c *SourceCache) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	nsCache, ok := c.caches[key.Namespace]
	if !ok {
		return fmt.Errorf("no Secret cache for namespace %q", key.Namespace)
	}
	return nsCache.Get(ctx, key, obj, opts...)
}

// Source returns a source that delivers the events of every namespace's
// Secret informer to h, and reports synced once all of them are.
func (c *SourceCache) Source(
	h handler.TypedEventHandler[client.Object, reconcile.Request],
) source.SyncingSource {
	sources := make([]source.SyncingSource, 0, len(c.caches))
	for _, nsCache := range c.caches {
		sources = append(sources, source.Kind(nsCache, client.Object(&corev1.Secret{}), h))
	}
	return multiSource(sources)
}

// Start runs the caches until ctx is done, or one of them fails.
func (c *SourceCache) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		once     sync.Once
		firstErr error
	)
	for _, nsCache := range c.caches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := nsCache.Start(ctx); err != nil {
				once.Do(func() { firstErr = err })
				cancel()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// NeedLeaderElection returns false: like the manager's caches, the Secret
// caches run on every replica, ready for when it becomes the leader.
func (c *SourceCache) NeedLeaderElection() bool { return false }

// multiSource combines sources into one.
type multiSource []source.SyncingSource

func (m multiSource) Start(
	ctx context.Context,
	queue workqueue.TypedRateLimitingInterface[reconcile.Request],
) error {
	for _, s := range m {
		if err := s.Start(ctx, queue); err != nil {
			return err
		}
	}
	return nil
}

func (m multiSource) WaitForSync(ctx context.Context) error {
	for _, s := range m {
		if err := s.WaitForSync(ctx); err != nil {
			return err
		}
	}
	return nil
}
