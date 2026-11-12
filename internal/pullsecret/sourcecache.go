// SPDX-License-Identifier: Apache-2.0

package pullsecret

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// DefaultRecheckInterval is how often the SourceCache checks that it
	// can still list the Secrets of the namespaces it watches, and retries
	// the ones it could not.
	DefaultRecheckInterval = 30 * time.Second

	// activationTimeout bounds the wait for a namespace's Secrets to be
	// listed for the first time.
	activationTimeout = 30 * time.Second

	waitingForAccessCheck = "waiting to check access"
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

	// Namespaces returns the namespaces pools may reference pull secrets
	// from. A namespace on the list may still be unavailable, see Get.
	Namespaces() []string

	// Source returns a controller source that delivers events for the
	// source Secrets to h.
	Source(h handler.TypedEventHandler[client.Object, reconcile.Request]) source.SyncingSource

	// NamespaceSource returns a controller source that delivers an event to
	// h, for a Namespace object named after it, whenever the availability of
	// a namespace's Secrets changes: they become available or unavailable,
	// the watch fails or recovers, or the namespace is added to or removed
	// from Namespaces.
	NamespaceSource(
		h handler.TypedEventHandler[client.Object, reconcile.Request],
	) source.SyncingSource
}

// NamespaceUnavailableError is returned by SourceCache.Get for a namespace
// that is allowed, but whose Secrets the controller cannot list, or not yet.
type NamespaceUnavailableError struct {
	Namespace string
	Reason    string
}

func (e *NamespaceUnavailableError) Error() string {
	return fmt.Sprintf("pull secret namespace %q is not available: %s", e.Namespace, e.Reason)
}

// NamespaceStatus describes what the SourceCache can do in a namespace.
type NamespaceStatus struct {
	Namespace string

	// Listable is whether the controller can list the Secrets there.
	Listable bool

	// WatchHealthy is whether the watch of the Secrets works. Reads go to the
	// API server while it does not. Only meaningful when Listable.
	WatchHealthy bool

	// Reason says why the namespace is not Listable.
	Reason string
}

// SourceCache is the SourceSecrets of the controller. It keeps one Secret
// cache per namespace it can list Secrets in, owned by the controller rather
// than the manager, so the allowed namespaces can change while it runs: a
// namespace's cache is started once the controller is known to have access,
// and stopped when the access is lost or the namespace is no longer allowed.
//
// Access is checked every RecheckInterval with a one-item list that does not
// read Secret data. A namespace that is denied does not stop the others from
// working. The controller needs no permission besides the get, list and watch
// on Secrets that the administrator grants in each namespace.
//
// Reads are served from a namespace's cache while its watch is healthy and
// from the API server while it is not, since a cache that cannot follow the
// Secrets may be stale.
type SourceCache struct {
	// RecheckInterval is how often access is checked. Set it before Start.
	RecheckInterval time.Duration

	apiReader client.Reader
	log       logr.Logger
	newCache  func(namespace string, onWatchError func()) (cache.Cache, error)

	started   chan struct{}
	startOnce sync.Once
	recheck   chan struct{}

	mu      sync.RWMutex
	ctx     context.Context
	desired []string
	entries map[string]*nsEntry
	reasons map[string]string
	secrets []*subscription
	nsSubs  []*subscription
}

var (
	_ SourceSecrets    = (*SourceCache)(nil)
	_ manager.Runnable = (*SourceCache)(nil)
)

// nsEntry is the running cache of one namespace.
type nsEntry struct {
	cache    cache.Cache
	informer cache.Informer
	cancel   context.CancelFunc

	// lastWatchError is when the watch last failed, in Unix nanoseconds, or
	// zero.
	lastWatchError atomic.Int64

	// reportedHealthy is the health subscribers were last told. Guarded by
	// SourceCache.mu.
	reportedHealthy bool
}

