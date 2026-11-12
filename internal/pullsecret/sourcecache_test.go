// SPDX-License-Identifier: Apache-2.0

package pullsecret

import (
	"context"
	"sync"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

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

func TestSourceCacheOnlyCoversItsNamespaces(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, name := range []string{"source-cache-allowed", "source-cache-other"} {
		g.Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		})).To(Succeed())
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	g.Expect(err).NotTo(HaveOccurred())
	sourceCache, err := NewSourceCache(mgr, []string{"source-cache-allowed"})
	g.Expect(err).NotTo(HaveOccurred())

	recorder := &requestRecorder{}
	g.Expect(ctrl.NewControllerManagedBy(mgr).
		Named("sourcecache-test").
		WatchesRawSource(sourceCache.Source(handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
			}))).
		Complete(recorder)).To(Succeed())
	go func() {
		_ = mgr.Start(ctx)
	}()

	newSecret := func(namespace string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "ps"},
			Data:       map[string][]byte{"k": []byte("v1")},
		}
	}
	allowed := newSecret("source-cache-allowed")
	other := newSecret("source-cache-other")
	g.Expect(k8sClient.Create(ctx, other)).To(Succeed())
	g.Expect(k8sClient.Create(ctx, allowed)).To(Succeed())

	// A Secret in a covered namespace is readable, and its changes are
	// reported.
	key := client.ObjectKeyFromObject(allowed)
	g.Eventually(func() ([]byte, error) {
		var got corev1.Secret
		err := sourceCache.Get(ctx, key, &got)
		return got.Data["k"], err
	}, pollTimeout, pollInterval).Should(Equal([]byte("v1")))

	allowed.Data["k"] = []byte("v2")
	g.Expect(k8sClient.Update(ctx, allowed)).To(Succeed())
	g.Eventually(func() ([]byte, error) {
		var got corev1.Secret
		err := sourceCache.Get(ctx, key, &got)
		return got.Data["k"], err
	}, pollTimeout, pollInterval).Should(Equal([]byte("v2")))
	g.Eventually(recorder.seen, pollTimeout, pollInterval).Should(ContainElement(key))

	// One in another namespace is neither readable nor reported, even though
	// it exists and the API server would serve it.
	var got corev1.Secret
	g.Expect(sourceCache.Get(ctx, client.ObjectKeyFromObject(other), &got)).
		To(MatchError(ContainSubstring(`no Secret cache for namespace "source-cache-other"`)))
	g.Expect(recorder.seen()).NotTo(ContainElement(client.ObjectKeyFromObject(other)))
}
