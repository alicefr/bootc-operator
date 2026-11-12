// SPDX-License-Identifier: Apache-2.0

package pullsecret

import (
	"context"
	"errors"
	"sync"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	testutil "github.com/bootc-dev/bootc-operator/test/util"
)

// scriptedSecrets serves Secrets, or the error set for them, in the allowed
// namespaces.
type scriptedSecrets struct {
	mu         sync.Mutex
	namespaces []string
	secrets    map[client.ObjectKey]string
	errs       map[client.ObjectKey]error
}

func (s *scriptedSecrets) Get(
	_ context.Context,
	key client.ObjectKey,
	obj client.Object,
	_ ...client.GetOption,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.errs[key]; err != nil {
		return err
	}
	auths, ok := s.secrets[key]
	if !ok {
		return apierrors.NewNotFound(corev1.Resource("secrets"), key.Name)
	}
	obj.(*corev1.Secret).Data = map[string][]byte{corev1.DockerConfigJsonKey: []byte(auths)}
	return nil
}

func (s *scriptedSecrets) Namespaces() []string { return s.namespaces }

func (*scriptedSecrets) Source(
	handler.TypedEventHandler[client.Object, reconcile.Request],
) source.SyncingSource {
	return nil
}

func (*scriptedSecrets) NamespaceSource(
	handler.TypedEventHandler[client.Object, reconcile.Request],
) source.SyncingSource {
	return nil
}

func (s *scriptedSecrets) set(ns, name, auths string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := client.ObjectKey{Namespace: ns, Name: name}
	s.secrets[key] = auths
	delete(s.errs, key)
	if err != nil {
		s.errs[key] = err
	}
}

func authsFor(registry string) string {
	return `{"auths":{"` + registry + `":{}}}`
}

