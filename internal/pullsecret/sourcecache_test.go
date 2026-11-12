// SPDX-License-Identifier: Apache-2.0

package pullsecret

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const recheck = 150 * time.Millisecond

var sourceCacheTestID atomic.Int32

// requestRecorder is a reconciler that records the requests it receives.
type requestRecorder struct {
	mu       sync.Mutex
	requests []types.NamespacedName
}

func (r *requestRecorder) Reconcile(_ context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req.NamespacedName)
	return ctrl.Result{}, nil
}

func (r *requestRecorder) seen() []types.NamespacedName {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.NamespacedName(nil), r.requests...)
}

// sourceCacheFixture is a SourceCache that runs as a user with no permissions
// but those the test grants, and the requests its two sources produced:
// {namespace, name} for a Secret, and {"", "namespace/<ns>"} for a change in
// the availability of a namespace.
type sourceCacheFixture struct {
	*SourceCache
	user     string
	recorder *requestRecorder
}

func newSourceCacheFixture(t *testing.T, namespaces ...string) *sourceCacheFixture {
	t.Helper()
	g := NewWithT(t)
	id := sourceCacheTestID.Add(1)
	user := fmt.Sprintf("source-cache-user-%d", id)
	authenticated, err := testEnv.AddUser(envtest.User{Name: user}, nil)
	g.Expect(err).NotTo(HaveOccurred())

	for _, ns := range namespaces {
		createNamespace(t, ns)
	}
	mgr, err := ctrl.NewManager(authenticated.Config(), ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	g.Expect(err).NotTo(HaveOccurred())
	sourceCache, err := NewSourceCache(mgr, namespaces, logr.Discard())
	g.Expect(err).NotTo(HaveOccurred())
	sourceCache.RecheckInterval = recheck

	recorder := &requestRecorder{}
	g.Expect(ctrl.NewControllerManagedBy(mgr).
		Named(fmt.Sprintf("sourcecache-test-%d", id)).
		WatchesRawSource(sourceCache.Source(handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
			}))).
		WatchesRawSource(sourceCache.NamespaceSource(handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{Name: "namespace/" + obj.GetName()},
				}}
			}))).
		Complete(recorder)).To(Succeed())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return &sourceCacheFixture{SourceCache: sourceCache, user: user, recorder: recorder}
}

func createNamespace(t *testing.T, name string) {
	t.Helper()
	g := NewWithT(t)
	g.Expect(client.IgnoreAlreadyExists(k8sClient.Create(context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))).To(Succeed())
}

// grant lets the fixture's user get, list and watch Secrets in ns, and returns
// a function that takes the access away.
func (f *sourceCacheFixture) grant(t *testing.T, ns string) (revoke func()) {
	t.Helper()
	g := NewWithT(t)
	ctx := context.Background()
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: f.user},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get", "list", "watch"},
		}},
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: f.user},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: f.user},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: f.user}},
	}
	g.Expect(k8sClient.Create(ctx, role)).To(Succeed())
	g.Expect(k8sClient.Create(ctx, binding)).To(Succeed())
	return func() {
		g.Expect(k8sClient.Delete(ctx, binding)).To(Succeed())
	}
}

func (f *sourceCacheFixture) status(ns string) func() (NamespaceStatus, error) {
	return func() (NamespaceStatus, error) {
		for _, s := range f.Statuses() {
			if s.Namespace == ns {
				return s, nil
			}
		}
		return NamespaceStatus{}, fmt.Errorf("namespace %s is not allowed", ns)
	}
}

func (f *sourceCacheFixture) data(ns, name string) func() (string, error) {
	return func() (string, error) {
		var secret corev1.Secret
		err := f.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &secret)
		return string(secret.Data["k"]), err
	}
}

func (f *sourceCacheFixture) changed(ns string) func() []types.NamespacedName {
	return func() []types.NamespacedName { return f.recorder.seen() }
}

func createSourceSecret(t *testing.T, ns, name, value string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Data:       map[string][]byte{"k": []byte(value)},
	}
	NewWithT(t).Expect(k8sClient.Create(context.Background(), secret)).To(Succeed())
	return secret
}

