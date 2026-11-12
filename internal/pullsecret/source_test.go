// SPDX-License-Identifier: Apache-2.0

package pullsecret

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
)

func TestSourceReaderRead(t *testing.T) {
	const (
		current = `{"auths":{"quay.io":{"auth":"Zm9vOmJhcg=="}}}`
		legacy  = `{"quay.io":{"auth":"Zm9vOmJhcg==","identitytoken":"tok"}}`
	)
	secret := func(name string, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: name},
			Data:       data,
		}
	}
	reader := func(namespaces []string, objs ...client.Object) *SourceReader {
		return &SourceReader{
			Reader: fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithObjects(objs...).
				Build(),
			Namespaces: namespaces,
		}
	}
	allowed := []string{"bootc-operator", "team-a"}

	t.Run("dockerconfigjson", func(t *testing.T) {
		g := NewWithT(t)
		r := reader(allowed, secret("ps", map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(current),
		}))
		key, data, err := r.Read(context.Background(),
			&bootcv1alpha1.PullSecretRef{Namespace: "team-a", Name: "ps"})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(key).To(Equal("team-a_ps"))
		g.Expect(string(data)).To(Equal(current))
	})

	t.Run("legacy dockercfg is normalized", func(t *testing.T) {
		g := NewWithT(t)
		r := reader(allowed, secret("ps", map[string][]byte{
			corev1.DockerConfigKey: []byte(legacy),
		}))
		_, data, err := r.Read(context.Background(),
			&bootcv1alpha1.PullSecretRef{Namespace: "team-a", Name: "ps"})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(string(data)).To(Equal(`{"auths":` + legacy + `}`))
	})

	t.Run("namespace not allowed", func(t *testing.T) {
		g := NewWithT(t)
		// The Secret exists, so only the allowlist can refuse it.
		other := secret("ps", map[string][]byte{corev1.DockerConfigJsonKey: []byte(current)})
		other.Namespace = "team-b"
		r := reader(allowed, other)
		_, _, err := r.Read(context.Background(),
			&bootcv1alpha1.PullSecretRef{Namespace: "team-b", Name: "ps"})
		var notAllowed *NamespaceNotAllowedError
		g.Expect(errors.As(err, &notAllowed)).To(BeTrue())
		g.Expect(notAllowed.Namespace).To(Equal("team-b"))
		g.Expect(err).To(MatchError(
			`pull secret namespace "team-b" is not allowed (allowed: bootc-operator, team-a)`))
	})

	t.Run("empty allowlist refuses everything", func(t *testing.T) {
		g := NewWithT(t)
		r := reader(nil)
		_, _, err := r.Read(context.Background(),
			&bootcv1alpha1.PullSecretRef{Namespace: "team-a", Name: "ps"})
		var notAllowed *NamespaceNotAllowedError
		g.Expect(errors.As(err, &notAllowed)).To(BeTrue())
	})

	t.Run("invalid reference is rejected before reading", func(t *testing.T) {
		g := NewWithT(t)
		called := false
		r := &SourceReader{
			Reader: fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(
						context.Context, client.WithWatch, client.ObjectKey,
						client.Object, ...client.GetOption,
					) error {
						called = true
						return nil
					},
				}).
				Build(),
			Namespaces: allowed,
		}
		_, _, err := r.Read(context.Background(),
			&bootcv1alpha1.PullSecretRef{Namespace: "team-a", Name: "../../etc/shadow"})
		g.Expect(err).To(MatchError(ContainSubstring("invalid name")))
		g.Expect(called).To(BeFalse())
	})

	t.Run("missing secret", func(t *testing.T) {
		g := NewWithT(t)
		r := reader(allowed)
		_, _, err := r.Read(context.Background(),
			&bootcv1alpha1.PullSecretRef{Namespace: "team-a", Name: "ps"})
		var fetchErr *FetchError
		g.Expect(errors.As(err, &fetchErr)).To(BeTrue())
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
		g.Expect(err).To(MatchError(ContainSubstring("fetching pull secret team-a/ps:")))
	})

	t.Run("read failure is not NotFound", func(t *testing.T) {
		g := NewWithT(t)
		r := &SourceReader{
			Reader: fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(
						context.Context, client.WithWatch, client.ObjectKey,
						client.Object, ...client.GetOption,
					) error {
						return apierrors.NewForbidden(
							corev1.Resource("secrets"), "ps", errors.New("denied"))
					},
				}).
				Build(),
			Namespaces: allowed,
		}
		_, _, err := r.Read(context.Background(),
			&bootcv1alpha1.PullSecretRef{Namespace: "team-a", Name: "ps"})
		var fetchErr *FetchError
		g.Expect(errors.As(err, &fetchErr)).To(BeTrue())
		g.Expect(apierrors.IsNotFound(err)).To(BeFalse())
		g.Expect(apierrors.IsForbidden(err)).To(BeTrue())
	})

	t.Run("secret without credentials", func(t *testing.T) {
		g := NewWithT(t)
		r := reader(allowed, secret("ps", map[string][]byte{"token": []byte("hunter2")}))
		_, _, err := r.Read(context.Background(),
			&bootcv1alpha1.PullSecretRef{Namespace: "team-a", Name: "ps"})
		var fetchErr *FetchError
		g.Expect(errors.As(err, &fetchErr)).To(BeFalse())
		g.Expect(err).To(MatchError(
			"pull secret team-a/ps: missing .dockerconfigjson or .dockercfg key"))
		g.Expect(err.Error()).NotTo(ContainSubstring("hunter2"))
	})
}

func TestUnreadable(t *testing.T) {
	notFound := apierrors.NewNotFound(corev1.Resource("secrets"), "ps")
	forbidden := apierrors.NewForbidden(corev1.Resource("secrets"), "ps", errors.New("denied"))
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"missing", &FetchError{Err: notFound}, true},
		{"forbidden", &FetchError{Err: forbidden}, true},
		{"namespace not allowed", &NamespaceNotAllowedError{Namespace: "ns"}, true},
		{"namespace unavailable", &FetchError{Err: &NamespaceUnavailableError{Namespace: "ns"}}, true},
		{"server error", &FetchError{Err: apierrors.NewInternalError(errors.New("boom"))}, false},
		{"timeout", &FetchError{Err: context.DeadlineExceeded}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			NewWithT(t).Expect(Unreadable(tt.err)).To(Equal(tt.want))
		})
	}
}
