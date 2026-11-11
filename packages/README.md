# Cozystack packages

Cozystack integration of the BootBox provider, laid out like cozystack's own
`packages/` tree so `PackageSource`s install it straight from this repository.

| Path | What |
|---|---|
| `apps/kubernetes-nodes` | Cozystack v1.7.0 `apps/kubernetes-nodes` plus the **`bootbox` substrate**: bare-metal pools are ordinary `KubernetesNodes` with `substrate: bootbox` and `bootbox.*` (host selector, resolvers, apiserver address, node subnets, install disk, sysctls, extra Talos documents). KubeVirt and Proxmox pools render byte-identical to upstream. Proposed upstream; this copy replaces cozystack's package until then. |
| `system/kubernetes-nodes-rd` | `ApplicationDefinition` `KubernetesNodes` pointing at this chart (`make -C system/kubernetes-nodes-rd generate`). |
| `apps/baremetal` | `BareMetal` admin module (dashboard: Admin → Modules, one instance `baremetal` in tenant-root): a list of servers, each rendering its Tinkerbell `Hardware` (DHCP reservation) and `BootBoxHost` (BMC, labels, admitted tenants, static address) in `cozy-bootbox`. BMC credentials stay in hand-made Secrets `<name>-bmc`. |
| `system/baremetal-rd` | `ApplicationDefinition` `BareMetal` (module). |
| `system/cluster-api-provider-bootbox` | The provider itself (CRDs, RBAC, controllers and boot server) as a chart, installed by the `moindo.baremetal-application` package into `cozy-bootbox`. CRDs and RBAC are generated from `config/` (`make generate`). Site values (image tag, boot URLs, BMC network attachment) go into the Package's `spec.components.cluster-api-provider-bootbox.values`. |
| `library/cozy-lib` | Vendored cozystack v1.7.0 library chart. |

## Install

1. Disable cozystack's own package in the platform values:
   `bundles.disabledPackages: [cozystack.kubernetes-nodes-application]`, then
   delete the retained `Package cozystack.kubernetes-nodes-application` (the
   platform marks it `helm.sh/resource-policy: keep`). Delete existing
   `KubernetesNodes` first or expect their HelmReleases to fail until step 2
   is in place.
2. In `cozy-system`: a Flux `GitRepository` of this repository and two
   `PackageSource`/`Package` pairs:
   - `moindo.kubernetes-nodes-application` (variant `default`): components
     `kubernetes-nodes` (`apps/kubernetes-nodes`, library `cozy-lib`) and
     `kubernetes-nodes-rd` (`system/kubernetes-nodes-rd`, installed in
     `cozy-system` as `kubernetes-nodes-rd`), same dependencies as upstream.
   - `moindo.baremetal-application` (variant `default`): components
     `cluster-api-provider-bootbox` (installed in `cozy-bootbox` as
     `capi-bootbox`, with site values), `baremetal` and `baremetal-rd`.
     Enabling this one package brings the provider, the BareMetal module and
     its definition; tenants then claim servers through `KubernetesNodes`
     with `substrate: bootbox`.

## Status in the dashboard

The console shows the ConfigMaps an application lists in its
`<release>-resourcemap` ConfigMap (data key `resources`, a YAML list of
`{apiVersion: v1, kind: ConfigMap, name}`) in a ConfigMaps tab. The provider
fills them:

- hosts with the annotation `bootbox.infrastructure.cluster.x-k8s.io/status-configmap: <ns>/<name>`
  (set by the `baremetal` module: `tenant-root/baremetal-status`): state, power,
  BMC, model, serial, BIOS, consumer, admitted tenants
- machines of a MachineDeployment: `<deployment>-bootbox` in the pool's
  namespace (listed by `kubernetes-nodes` for `substrate: bootbox`): host,
  node, address, provisioning phase

Re-base `apps/kubernetes-nodes` onto cozystack's chart on every Cozystack
upgrade (`diff -r` against the release's `packages/apps/kubernetes-nodes`).