func namespaceChange(ns string) types.NamespacedName {
	return types.NamespacedName{Name: "namespace/" + ns}
}

func TestSourceCacheServesOnlyNamespacesItCanList(t *testing.T) {
	g := NewWithT(t)
	f := newSourceCacheFixture(t, "sc-listable", "sc-denied")
	f.grant(t, "sc-listable")
	createSourceSecret(t, "sc-listable", "ps", "v1")
	createSourceSecret(t, "sc-denied", "ps", "v1")

	g.Eventually(f.data("sc-listable", "ps"), pollTimeout, pollInterval).Should(Equal("v1"))
	g.Eventually(f.status("sc-listable"), pollTimeout, pollInterval).Should(And(
		HaveField("Listable", BeTrue()), HaveField("WatchHealthy", BeTrue())))

	// The denied namespace does not stop the other one, and says why.
	g.Eventually(f.status("sc-denied"), pollTimeout, pollInterval).Should(And(
		HaveField("Listable", BeFalse()),
		HaveField("Reason", ContainSubstring("cannot list Secrets"))))
	_, err := f.data("sc-denied", "ps")()
	var unavailable *NamespaceUnavailableError
	g.Expect(errors.As(err, &unavailable)).To(BeTrue())
	g.Expect(unavailable.Namespace).To(Equal("sc-denied"))

	// Events come from the one that works.
	g.Eventually(f.changed("sc-listable"), pollTimeout, pollInterval).Should(
		ContainElement(types.NamespacedName{Namespace: "sc-listable", Name: "ps"}))
	g.Expect(f.recorder.seen()).NotTo(ContainElement(
		types.NamespacedName{Namespace: "sc-denied", Name: "ps"}))
}

func TestSourceCacheActivatesNamespaceOnceAccessIsGranted(t *testing.T) {
	g := NewWithT(t)
	f := newSourceCacheFixture(t, "sc-late")
	createSourceSecret(t, "sc-late", "ps", "v1")
	g.Eventually(f.status("sc-late"), pollTimeout, pollInterval).Should(
		HaveField("Listable", BeFalse()))

	f.grant(t, "sc-late")

	g.Eventually(f.data("sc-late", "ps"), pollTimeout, pollInterval).Should(Equal("v1"))
	g.Eventually(f.changed("sc-late"), pollTimeout, pollInterval).Should(
		ContainElement(namespaceChange("sc-late")))
	// Its Secrets are watched, too, so a rotation is reported.
	g.Eventually(f.changed("sc-late"), pollTimeout, pollInterval).Should(
		ContainElement(types.NamespacedName{Namespace: "sc-late", Name: "ps"}))
}

func TestSourceCacheDropsNamespaceWhenAccessIsRevoked(t *testing.T) {
	g := NewWithT(t)
	f := newSourceCacheFixture(t, "sc-revoked", "sc-kept")
	f.grant(t, "sc-kept")
	revoke := f.grant(t, "sc-revoked")
	createSourceSecret(t, "sc-revoked", "ps", "v1")
	createSourceSecret(t, "sc-kept", "ps", "v1")
	g.Eventually(f.data("sc-revoked", "ps"), pollTimeout, pollInterval).Should(Equal("v1"))
	g.Eventually(f.data("sc-kept", "ps"), pollTimeout, pollInterval).Should(Equal("v1"))

	revoke()

	g.Eventually(f.status("sc-revoked"), pollTimeout, pollInterval).Should(
		HaveField("Listable", BeFalse()))
	_, err := f.data("sc-revoked", "ps")()
	var unavailable *NamespaceUnavailableError
	g.Expect(errors.As(err, &unavailable)).To(BeTrue(), "stale cached data is not served")
	g.Expect(f.recorder.seen()).To(ContainElement(namespaceChange("sc-revoked")))
	g.Expect(f.data("sc-kept", "ps")()).To(Equal("v1"))
}

