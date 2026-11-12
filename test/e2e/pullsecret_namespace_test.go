// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
	"github.com/bootc-dev/bootc-operator/internal/pullsecret"
	"github.com/bootc-dev/bootc-operator/test/e2e/e2eutil"
	testutil "github.com/bootc-dev/bootc-operator/test/util"
)

// TestPullSecretNamespaces verifies that pools can only use pull secrets
// from the namespaces the operator is configured to read from. The
// operator must already allow E2E_PULL_SECRET_NAMESPACE (make deploy-bink
// does). A pool referencing a secret in a namespace the test creates must
// be Degraded without claiming its node; switched to a secret in the
// allowed namespace, its node must update through the authenticated
// registry.
func TestPullSecretNamespaces(t *testing.T) {
	e2eutil.Providers(t, "bink", "eks")
	t.Parallel()
	g := NewWithT(t)
	g.SetDefaultEventuallyTimeout(pollTimeout)
	g.SetDefaultEventuallyPollingInterval(pollInterval)

	env := e2eutil.New(t)
	if env.RegistryUser() == "" || env.RegistryPassword() == "" {
		t.Skip("E2E_REGISTRY_USER / E2E_REGISTRY_PASSWORD not set")
	}
	allowedNS := os.Getenv("E2E_PULL_SECRET_NAMESPACE")
	if allowedNS == "" {
		t.Skip("E2E_PULL_SECRET_NAMESPACE not set")
	}

	ctx := context.Background()
	configuredNamespaces := controllerPullSecretNamespaces(t, g, ctx, env.Client)
	if !slices.Contains(configuredNamespaces, allowedNS) {
		t.Fatalf(
			"operator must allow pull secrets from %q; configured namespaces: %v",
			allowedNS,
			configuredNamespaces,
		)
	}

	deniedNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: env.TestID() + "-denied"}}
	g.Expect(env.Client.Create(ctx, deniedNS)).To(Succeed())
	t.Cleanup(func() { _ = env.Client.Delete(context.Background(), deniedNS) })

	nodeName := env.AddNode(t)

	deniedSecret := registrySecret(env, env.TestID()+"-pull-secret", deniedNS.Name)
	g.Expect(env.Client.Create(ctx, deniedSecret)).To(Succeed())
	allowedSecret := registrySecret(env, env.TestID()+"-pull-secret", allowedNS)
	g.Expect(env.Client.Create(ctx, allowedSecret)).To(Succeed())
	t.Cleanup(func() { _ = env.Client.Delete(context.Background(), allowedSecret) })

	authImageRef := env.AuthImageRef()
	pool := env.NewPool("psns", authImageRef,
		testutil.WithPullSecret(deniedSecret.Name, deniedSecret.Namespace),
	)
	g.Expect(env.Client.Create(ctx, pool)).To(Succeed())

	// Phase 1: the secret exists, but its namespace is not allowed.
	g.Eventually(func() ([]metav1.Condition, error) {
		var p bootcv1alpha1.BootcNodePool
		err := env.Client.Get(ctx, client.ObjectKeyFromObject(pool), &p)
		return p.Status.Conditions, err
	}).Should(ContainElement(And(
		HaveField("Type", bootcv1alpha1.PoolDegraded),
		HaveField("Status", metav1.ConditionTrue),
		HaveField("Reason", bootcv1alpha1.PoolSecretError),
		HaveField("Message", ContainSubstring(fmt.Sprintf("%q", deniedNS.Name))),
	)))
	g.Consistently(func() bool {
		var bn bootcv1alpha1.BootcNode
		err := env.Client.Get(ctx, client.ObjectKey{Name: nodeName}, &bn)
		return apierrors.IsNotFound(err)
	}).WithTimeout(10*time.Second).Should(BeTrue(),
		"no BootcNode should be created for a pool with a denied secret")

	t.Logf("Pool %q is Degraded for secret in namespace %q", pool.Name, deniedNS.Name)

	// Phase 2: a secret in the allowed namespace is used to pull the image.
	modified := pool.DeepCopy()
	modified.Spec.PullSecretRef = &bootcv1alpha1.PullSecretRef{
		Name: allowedSecret.Name, Namespace: allowedSecret.Namespace,
	}
	g.Expect(env.Client.Patch(ctx, modified, client.MergeFrom(pool))).To(Succeed())
	*pool = *modified

	testutil.WaitForNodeIdle(t, g, ctx, env.Client, nodeName, 8*time.Minute,
		HaveField("Booted", HaveField("Image", Equal(authImageRef))),
	)
	g.Eventually(fetchPoolStatus(ctx, env.Client, pool)).
		Should(poolAllUpdated(1, env.NodeImageUpdateDigest()))

	t.Logf("Node %q updated with a pull secret from namespace %q", nodeName, allowedNS)
}

