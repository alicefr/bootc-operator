# Configuring the operator

Configure the controller and daemon with a cluster-scoped
`BootcOperatorConfig`. Its typed `node.bootc.dev/v1alpha1` API keeps
administrator settings separate from installation manifests. Choose any valid
Kubernetes resource name; use at most one configuration in the cluster.

For example:

```yaml
apiVersion: node.bootc.dev/v1alpha1
kind: BootcOperatorConfig
metadata:
  name: cluster
spec:
  controller:
    tagResolutionPeriodSeconds: 60
  daemon:
    statusPollPeriodSeconds: 60
```

Installation does not create or overwrite your configuration. Without one,
the operator uses its defaults and command-line arguments. `cluster` is an
example name; the resource has no namespace.

## Supported settings

| Field under spec | Component | Built-in default |
| --- | --- | --- |
| `controller.allowInsecureRegistry` | Controller | `false` |
| `controller.pullSecretNamespaces` | Controller | `[]` (operator namespace only) |
| `controller.tagResolutionPeriodSeconds` | Controller | `300` (5 minutes) |
| `daemon.statusPollPeriodSeconds` | Daemon | `300` (5 minutes) |

Period values are positive whole seconds. Invalid types and zero or negative
periods are rejected by the API server. Omitted fields, including an empty
`spec: {}`, use CLI values or built-in defaults. Either section may be omitted.
Use YAML booleans and integers directly, without string quotes.

`allowInsecureRegistry` lets the controller's tag resolver fall back to HTTP
for registries without TLS. The bink development workflow enables it for its
local registry. Registry credentials continue to use each pool's
`pullSecretRef`.

The daemon normally observes bootc status through filesystem notifications.
`statusPollPeriodSeconds` controls fallback polling when those notifications
are unavailable.

## Pull secret namespaces

Allow pools to use pull secrets from additional namespaces with
`controller.pullSecretNamespaces`. The operator's own namespace is always
allowed. Omit the field to use CLI settings, or set `[]` for no extras even
when CLI flags list namespaces. Use unique, valid namespace names. Add this
setting to your existing configuration:

```yaml
spec:
  controller:
    pullSecretNamespaces:
      - team-a
```

This setting does not grant permissions. Create the namespace and grant the
controller access to its Secrets before adding it. For a Helm release named
`bootc-operator` in namespace `bootc-operator`, apply this example in `team-a`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: bootc-operator-pull-secrets
  namespace: team-a
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: bootc-operator-pull-secrets
  namespace: team-a
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: bootc-operator-pull-secrets
subjects:
  - kind: ServiceAccount
    name: bootc-operator-controller
    namespace: bootc-operator
```

Adjust the ServiceAccount name and namespace for your installation;
`install.yaml` uses `bootc-operator-controller-manager` instead.
For Helm-managed permissions, see
[Helm configuration](../helm.md#pool-pull-secret-namespaces).

The controller applies a change to the list while it runs. Grant the
permissions before adding a namespace, or after: it checks again about every 30
seconds, so the namespace becomes usable within that time of the grant. Until
then pools that reference it report a `SecretError`, and the namespaces it can
list keep working. The same check notices a Role or RoleBinding that is later
removed: the namespace becomes unavailable and its pools degrade.

To remove a namespace, remove it from the CR list (or the Helm/CLI setting when
the CR field is omitted). The controller stops reading it at once, pools that
reference it report that the namespace is not allowed, and its entries leave
the aggregated pull secret. Copies already on the nodes stay until the
aggregated Secret reaches them, and the daemons then remove the credentials at
the next staging. Rotate the credentials and delete the namespace's Role and
RoleBinding if the access has to be revoked, as removing a namespace from the
list does not.

The controller drops the key of a pull secret from the aggregated Secret when
it can no longer be used: it is deleted, the controller is forbidden to read
it, or its namespace is removed or unavailable. When a read fails for another
reason, such as an API server hiccup, the key keeps the credentials it last
had, the other keys still update, and the controller retries with backoff.

## Applying configuration

To use custom settings on the first startup with Helm, follow
[Configure before first startup](../helm.md#configure-before-first-startup).

After installing or upgrading the operator, check for an existing configuration:

```shell
kubectl get bootcoperatorconfigs
```

If none exists, edit and apply the sample:

```shell
kubectl apply -f config/samples/bootc_v1alpha1_bootcoperatorconfig.yaml
kubectl get bootcoperatorconfig cluster -o yaml
```

The sample explicitly sets default values, which override matching CLI flags.
Edit it or remove fields you want to leave to CLI settings before applying it.
If a configuration already exists, edit that resource instead of creating another.

Releases also provide a separate, optional `operator-config.yaml` asset with
the same default values. On a released installation, replace `vX.Y.Z` with
the installed release tag, then download and edit the file before applying it:

```shell
curl -fL -o operator-config.yaml \
  https://github.com/bootc-dev/bootc-operator/releases/download/vX.Y.Z/operator-config.yaml
# Edit operator-config.yaml, then:
kubectl apply -f operator-config.yaml
```

The controller applies changes to `controller.allowInsecureRegistry` and
`controller.tagResolutionPeriodSeconds` while it runs, within seconds of the
edit, and resolves the tags of all pools with a tag-based image again so the
new settings take effect at once. Each daemon applies
`daemon.statusPollPeriodSeconds` the same way: if it is polling because
filesystem notifications are unavailable, the next poll is one new interval
from the change, and otherwise the new interval is kept for when polling
starts. The controller also applies `controller.pullSecretNamespaces` while it
runs, see [Pull secret namespaces](#pull-secret-namespaces). No pod restarts
for any of the settings. Daemon pods run only on nodes selected by a pool, and
new ones read the current configuration when they start.

## Precedence and startup failures

For each supported setting, the value is selected in this order:

1. A field explicitly set in `BootcOperatorConfig`.
2. An explicitly supplied command-line flag when the CR field is omitted.
3. The built-in default.

Explicit `false` and `[]` in the CR override CLI values. For example,
`pullSecretNamespaces: []` permits only the operator's namespace, even if
Helm or CLI flags list extra namespaces. Omitted duration fields retain CLI
duration syntax and precision, including fractional seconds.

More than one configuration, a missing CRD, denied API access, or a connection
failure stops startup with an error. Remove extra configurations, install the
matching CRD and permissions, or restore API connectivity before restarting.
A running process keeps its loaded settings during a later API outage.
If more than one configuration exists while the controller or a daemon runs, it keeps
the settings it had until exactly one remains.

## Reverting and upgrading

Restore a field's previous value, or remove it to use the CLI value or built-in
default. To remove all overrides from this resource:

```shell
kubectl delete bootcoperatorconfig cluster
```

Replace `cluster` with the resource's actual name if you used a different one.
The controller and daemons then go back to the CLI value or built-in default
of every setting, without a restart. Explicit command-line flags still apply.

Reapplying installation manifests during an ordinary upgrade preserves the
configuration resource. For Helm installation and removal behavior, see the
[Helm guide](../helm.md).