func TestSourceCacheFollowsAllowedNamespaces(t *testing.T) {
	g := NewWithT(t)
	f := newSourceCacheFixture(t, "sc-first")
	f.grant(t, "sc-first")
	createNamespace(t, "sc-second")
	f.grant(t, "sc-second")
	createSourceSecret(t, "sc-first", "ps", "v1")
	createSourceSecret(t, "sc-second", "ps", "v1")
	g.Eventually(f.data("sc-first", "ps"), pollTimeout, pollInterval).Should(Equal("v1"))

	f.SetNamespaces([]string{"sc-first", "sc-second"})
	g.Expect(f.Namespaces()).To(Equal([]string{"sc-first", "sc-second"}))
	g.Eventually(f.data("sc-second", "ps"), pollTimeout, pollInterval).Should(Equal("v1"))
	g.Expect(f.recorder.seen()).To(ContainElement(namespaceChange("sc-second")))

	// Removal takes effect at once, and its Secrets stop being reported.
	f.SetNamespaces([]string{"sc-first"})
	g.Expect(f.Namespaces()).To(Equal([]string{"sc-first"}))
	var unavailable *NamespaceUnavailableError
	_, err := f.data("sc-second", "ps")()
	g.Expect(errors.As(err, &unavailable)).To(BeTrue())
	g.Expect(unavailable.Reason).To(Equal("not an allowed namespace"))
	g.Expect(f.Statuses()).To(ConsistOf(HaveField("Namespace", "sc-first")))

	// Let what was already in flight be reported first.
	time.Sleep(500 * time.Millisecond)
	rotated := types.NamespacedName{Namespace: "sc-second", Name: "ps"}
	before := countOf(f.recorder.seen(), rotated)
	secret := &corev1.Secret{}
	g.Expect(k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: "sc-second", Name: "ps"}, secret)).To(Succeed())
	secret.Data["k"] = []byte("v2")
	g.Expect(k8sClient.Update(context.Background(), secret)).To(Succeed())
	g.Consistently(func() int { return countOf(f.recorder.seen(), rotated) },
		500*time.Millisecond, pollInterval).Should(Equal(before))

	// And the namespace can be added back.
	f.SetNamespaces([]string{"sc-first", "sc-second"})
	g.Eventually(f.data("sc-second", "ps"), pollTimeout, pollInterval).Should(Equal("v2"))
}

func TestSourceCacheReadsFromAPIWhileWatchFails(t *testing.T) {
	g := NewWithT(t)
	f := newSourceCacheFixture(t, "sc-watch")
	f.grant(t, "sc-watch")
	createSourceSecret(t, "sc-watch", "ps", "v1")
	g.Eventually(f.data("sc-watch", "ps"), pollTimeout, pollInterval).Should(Equal("v1"))

	f.mu.RLock()
	e := f.entries["sc-watch"]
	cached := e.cache
	f.mu.RUnlock()

	// A watch failure: reads must not trust the cache. Taking the cache away
	// shows they do not use it.
	f.mu.Lock()
	e.cache = nil
	f.mu.Unlock()
	e.lastWatchError.Store(time.Now().UnixNano())
	f.updateHealth("sc-watch", e)

	g.Expect(f.status("sc-watch")()).To(And(
		HaveField("Listable", BeTrue()), HaveField("WatchHealthy", BeFalse())))
	g.Expect(f.recorder.seen()).To(ContainElement(namespaceChange("sc-watch")))
	g.Expect(f.data("sc-watch", "ps")()).To(Equal("v1"), "read from the API server")

	// It recovers once the watch has been quiet for a while, and pools are
	// told so they read again.
	f.mu.Lock()
	e.cache = cached
	f.mu.Unlock()
	before := countOf(f.recorder.seen(), namespaceChange("sc-watch"))
	g.Eventually(f.status("sc-watch"), pollTimeout, pollInterval).Should(
		HaveField("WatchHealthy", BeTrue()))
	g.Eventually(func() int { return countOf(f.recorder.seen(), namespaceChange("sc-watch")) },
		pollTimeout, pollInterval).Should(BeNumerically(">", before))
}

func countOf(requests []types.NamespacedName, want types.NamespacedName) int {
	n := 0
	for _, r := range requests {
		if r == want {
			n++
		}
	}
	return n
}