// TestOperatorConfigPullSecretNamespaces edits the BootcOperatorConfig while
// the controller runs and checks that the pull secret namespaces follow without
// a restart: a namespace is added after its RBAC exists, and another before
// it, a rotation reaches the aggregated Secret, and removing a namespace or
// deleting the configuration takes its key out again. It changes shared
// configuration, so it must stay serial. No node is provisioned: the aggregated
// Secret and the pools' conditions show what the controller reads.
func TestOperatorConfigPullSecretNamespaces(t *testing.T) {
	e2eutil.Providers(t, "bink")
	g := NewWithT(t)
	g.SetDefaultEventuallyTimeout(pollTimeout)
	g.SetDefaultEventuallyPollingInterval(pollInterval)
	env := e2eutil.New(t)
	ctx := context.Background()
	var originalDeploy appsv1.Deployment
	g.Expect(env.Client.Get(ctx, operatorDeployKey(), &originalDeploy)).To(Succeed())
	originalConfig, err := readOperatorConfig(ctx, env.Client)
	g.Expect(err).NotTo(HaveOccurred())

	id := env.TestID()
	allowedNS, lateNS, deniedNS := id+"-allowed", id+"-late", id+"-denied"
	for _, name := range []string{allowedNS, lateNS, deniedNS} {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		g.Expect(env.Client.Create(ctx, ns)).To(Succeed())
		t.Cleanup(
			func() { g.Expect(client.IgnoreNotFound(env.Client.Delete(ctx, ns))).To(Succeed()) },
		)
		g.Expect(env.Client.Create(ctx, pullSecretIn(name, "v1"))).To(Succeed())
	}
	serviceAccount := originalDeploy.Spec.Template.Spec.ServiceAccountName
	grantSecretAccess(t, g, ctx, env.Client, allowedNS, serviceAccount, originalDeploy.Namespace)

	var originalArgs []string
	for _, cont := range originalDeploy.Spec.Template.Spec.Containers {
		if cont.Name == "manager" {
			originalArgs = cont.Args
		}
	}
	t.Cleanup(func() {
		g.Expect(restoreOperatorConfig(ctx, env.Client, originalConfig)).To(Succeed())
		setControllerArgs(t, g, ctx, env.Client, originalArgs)
		restartOperator(t)
		waitForOperatorReady(t, g, ctx, env.Client)
	})

	// Start from a known configuration, with the CLI out of the way. This is
	// the only restart: the rest is applied by the running controller.
	name := id
	if originalConfig != nil {
		name = originalConfig.Name
	}
	cfg := originalConfig.DeepCopy()
	if cfg == nil {
		cfg = &bootcv1alpha1.BootcOperatorConfig{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}
	if cfg.Spec.Controller == nil {
		cfg.Spec.Controller = &bootcv1alpha1.OperatorControllerConfig{}
	}
	cfg.Spec.Controller.PullSecretNamespaces = []string{allowedNS}
	g.Expect(restoreOperatorConfig(ctx, env.Client, cfg)).To(Succeed())
	args, _ := splitNamespaceArgs(originalArgs)
	setControllerArgs(t, g, ctx, env.Client, args)
	restartOperator(t)
	waitForOperatorReady(t, g, ctx, env.Client)
	controllerBefore := controllerPods(t, g, ctx, env.Client)

	poolFor := func(label, namespace string) *bootcv1alpha1.BootcNodePool {
		pool := env.NewPool(label, env.NodeImageDigestedPullSpec(),
			testutil.WithPullSecret("pull-secret", namespace))
		g.Expect(env.Client.Create(ctx, pool)).To(Succeed())
		t.Cleanup(func() {
			g.Expect(client.IgnoreNotFound(env.Client.Delete(ctx, pool))).To(Succeed())
			g.Eventually(func() error {
				return env.Client.Get(ctx, client.ObjectKeyFromObject(pool), pool)
			}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))
		})
		return pool
	}
	secretError := func(pool *bootcv1alpha1.BootcNodePool) func() ([]metav1.Condition, error) {
		return func() ([]metav1.Condition, error) {
			var got bootcv1alpha1.BootcNodePool
			err := env.Client.Get(ctx, client.ObjectKeyFromObject(pool), &got)
			return got.Status.Conditions, err
		}
	}
	degradedBySecret := func(namespace string) types.GomegaMatcher {
		return ContainElement(And(
			HaveField("Type", bootcv1alpha1.PoolDegraded),
			HaveField("Status", metav1.ConditionTrue),
			HaveField("Reason", bootcv1alpha1.PoolSecretError),
			HaveField("Message", ContainSubstring(namespace)),
		))
	}
	notDegradedBySecret := ContainElement(And(
		HaveField("Type", bootcv1alpha1.PoolDegraded),
		HaveField("Status", metav1.ConditionFalse),
		HaveField("Reason", bootcv1alpha1.PoolHealthy),
	))
	aggregated := func() (map[string][]byte, error) {
		var secret corev1.Secret
		err := env.Client.Get(ctx, client.ObjectKey{
			Namespace: testutil.OperatorNamespaceName,
			Name:      pullsecret.SecretName,
		}, &secret)
		return secret.Data, err
	}
	keyOf := func(namespace string) string { return namespace + "_pull-secret" }
	status := func() (bootcv1alpha1.BootcOperatorConfigStatus, error) {
		var got bootcv1alpha1.BootcOperatorConfig
		err := env.Client.Get(ctx, client.ObjectKey{Name: name}, &got)
		return got.Status, err
	}
	const slow = 2 * time.Minute

	// A namespace that is not configured is refused, whatever its Secrets.
	deniedPool := poolFor("cfg-live-denied", deniedNS)
	g.Eventually(secretError(deniedPool)).Should(degradedBySecret(deniedNS))

	// A configured one with access is read, and its rotation propagates.
	allowedPool := poolFor("cfg-live-allowed", allowedNS)
	g.Eventually(secretError(allowedPool)).Should(notDegradedBySecret)
	g.Eventually(aggregated).Should(HaveKeyWithValue(keyOf(allowedNS), authsJSON("v1")))
	g.Eventually(status).Should(And(
		HaveField("ListablePullSecretNamespaces", ContainElement(allowedNS)),
		HaveField("Conditions", ContainElement(And(
			HaveField("Type", bootcv1alpha1.ConfigControllerReady),
			HaveField("Status", metav1.ConditionTrue),
		))),
	))
	rotate(t, g, ctx, env.Client, allowedNS, "v2")
	g.Eventually(aggregated).Should(HaveKeyWithValue(keyOf(allowedNS), authsJSON("v2")))

	// Adding a namespace before its RBAC exists leaves it pending, and the rest
	// keeps working.
	setPullSecretNamespaces(t, g, ctx, env.Client, name, allowedNS, lateNS)
	latePool := poolFor("cfg-live-late", lateNS)
	g.Eventually(secretError(latePool)).Should(degradedBySecret(lateNS))
	g.Eventually(status).Should(And(
		HaveField("ListablePullSecretNamespaces", Not(ContainElement(lateNS))),
		HaveField("Conditions", ContainElement(And(
			HaveField("Type", bootcv1alpha1.ConfigControllerReady),
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Reason", bootcv1alpha1.ConfigReasonPendingNamespaces),
			HaveField("Message", ContainSubstring(lateNS)),
		))),
	))
	g.Expect(aggregated()).To(HaveKey(keyOf(allowedNS)))
	rotate(t, g, ctx, env.Client, allowedNS, "v3")
	g.Eventually(aggregated).Should(HaveKeyWithValue(keyOf(allowedNS), authsJSON("v3")))

	// Granting the access activates it without touching the configuration.
	grantSecretAccess(t, g, ctx, env.Client, lateNS, serviceAccount, originalDeploy.Namespace)
	g.Eventually(secretError(latePool)).WithTimeout(slow).Should(notDegradedBySecret)
	g.Eventually(aggregated).Should(HaveKeyWithValue(keyOf(lateNS), authsJSON("v1")))
	g.Eventually(status).Should(And(
		HaveField("ListablePullSecretNamespaces", ContainElements(allowedNS, lateNS)),
		HaveField("Conditions", ContainElement(And(
			HaveField("Type", bootcv1alpha1.ConfigControllerReady),
			HaveField("Status", metav1.ConditionTrue),
		))),
	))
	rotate(t, g, ctx, env.Client, lateNS, "v2")
	g.Eventually(aggregated).Should(HaveKeyWithValue(keyOf(lateNS), authsJSON("v2")))

	// Removing a namespace drops it, and only it.
	setPullSecretNamespaces(t, g, ctx, env.Client, name, allowedNS)
	g.Eventually(secretError(latePool)).Should(degradedBySecret(lateNS))
	g.Eventually(aggregated).Should(And(
		HaveKey(keyOf(allowedNS)),
		Not(HaveKey(keyOf(lateNS))),
	))
	g.Eventually(status).Should(
		HaveField("ListablePullSecretNamespaces", Not(ContainElement(lateNS))))

	// Deleting the configuration restores the CLI settings, which allow nothing
	// extra.
	g.Expect(restoreOperatorConfig(ctx, env.Client, nil)).To(Succeed())
	g.Eventually(secretError(allowedPool)).Should(degradedBySecret(allowedNS))
	g.Eventually(aggregated).ShouldNot(HaveKey(keyOf(allowedNS)))

	// None of it restarted the controller.
	g.Expect(controllerPods(t, g, ctx, env.Client)).To(Equal(controllerBefore))
}