// healthy reports whether the watch has been free of failures for window. A
// failing watch retries with a backoff of up to 30 seconds, so a recheck
// interval of silence is as good a sign of recovery as there is without
// asking the cache.
func (e *nsEntry) healthy(window time.Duration) bool {
	last := e.lastWatchError.Load()
	return last == 0 || time.Since(time.Unix(0, last)) > window
}

// subscription is a controller's registration for events.
type subscription struct {
	ctx     context.Context
	handler handler.TypedEventHandler[client.Object, reconcile.Request]
	queue   workqueue.TypedRateLimitingInterface[reconcile.Request]
}

// NewSourceCache returns a SourceCache for the given namespaces and adds it
// to the manager. The manager's own Secret cache is not involved.
func NewSourceCache(
	mgr ctrl.Manager,
	namespaces []string,
	log logr.Logger,
) (*SourceCache, error) {
	c := &SourceCache{
		RecheckInterval: DefaultRecheckInterval,
		apiReader:       mgr.GetAPIReader(),
		log:             log,
		started:         make(chan struct{}),
		recheck:         make(chan struct{}, 1),
		desired:         slices.Clone(namespaces),
		entries:         map[string]*nsEntry{},
		reasons:         map[string]string{},
	}
	for _, ns := range namespaces {
		c.reasons[ns] = waitingForAccessCheck
	}
	c.newCache = func(namespace string, onWatchError func()) (cache.Cache, error) {
		return cache.New(mgr.GetConfig(), cache.Options{
			Scheme:            mgr.GetScheme(),
			HTTPClient:        mgr.GetHTTPClient(),
			Mapper:            mgr.GetRESTMapper(),
			DefaultNamespaces: map[string]cache.Config{namespace: {}},
			DefaultWatchErrorHandler: func(
				ctx context.Context,
				r *toolscache.Reflector,
				err error,
			) {
				toolscache.DefaultWatchErrorHandler(ctx, r, err)
				onWatchError()
			},
		})
	}
	if err := mgr.Add(c); err != nil {
		return nil, err
	}
	return c, nil
}

// Namespaces returns the allowed namespaces.
func (c *SourceCache) Namespaces() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.desired)
}

// SetNamespaces changes the allowed namespaces. The caches of the ones removed
// stop at once, and their Secrets cannot be read from then on. The ones added
// start when the controller is found to have access, which is checked right
// away, and again every RecheckInterval if it does not.
func (c *SourceCache) SetNamespaces(namespaces []string) {
	namespaces = slices.Clone(namespaces)

	c.mu.Lock()
	if slices.Equal(c.desired, namespaces) {
		c.mu.Unlock()
		return
	}
	previous := c.desired
	c.desired = namespaces
	var changed []string
	for _, ns := range previous {
		if !slices.Contains(namespaces, ns) {
			c.stopLocked(ns)
			delete(c.reasons, ns)
			changed = append(changed, ns)
		}
	}
	for _, ns := range namespaces {
		if !slices.Contains(previous, ns) {
			c.reasons[ns] = waitingForAccessCheck
			changed = append(changed, ns)
		}
	}
	c.mu.Unlock()

	for _, ns := range changed {
		c.notify(ns)
	}
	select {
	case c.recheck <- struct{}{}:
	default:
	}
}

