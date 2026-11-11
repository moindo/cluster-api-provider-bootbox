# Cozystack packages

Cozystack integration of the BootBox provider, laid out like cozystack's own
`packages/` tree so `PackageSource`s install it straight from this repository.

| Path | What |
|---|---|
| `apps/kubernetes-nodes` | Cozystack v1.7.0 `apps/kubernetes-nodes` plus the **`bootbox` substrate**: bare-metal pools are ordinary `KubernetesNodes` with `substrate: bootbox` and `bootbox.*` (host selector, resolvers, apiserver address, node subnets, install disk, sysctls, extra Talos documents). KubeVirt and Proxmox pools render byte-identical to upstream. Proposed upstream; this copy replaces cozystack's package until then. |
| `system/kubernetes-nodes-rd` | `ApplicationDefinition` `KubernetesNodes` pointing at this chart (`make -C system/kubernetes-nodes-rd generate`). |
| `apps/baremetal-server` | `BareMetalServer` app (dashboard: Administration, tenant-root only): one physical server, rendering its Tinkerbell `Hardware` (DHCP reservation) and `BootBoxHost` (BMC, labels, admitted tenants, static address) in `cozy-bootbox`. BMC credentials stay in a hand-made Secret `<name>-bmc`. |
| `system/baremetal-server-rd` | `ApplicationDefinition` `BareMetalServer`. |
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
   - `moindo.baremetal-server-application` (variant `default`): components
     `baremetal-server` and `baremetal-server-rd`.

Re-base `apps/kubernetes-nodes` onto cozystack's chart on every Cozystack
upgrade (`diff -r` against the release's `packages/apps/kubernetes-nodes`).
