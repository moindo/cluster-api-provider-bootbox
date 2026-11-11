 # cluster-api-provider-bootbox

A [Cluster API](https://cluster-api.sigs.k8s.io/) infrastructure provider that
turns bare-metal servers into Talos worker nodes. Servers are PXE-booted
through [Cozystack BootBox](https://cozystack.io/) (Tinkerbell
[smee](https://github.com/tinkerbell/smee) + matchbox assets) and driven over
[Redfish](https://www.dmtf.org/standards/redfish). It pairs with the
[Talos bootstrap provider (CABPT)](https://github.com/siderolabs/cluster-api-bootstrap-provider-talos)
and any control-plane provider, for example
[Kamaji](https://github.com/clastix/cluster-api-control-plane-provider-kamaji).

Status: **alpha**. The API is `infrastructure.cluster.x-k8s.io/v1alpha1` and
follows the Cluster API v1beta1 contract.

## How it works

```
BootBoxHost (inventory, cozy-bootbox)        BootBoxMachine (per Machine, tenant ns)
  hardwareName ─► Tinkerbell Hardware          hostSelector ─► claims an Available host
  bmc ─► Redfish (pinned TLS)
```

1. A `MachineDeployment` creates a `Machine`, a CABPT `TalosConfig` and a
   `BootBoxMachine`.
2. Once the bootstrap data exists, the provider claims an `Available`
   `BootBoxHost` whose labels match `hostSelector` and whose
   `consumerNamespaces` admit the machine's namespace.
3. It creates a random per-host token, points the host's Tinkerbell
   `Hardware` iPXE URL at `<boot-base-url>/i/<host>/<token>`
   and sets `allowPXE: true`.
4. Over Redfish it requests a one-time UEFI PXE boot and power-cycles the
   server.
5. smee answers DHCP and chains iPXE to the provider's boot server. The
   script boots the Talos kernel with `talos.experimental.wipe=system` (a
   reused disk never boots its previous `STATE`) and
   `talos.config=<boot-base-url>/c/<host>/<token>`.
6. Talos fetches its config: the CABPT bootstrap data plus the host's
   hostname, `machine.kubelet.extraConfig.providerID =
   bootbox://<ns>/<host>` and the host's `configPatches` (JSON merge patches
   per document, for example a static bond address). The machine becomes
   `Ready` and Cluster API matches the Node by provider ID.
7. When the Machine has a `nodeRef`, the token is deleted, `allowPXE` is
   reset and the host is `Provisioned`.
8. Deleting the machine powers the host off over Redfish and returns it to
   `Available`.

If the host does not fetch its config within `--boot-timeout`, the boot is
requested again, up to `--max-boot-attempts` times.

## Resources

| Kind | Scope | Purpose |
|---|---|---|
| `BootBoxHost` (`bbh`) | host namespace | One server: Hardware reference, BMC endpoint and credentials, admitted namespaces, per-host config patches. The host controller inspects the BMC (model, serial, power state). |
| `BootBoxMachine` (`bbm`) | cluster namespace | Infrastructure machine. |
| `BootBoxMachineTemplate` (`bbmt`) | cluster namespace | Template for MachineDeployments/MachineSets. |

Samples: [`config/samples/`](config/samples/).

## Install

```bash
kubectl apply -k config/default   # generic, namespace capi-bootbox-system
```

Run it in the namespace of the Tinkerbell `Hardware` objects (or set
`--host-namespace`) and set the site flags in an overlay:

| Flag | Meaning |
|---|---|
| `--boot-base-url` | URL of the boot server as reachable by booting hosts (expose the `boot` Service, e.g. under a path of the PXE ingress). Keep it short: the iPXE URL travels in the 128-byte DHCP boot file field (the provider refuses longer URLs). |
| `--kernel-url`, `--initrd-url` | Talos PXE assets, e.g. matchbox `/assets/vmlinuz` and `/assets/initramfs.xz`. |
| `--kernel-args` | Comma-separated kernel arguments (default: matchbox defaults plus the wipe). |
| `--idle-ipxe-url` | iPXE URL restored on the Hardware after provisioning. |
| `--boot-timeout`, `--max-boot-attempts`, `--inspect-interval` | Timing. |

The manager must reach the BMCs. A common setup gives only this pod a leg in
the out-of-band network, e.g. a Multus macvlan attachment; with a static
address keep `replicas: 1` (the default).

BMC accounts need no more than the Redfish *Operator* role. Pin each BMC
certificate with `spec.bmc.certificateFingerprint` (SHA-256 of the leaf
certificate):

```bash
openssl s_client -connect <bmc>:443 </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256
```

## Security notes

- The machine config contains cluster secrets. It is served over plain HTTP
  (Talos fetches `talos.config` before it has any trust anchor) and only
  while the host provisions, behind a 128-bit per-host token. Keep the boot
  network restricted to the servers.
- The manager reads bootstrap Secrets in the cluster namespaces (`get`
  only; Secrets are not cached).
- A host is only claimed from namespaces listed in `consumerNamespaces`.

## Development

```bash
make generate   # deepcopy, CRDs, RBAC (controller-gen)
make test       # go vet + unit tests (fake client, fake Redfish over TLS)
make docker-build IMG=...
```

## Roadmap

- Talos `reset` over the API on deprovisioning instead of power-off + wipe on reuse
- Inventory discovery from Redfish (MACs, disks) to create Hardware objects
- Optional HTTPS for the config endpoint
- Events and metrics per host

## License

Apache-2.0