// pullSecretIn returns a pull secret in the namespace whose credentials
// differ with version.
func pullSecretIn(namespace, version string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "pull-secret", Namespace: namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: authsJSON(version)},
	}
}

func authsJSON(version string) []byte {
	return fmt.Appendf(nil, `{"auths":{"%s.example.com":{}}}`, version)
}

// rotate changes the credentials of the namespace's pull secret.
func rotate(
	t *testing.T,
	g Gomega,
	ctx context.Context,
	c client.Client,
	namespace, version string,
) {
	t.Helper()
	var secret corev1.Secret
	g.Expect(c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "pull-secret"}, &secret)).
		To(Succeed())
	secret.Data[corev1.DockerConfigJsonKey] = authsJSON(version)
	g.Expect(c.Update(ctx, &secret)).To(Succeed())
}

// grantSecretAccess lets the controller's ServiceAccount get, list and watch
// Secrets in the namespace, as an administrator does.
func grantSecretAccess(
	t *testing.T,
	g Gomega,
	ctx context.Context,
	c client.Client,
	namespace, serviceAccount, serviceAccountNamespace string,
) {
	t.Helper()
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "pull-secrets", Namespace: namespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"secrets"},
			Verbs:     []string{"get", "list", "watch"},
		}},
	}
	g.Expect(c.Create(ctx, role)).To(Succeed())
	g.Expect(c.Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      serviceAccount,
			Namespace: serviceAccountNamespace,
		}},
	})).To(Succeed())
}