func TestPullSecretAggregatorFailureHandling(t *testing.T) {
	ctx := context.Background()
	transient := apierrors.NewServiceUnavailable("etcd is busy")
	forbidden := apierrors.NewForbidden(corev1.Resource("secrets"), "ps", errors.New("denied"))

	setup := func(g Gomega) (*PullSecretAggregator, *scriptedSecrets) {
		secrets := &scriptedSecrets{
			namespaces: []string{testutil.OperatorNamespaceName, "team-a", "team-b"},
			secrets:    map[client.ObjectKey]string{},
			errs:       map[client.ObjectKey]error{},
		}
		secrets.set("team-a", "ps", authsFor("a.example.com"), nil)
		secrets.set("team-b", "ps", authsFor("b.example.com"), nil)
		c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(
			newAggregatorPool("pool-a", "ps", "team-a"),
			newAggregatorPool("pool-b", "ps", "team-b"),
		).Build()
		r := &PullSecretAggregator{
			Client:    c,
			Secrets:   secrets,
			Namespace: testutil.OperatorNamespaceName,
		}
		_, err := r.Reconcile(ctx, ctrl.Request{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(fakeAggregated(ctx, c)).To(And(
			HaveKeyWithValue("team-a_ps", []byte(authsFor("a.example.com"))),
			HaveKeyWithValue("team-b_ps", []byte(authsFor("b.example.com"))),
		))
		return r, secrets
	}

	t.Run("transient failure keeps the key and updates the others", func(t *testing.T) {
		g := NewWithT(t)
		r, secrets := setup(g)
		secrets.set("team-a", "ps", authsFor("a2.example.com"), nil)
		secrets.set("team-b", "ps", authsFor("b2.example.com"), transient)

		_, err := r.Reconcile(ctx, ctrl.Request{})

		g.Expect(err).To(MatchError(ContainSubstring("fetching pull secret team-b/ps")))
		g.Expect(fakeAggregated(ctx, r.Client)).To(And(
			HaveKeyWithValue("team-a_ps", []byte(authsFor("a2.example.com"))),
			HaveKeyWithValue("team-b_ps", []byte(authsFor("b.example.com"))),
		), "team-b keeps its last credentials while team-a rotates")

		// The next reconcile, once the read works again, catches up.
		secrets.set("team-b", "ps", authsFor("b2.example.com"), nil)
		_, err = r.Reconcile(ctx, ctrl.Request{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(fakeAggregated(ctx, r.Client)).To(
			HaveKeyWithValue("team-b_ps", []byte(authsFor("b2.example.com"))))
	})

	t.Run("transient failure without earlier credentials omits only that key", func(t *testing.T) {
		g := NewWithT(t)
		r, secrets := setup(g)
		g.Expect(r.Create(ctx, newAggregatorPool("pool-c", "ps", "team-c"))).To(Succeed())
		secrets.namespaces = append(secrets.namespaces, "team-c")
		secrets.set("team-c", "ps", authsFor("c.example.com"), transient)

		_, err := r.Reconcile(ctx, ctrl.Request{})

		g.Expect(err).To(HaveOccurred())
		data := fakeAggregated(ctx, r.Client)
		g.Expect(data).
			To(And(HaveKey("team-a_ps"), HaveKey("team-b_ps"), Not(HaveKey("team-c_ps"))))
	})

	t.Run("forbidden drops the key without error", func(t *testing.T) {
		g := NewWithT(t)
		r, secrets := setup(g)
		secrets.set("team-b", "ps", authsFor("b.example.com"), forbidden)

		_, err := r.Reconcile(ctx, ctrl.Request{})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(fakeAggregated(ctx, r.Client)).To(
			And(HaveKey("team-a_ps"), Not(HaveKey("team-b_ps"))))
	})

	t.Run("deleted secret drops the key without error", func(t *testing.T) {
		g := NewWithT(t)
		r, secrets := setup(g)
		secrets.mu.Lock()
		delete(secrets.secrets, client.ObjectKey{Namespace: "team-b", Name: "ps"})
		secrets.mu.Unlock()

		_, err := r.Reconcile(ctx, ctrl.Request{})

		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(fakeAggregated(ctx, r.Client)).To(
			And(HaveKey("team-a_ps"), Not(HaveKey("team-b_ps"))))
	})

	t.Run("removed or unavailable namespace drops its keys without error", func(t *testing.T) {
		g := NewWithT(t)
		r, secrets := setup(g)
		secrets.namespaces = []string{testutil.OperatorNamespaceName, "team-a"}

		_, err := r.Reconcile(ctx, ctrl.Request{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(fakeAggregated(ctx, r.Client)).To(
			And(HaveKey("team-a_ps"), Not(HaveKey("team-b_ps"))))

		// Allowed again but unavailable: still no key.
		secrets.namespaces = append(secrets.namespaces, "team-b")
		secrets.set("team-b", "ps", authsFor("b.example.com"),
			&NamespaceUnavailableError{Namespace: "team-b", Reason: "denied"})
		_, err = r.Reconcile(ctx, ctrl.Request{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(fakeAggregated(ctx, r.Client)).To(Not(HaveKey("team-b_ps")))
	})

	t.Run("a failed write after a transient read is reported", func(t *testing.T) {
		g := NewWithT(t)
		r, secrets := setup(g)
		secrets.set("team-b", "ps", authsFor("b2.example.com"), transient)
		secrets.set("team-a", "ps", authsFor("a2.example.com"), nil)
		r.Client = failingUpdates{Client: r.Client, err: errors.New("write failed")}

		_, err := r.Reconcile(ctx, ctrl.Request{})

		g.Expect(err).To(MatchError(ContainSubstring("write failed")))
	})
}

// failingUpdates fails every Secret update.
type failingUpdates struct {
	client.Client
	err error
}

func (f failingUpdates) Update(context.Context, client.Object, ...client.UpdateOption) error {
	return f.err
}

func fakeAggregated(ctx context.Context, c client.Client) map[string][]byte {
	var secret corev1.Secret
	err := c.Get(ctx, client.ObjectKey{
		Namespace: testutil.OperatorNamespaceName,
		Name:      SecretName,
	}, &secret)
	if err != nil {
		return nil
	}
	return secret.Data
}
