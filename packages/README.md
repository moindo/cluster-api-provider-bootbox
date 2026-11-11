# Cozystack packages

Cozystack integration of the BootBox provider, laid out like cozystack's own
`packages/` tree so a Cozystack `PackageSource` can install it from this
repository:

| Path | What |
|---|---|
| `apps/baremetal-nodes` | Chart of the `BareMetalNodes` app: a worker pool on bare-metal servers. Fork of cozystack v1.7.0 `apps/kubernetes-nodes` with an additional `bootbox` substrate (the default here); the diff to upstream is the proposed `substrate: bootbox` for `KubernetesNodes`. |
| `system/baremetal-nodes-rd` | `ApplicationDefinition` of `BareMetalNodes` (dashboard category IaaS). Generated: `make -C system/baremetal-nodes-rd generate`. |
| `apps/baremetal-server` | Chart of the `BareMetalServer` app (dashboard: Administration, tenant-root only): one physical server, rendering its Tinkerbell `Hardware` (DHCP reservation) and `BootBoxHost` (BMC, labels, admitted tenants, static address) in `cozy-bootbox`. BMC credentials stay in a hand-made Secret `<name>-bmc`. |
| `system/baremetal-server-rd` | `ApplicationDefinition` of `BareMetalServer`. |
| `library/cozy-lib` | Vendored cozystack v1.7.0 library chart. |

Install (management cluster, namespace `cozy-system`); `BareMetalServer` gets a
second PackageSource/Package `moindo.baremetal-server-application` with the
components `baremetal-server` and `baremetal-server-rd`:

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata: {name: cluster-api-provider-bootbox, namespace: cozy-system}
spec:
  url: https://github.com/moindo/cluster-api-provider-bootbox
  ref: {commit: <sha>}
  interval: 10m
---
apiVersion: cozystack.io/v1alpha1
kind: PackageSource
metadata: {name: moindo.baremetal-nodes-application}
spec:
  sourceRef: {kind: GitRepository, name: cluster-api-provider-bootbox, namespace: cozy-system, path: packages}
  variants:
    - name: default
      libraries: [{name: cozy-lib, path: library/cozy-lib}]
      components:
        - {name: baremetal-nodes, path: apps/baremetal-nodes, libraries: [cozy-lib]}
        - {name: baremetal-nodes-rd, path: system/baremetal-nodes-rd, install: {namespace: cozy-system, releaseName: baremetal-nodes-rd}}
---
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata: {name: moindo.baremetal-nodes-application}
spec: {variant: default}
```

A pool: `BareMetalNodes` named `<cluster>-<pool>` in the tenant namespace,
with `bootbox.hostSelector` matching `BootBoxHost` labels; the hosts must
list the namespace in `spec.consumerNamespaces`. Example values:
`apps/baremetal-nodes/tests/values-bootbox.yaml`.
