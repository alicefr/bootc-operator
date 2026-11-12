// SPDX-License-Identifier: Apache-2.0

package pullsecret

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
)

// SourceReader reads the source secrets that pools reference. It is the
// single place that decides whether a reference may be read (allowed
// namespace, storable key) and how its credentials are extracted, so the
// pool reconciler and the PullSecretAggregator cannot disagree about it.
type SourceReader struct {
	// Reader fetches the Secrets.
	Reader SecretGetter

	// Namespaces is the allowlist of namespaces pools may reference pull
	// secrets from.
	Namespaces []string
}

// NamespaceNotAllowedError is returned by Read when the reference's
// namespace is not in the allowlist.
type NamespaceNotAllowedError struct {
	Namespace string
	Allowed   []string
}

func (e *NamespaceNotAllowedError) Error() string {
	return fmt.Sprintf(
		"pull secret namespace %q is not allowed (allowed: %s)",
		e.Namespace,
		strings.Join(e.Allowed, ", "),
	)
}

// FetchError is returned by Read when the source Secret could not be
// fetched. Err is the Reader's error, so callers can tell a missing Secret
// (apierrors.IsNotFound) from other failures.
type FetchError struct {
	Namespace string
	Name      string
	Err       error
}

func (e *FetchError) Error() string {
	return fmt.Sprintf("fetching pull secret %s/%s: %v", e.Namespace, e.Name, e.Err)
}

func (e *FetchError) Unwrap() error { return e.Err }

// Read returns the aggregated Secret key and the credentials, in
// .dockerconfigjson format (see AuthData), of the source secret ref points
// to. The error is a *NamespaceNotAllowedError, a *FetchError, or describes
// a reference or Secret that cannot be used.
func (s *SourceReader) Read(
	ctx context.Context,
	ref *bootcv1alpha1.PullSecretRef,
) (string, []byte, error) {
	if !slices.Contains(s.Namespaces, ref.Namespace) {
		return "", nil, &NamespaceNotAllowedError{
			Namespace: ref.Namespace,
			Allowed:   s.Namespaces,
		}
	}

	key, err := Key(ref.Namespace, ref.Name)
	if err != nil {
		return "", nil, err
	}

	var secret corev1.Secret
	name := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
	if err := s.Reader.Get(ctx, name, &secret); err != nil {
		return "", nil, &FetchError{Namespace: ref.Namespace, Name: ref.Name, Err: err}
	}

	data, err := AuthData(&secret)
	if err != nil {
		return "", nil, fmt.Errorf("pull secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	return key, data, nil
}