// Statuses returns the status of each allowed namespace.
func (c *SourceCache) Statuses() []NamespaceStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	statuses := make([]NamespaceStatus, 0, len(c.desired))
	for _, ns := range c.desired {
		status := NamespaceStatus{Namespace: ns, Reason: c.reasons[ns]}
		if e := c.entries[ns]; e != nil {
			status.Listable = true
			status.WatchHealthy = e.healthy(c.healthWindow())
			status.Reason = ""
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func (c *SourceCache) healthWindow() time.Duration { return 2 * c.RecheckInterval }

// Get reads a Secret. In an available namespace it comes from the cache, or
// from the API server while the watch is failing. Otherwise the error is a
// *NamespaceUnavailableError.
func (c *SourceCache) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	c.mu.RLock()
	e := c.entries[key.Namespace]
	reason := c.reasons[key.Namespace]
	window := c.healthWindow()
	c.mu.RUnlock()

	switch {
	case e == nil:
		if reason == "" {
			reason = "not an allowed namespace"
		}
		return &NamespaceUnavailableError{Namespace: key.Namespace, Reason: reason}
	case e.healthy(window):
		return e.cache.Get(ctx, key, obj, opts...)
	default:
		return c.apiReader.Get(ctx, key, obj, opts...)
	}
}

// Source returns a source that delivers the events of the Secrets in every
// available namespace to h. It reports synced once the namespaces that were
// available at startup are.
func (c *SourceCache) Source(
	h handler.TypedEventHandler[client.Object, reconcile.Request],
) source.SyncingSource {
	return &sourceCacheSource{cache: c, handler: h, secrets: true}
}

// NamespaceSource returns a source that reports changes in the availability
// of namespaces to h.
func (c *SourceCache) NamespaceSource(
	h handler.TypedEventHandler[client.Object, reconcile.Request],
) source.SyncingSource {
	return &sourceCacheSource{cache: c, handler: h}
}

type sourceCacheSource struct {
	cache   *SourceCache
	handler handler.TypedEventHandler[client.Object, reconcile.Request]
	secrets bool
}

func (s *sourceCacheSource) Start(
	ctx context.Context,
	queue workqueue.TypedRateLimitingInterface[reconcile.Request],
) error {
	s.cache.subscribe(&subscription{ctx: ctx, handler: s.handler, queue: queue}, s.secrets)
	return nil
}

func (s *sourceCacheSource) WaitForSync(ctx context.Context) error {
	select {
	case <-s.cache.started:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *SourceCache) subscribe(sub *subscription, secrets bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !secrets {
		c.nsSubs = append(c.nsSubs, sub)
		return
	}
	c.secrets = append(c.secrets, sub)
	for _, e := range c.entries {
		c.attach(e, sub)
	}
}

// attach delivers the events of the entry's Secrets to the subscription,
// starting with an add for each existing Secret.
func (c *SourceCache) attach(e *nsEntry, sub *subscription) {
	_, err := e.informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if o, ok := obj.(client.Object); ok {
				sub.handler.Create(
					sub.ctx,
					event.TypedCreateEvent[client.Object]{Object: o},
					sub.queue,
				)
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			o, okOld := oldObj.(client.Object)
			n, okNew := newObj.(client.Object)
			if okOld && okNew {
				sub.handler.Update(sub.ctx,
					event.TypedUpdateEvent[client.Object]{ObjectOld: o, ObjectNew: n}, sub.queue)
			}
		},
		DeleteFunc: func(obj any) {
			if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			if o, ok := obj.(client.Object); ok {
				sub.handler.Delete(
					sub.ctx,
					event.TypedDeleteEvent[client.Object]{Object: o},
					sub.queue,
				)
			}
		},
	})
	if err != nil {
		c.log.Error(err, "Failed to watch Secrets for a controller")
	}
}

// notify tells the namespace subscribers that something changed in ns.
func (c *SourceCache) notify(ns string) {
	c.mu.RLock()
	subs := slices.Clone(c.nsSubs)
	c.mu.RUnlock()

	obj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	for _, sub := range subs {
		sub.handler.Generic(sub.ctx, event.TypedGenericEvent[client.Object]{Object: obj}, sub.queue)
	}
}

// NeedLeaderElection returns false: like the manager's caches, the Secret
// caches run on every replica, ready for when it becomes the leader.
func (c *SourceCache) NeedLeaderElection() bool { return false }

// Start checks access and runs the caches of the namespaces it has until ctx
// is done.
func (c *SourceCache) Start(ctx context.Context) error {
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
	defer c.stopAll()

	c.sync(ctx)
	c.startOnce.Do(func() { close(c.started) })

	ticker := time.NewTicker(c.RecheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-c.recheck:
		}
		c.sync(ctx)
	}
}

func (c *SourceCache) stopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ns := range c.entries {
		c.stopLocked(ns)
	}
}

