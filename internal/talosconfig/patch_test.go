package talosconfig

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const base = `version: v1alpha1
machine:
  type: worker
  token: abc
  network:
    nameservers: [10.0.0.1]
  kubelet:
    extraConfig:
      maxPods: 250
cluster:
  controlPlane:
    endpoint: https://cp:6443
---
apiVersion: v1alpha1
kind: BondConfig
name: bond1
links: [eth0, eth1]
---
apiVersion: v1alpha1
kind: LinkConfig
name: eth2
mtu: 1500
`

func docs(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	out, err := split(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestApplyHostnameProviderIDAndPatch(t *testing.T) {
	out, err := Apply([]byte(base), HostSettings{
		Hostname:   "xa06",
		ProviderID: "bootbox://cozy-bootbox/xa06",
		Patches: []Patch{{
			Kind: "BondConfig", Name: "bond1",
			Merge: []byte(`{"addresses":[{"address":"172.19.24.36/21"}]}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := docs(t, out)
	if len(d) != 3 {
		t.Fatalf("want 3 documents, got %d", len(d))
	}
	m := d[0]["machine"].(map[string]any)
	if got := m["network"].(map[string]any)["hostname"]; got != "xa06" {
		t.Errorf("hostname = %v", got)
	}
	if got := m["network"].(map[string]any)["nameservers"]; got == nil {
		t.Error("merge dropped nameservers")
	}
	ec := m["kubelet"].(map[string]any)["extraConfig"].(map[string]any)
	if ec["providerID"] != "bootbox://cozy-bootbox/xa06" || ec["maxPods"] == nil {
		t.Errorf("extraConfig = %v", ec)
	}
	if m["token"] != "abc" {
		t.Error("token lost")
	}
	addrs := d[1]["addresses"].([]any)
	if addrs[0].(map[string]any)["address"] != "172.19.24.36/21" {
		t.Errorf("bond addresses = %v", addrs)
	}
	if _, ok := d[2]["addresses"]; ok {
		t.Error("patch leaked into LinkConfig")
	}
}

func TestApplyHostnameConfigDocument(t *testing.T) {
	in := base + "---\napiVersion: v1alpha1\nkind: HostnameConfig\nauto: stable\n"
	out, err := Apply([]byte(in), HostSettings{Hostname: "xa06"})
	if err != nil {
		t.Fatal(err)
	}
	d := docs(t, out)
	hc := d[3]
	if hc["hostname"] != "xa06" {
		t.Errorf("HostnameConfig = %v", hc)
	}
	if _, ok := hc["auto"]; ok {
		t.Error("auto must be removed when a hostname is set")
	}
	if _, ok := d[0]["machine"].(map[string]any)["network"].(map[string]any)["hostname"]; ok {
		t.Error("hostname must not be set in v1alpha1 when HostnameConfig exists")
	}
}

func TestApplyUnmatchedPatchFails(t *testing.T) {
	_, err := Apply([]byte(base), HostSettings{Patches: []Patch{{Kind: "VLANConfig", Merge: []byte(`{}`)}}})
	if err == nil || !strings.Contains(err.Error(), "matches no document") {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyLegacyPatch(t *testing.T) {
	out, err := Apply([]byte(base), HostSettings{Patches: []Patch{{
		Kind: LegacyKind, Merge: []byte(`{"machine":{"install":{"diskSelector":{"model":"X"}}}}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := yaml.Unmarshal([]byte(strings.Split(string(out), "---")[0]), &d); err != nil {
		t.Fatal(err)
	}
	if d["machine"].(map[string]any)["install"] == nil {
		t.Error("legacy patch not applied")
	}
}

func TestApplyWithoutLegacyDocumentFails(t *testing.T) {
	if _, err := Apply([]byte("apiVersion: v1alpha1\nkind: LinkConfig\nname: eth0\n"), HostSettings{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyReservationDefaultsOnlyWhenEmpty(t *testing.T) {
	in := "version: v1alpha1\nmachine:\n  type: worker\n  network: {}\n"
	out, err := Apply([]byte(in), HostSettings{Nameservers: []string{"10.0.0.1"}, NodeSubnet: "10.0.0.0/21", ProviderID: "bootbox://ns/h"})
	if err != nil {
		t.Fatal(err)
	}
	m := docs(t, out)[0]["machine"].(map[string]any)
	if ns := m["network"].(map[string]any)["nameservers"].([]any); len(ns) != 1 || ns[0] != "10.0.0.1" {
		t.Fatalf("nameservers = %v", ns)
	}
	k := m["kubelet"].(map[string]any)
	if k["nodeIP"].(map[string]any)["validSubnets"].([]any)[0] != "10.0.0.0/21" || k["extraConfig"].(map[string]any)["providerID"] != "bootbox://ns/h" {
		t.Fatalf("kubelet = %v", k)
	}

	// base sets nameservers already (pool-level override wins)
	out, err = Apply([]byte(base), HostSettings{Nameservers: []string{"9.9.9.9"}})
	if err != nil {
		t.Fatal(err)
	}
	if ns := docs(t, out)[0]["machine"].(map[string]any)["network"].(map[string]any)["nameservers"].([]any); ns[0] != "10.0.0.1" {
		t.Fatalf("pool nameservers overwritten: %v", ns)
	}
}

func TestApplyExtraDocumentsReplaceAndAppend(t *testing.T) {
	extra := "apiVersion: v1alpha1\nkind: BondConfig\nname: bond1\nlinks: [ens3f0np0, ens3f1np1]\nmtu: 9000\n---\napiVersion: v1alpha1\nkind: VLANConfig\nname: bond1.388\nvlanID: 388\nparent: bond1\n"
	out, err := Apply([]byte(base), HostSettings{
		ExtraDocuments: []byte(extra),
		Patches:        []Patch{{Kind: "BondConfig", Name: "bond1", Merge: []byte(`{"addresses":[{"address":"10.0.0.5/21"}]}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := docs(t, out)
	if len(d) != 4 {
		t.Fatalf("want 4 documents (v1alpha1, bond, link, vlan), got %d", len(d))
	}
	bond := d[1]
	if bond["mtu"] == nil || bond["links"].([]any)[0] != "ens3f0np0" || bond["addresses"] == nil {
		t.Fatalf("bond not replaced and patched: %v", bond)
	}
	if kindOf(d[3]) != "VLANConfig" {
		t.Fatalf("vlan not appended: %v", d[3])
	}
	if _, err := Apply([]byte(base), HostSettings{ExtraDocuments: []byte("version: v1alpha1\nmachine: {}\n")}); err == nil {
		t.Fatal("v1alpha1 extra document accepted")
	}
}