// setPullSecretNamespaces edits the configuration in place, as an
// administrator does, retrying when the controller wrote its status meanwhile.
func setPullSecretNamespaces(
	t *testing.T,
	g Gomega,
	ctx context.Context,
	c client.Client,
	name string,
	namespaces ...string,
) {
	t.Helper()
	g.Eventually(func() error {
		var cfg bootcv1alpha1.BootcOperatorConfig
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &cfg); err != nil {
			return err
		}
		if cfg.Spec.Controller == nil {
			cfg.Spec.Controller = &bootcv1alpha1.OperatorControllerConfig{}
		}
		cfg.Spec.Controller.PullSecretNamespaces = namespaces
		return c.Update(ctx, &cfg)
	}).Should(Succeed())
}

// controllerPods identifies the controller's pods and their containers' restarts,
// so a test can show that none was replaced or restarted.
func controllerPods(t *testing.T, g Gomega, ctx context.Context, c client.Client) []string {
	t.Helper()
	var d appsv1.Deployment
	g.Expect(c.Get(ctx, operatorDeployKey(), &d)).To(Succeed())
	var pods corev1.PodList
	g.Expect(c.List(ctx, &pods, client.InNamespace(d.Namespace),
		client.MatchingLabels(d.Spec.Selector.MatchLabels))).To(Succeed())
	var identities []string
	for _, pod := range pods.Items {
		restarts := int32(0)
		for _, status := range pod.Status.ContainerStatuses {
			restarts += status.RestartCount
		}
		identities = append(
			identities,
			fmt.Sprintf("%s/%s/restarts=%d", pod.Name, pod.UID, restarts),
		)
	}
	slices.Sort(identities)
	return identities
}

func setControllerArgs(
	t *testing.T,
	g Gomega,
	ctx context.Context,
	c client.Client,
	args []string,
) {
	t.Helper()
	var d appsv1.Deployment
	g.Expect(c.Get(ctx, operatorDeployKey(), &d)).To(Succeed())
	original := d.DeepCopy()
	for i := range d.Spec.Template.Spec.Containers {
		if d.Spec.Template.Spec.Containers[i].Name == "manager" {
			d.Spec.Template.Spec.Containers[i].Args = args
			g.Expect(c.Patch(ctx, &d, client.MergeFrom(original))).To(Succeed())
			return
		}
	}
	t.Fatal("manager container not found")
}

// registrySecret returns a dockerconfigjson Secret with the credentials of
// the authenticated e2e registry.
func registrySecret(env *e2eutil.Env, name, namespace string) *corev1.Secret {
	auth := base64.StdEncoding.EncodeToString(
		[]byte(env.RegistryUser() + ":" + env.RegistryPassword()),
	)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: fmt.Appendf(nil,
				`{"auths":{%q:{"auth":%q}}}`, env.RegistryHost(), auth),
		},
	}
}