// stopLocked stops the cache of ns. c.mu must be held.
func (c *SourceCache) stopLocked(ns string) {
	if e := c.entries[ns]; e != nil {
		e.cancel()
		delete(c.entries, ns)
	}
}

// sync brings the caches in line with the allowed namespaces and the access
// the controller has to them.
func (c *SourceCache) sync(ctx context.Context) {
	for _, ns := range c.Namespaces() {
		if ctx.Err() != nil {
			return
		}
		err := c.checkAccess(ctx, ns)

		c.mu.RLock()
		e := c.entries[ns]
		c.mu.RUnlock()

		switch {
		case err == nil && e == nil:
			c.activate(ctx, ns)
		case err == nil:
			c.updateHealth(ns, e)
		case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
			c.deny(ns, fmt.Sprintf("the controller cannot list Secrets: %v", err))
		default:
			// Neither access nor its loss is shown. Keep what works.
			c.log.Error(err, "Failed to check access to Secrets", "namespace", ns)
			if e == nil {
				c.deny(ns, fmt.Sprintf("checking access failed: %v", err))
			}
		}
	}
}

// checkAccess lists at most one Secret's metadata in ns.
func (c *SourceCache) checkAccess(ctx context.Context, ns string) error {
	list := &metav1.PartialObjectMetadataList{}
	list.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("SecretList"))
	return c.apiReader.List(ctx, list, client.InNamespace(ns), client.Limit(1))
}

// deny records that ns is unavailable, and stops its cache if it was running.
func (c *SourceCache) deny(ns, reason string) {
	c.mu.Lock()
	if !slices.Contains(c.desired, ns) {
		c.mu.Unlock()
		return
	}
	changed := c.entries[ns] != nil || c.reasons[ns] != reason
	c.stopLocked(ns)
	c.reasons[ns] = reason
	c.mu.Unlock()

	if changed {
		c.log.Info("Pull secret namespace is not available", "namespace", ns, "reason", reason)
		c.notify(ns)
	}
}

// activate starts the cache of ns and waits for its Secrets to be listed.
func (c *SourceCache) activate(ctx context.Context, ns string) {
	e := &nsEntry{}
	nsCache, err := c.newCache(ns, func() {
		e.lastWatchError.Store(time.Now().UnixNano())
		c.updateHealth(ns, e)
	})
	if err != nil {
		c.deny(ns, fmt.Sprintf("creating the Secret cache failed: %v", err))
		return
	}

	cacheCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := nsCache.Start(cacheCtx); err != nil {
			c.log.Error(err, "Secret cache stopped", "namespace", ns)
		}
	}()

	syncCtx, cancelSync := context.WithTimeout(cacheCtx, activationTimeout)
	defer cancelSync()
	informer, err := nsCache.GetInformer(syncCtx, &corev1.Secret{})
	if err != nil {
		cancel()
		c.deny(ns, fmt.Sprintf("listing Secrets failed: %v", err))
		return
	}
	e.cache, e.informer, e.cancel, e.reportedHealthy = nsCache, informer, cancel, true

	c.mu.Lock()
	if !slices.Contains(c.desired, ns) || c.entries[ns] != nil {
		c.mu.Unlock()
		cancel()
		return
	}
	c.entries[ns] = e
	delete(c.reasons, ns)
	for _, sub := range c.secrets {
		c.attach(e, sub)
	}
	c.mu.Unlock()

	c.log.Info("Watching pull secret namespace", "namespace", ns)
	c.notify(ns)
}

// updateHealth tells the namespace subscribers when the health of the watch
// differs from what they were last told.
func (c *SourceCache) updateHealth(ns string, e *nsEntry) {
	c.mu.Lock()
	if c.entries[ns] != e {
		c.mu.Unlock()
		return
	}
	healthy := e.healthy(c.healthWindow())
	changed := healthy != e.reportedHealthy
	e.reportedHealthy = healthy
	c.mu.Unlock()

	if changed {
		c.log.Info("Pull secret namespace watch health changed",
			"namespace", ns, "healthy", healthy)
		c.notify(ns)
	}
}
