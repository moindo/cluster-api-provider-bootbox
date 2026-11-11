package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/moindo/cluster-api-provider-bootbox/api/v1alpha1"
	"github.com/moindo/cluster-api-provider-bootbox/internal/bootserver"
	"github.com/moindo/cluster-api-provider-bootbox/internal/hardware"
	"github.com/moindo/cluster-api-provider-bootbox/internal/provision"
	"github.com/moindo/cluster-api-provider-bootbox/internal/redfish"
)

const (
	hostNS   = "cozy-bootbox"
	tenantNS = "tenant-training"
)

type fakeBMC struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeBMC) record(c string) { f.mu.Lock(); defer f.mu.Unlock(); f.calls = append(f.calls, c) }
func (f *fakeBMC) Inspect(context.Context) (redfish.Info, error) {
	f.record("inspect")
	return redfish.Info{Model: "PowerEdge R750xa", PowerState: "On"}, nil
}
func (f *fakeBMC) PXEBootOnce(context.Context) error { f.record("pxe"); return nil }
func (f *fakeBMC) PowerCycle(context.Context) error  { f.record("cycle"); return nil }
func (f *fakeBMC) PowerOff(context.Context) error    { f.record("off"); return nil }

const bootstrapConfig = `version: v1alpha1
machine:
  type: worker
  token: secret-token
cluster:
  controlPlane:
    endpoint: https://cp:6443
---
apiVersion: v1alpha1
kind: BondConfig
name: bond1
links: [ens3f0np0, ens3f1np1]
`

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, clusterv1.AddToScheme, infrav1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func fixtures() []client.Object {
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: tenantNS, Name: "kubernetes-training"}}
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: tenantNS, Name: "bm-xa-0", UID: "machine-uid",
			Labels: map[string]string{clusterv1.ClusterNameLabel: "kubernetes-training"},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: "kubernetes-training",
			Bootstrap:   clusterv1.Bootstrap{DataSecretName: ptr.To("bm-xa-0-bootstrap")},
		},
	}
	bbm := &infrav1.BootBoxMachine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: tenantNS, Name: "bm-xa-0", UID: "bbm-uid",
			Labels: map[string]string{clusterv1.ClusterNameLabel: "kubernetes-training"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(), Kind: "Machine", Name: "bm-xa-0", UID: "machine-uid",
			}},
		},
		Spec: infrav1.BootBoxMachineSpec{HostSelector: metav1.LabelSelector{MatchLabels: map[string]string{"chassis": "xa"}}},
	}
	host := &infrav1.BootBoxHost{
		ObjectMeta: metav1.ObjectMeta{Namespace: hostNS, Name: "xa06", UID: "host-uid", Labels: map[string]string{"chassis": "xa"}},
		Spec: infrav1.BootBoxHostSpec{
			HardwareName:       "xa06",
			BMC:                infrav1.BMC{Address: "https://10.99.121.22", CredentialsName: "xa06-bmc"},
			ConsumerNamespaces: []string{tenantNS},
		},
		Status: infrav1.BootBoxHostStatus{
			State: infrav1.HostStateAvailable,
			Conditions: []metav1.Condition{{
				Type: infrav1.ConditionBMCReachable, Status: metav1.ConditionTrue, Reason: "Reachable",
				LastTransitionTime: metav1.Now(),
			}},
		},
	}
	bmcSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: hostNS, Name: "xa06-bmc"},
		Data: map[string][]byte{"username": []byte("op"), "password": []byte("pw")}}
	bootstrap := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: tenantNS, Name: "bm-xa-0-bootstrap"},
		Data: map[string][]byte{"value": []byte(bootstrapConfig)}}
	hw := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": hostNS, "name": "xa06"},
		"spec": map[string]any{"interfaces": []any{map[string]any{
			"dhcp": map[string]any{"hostname": "xa06", "mac": "40:a6:b7:96:ac:98",
				"ip": map[string]any{"address": "172.19.24.36"}},
			"netboot": map[string]any{"allowPXE": true, "ipxe": map[string]any{"url": "http://pxe.example/boot.ipxe"}},
		}}},
	}}
	hw.SetGroupVersionKind(hardware.GVK)
	return []client.Object{cluster, machine, bbm, host, bmcSecret, bootstrap, hw}
}

type env struct {
	t      *testing.T
	c      client.Client
	bmc    *fakeBMC
	r      *MachineReconciler
	server *httptest.Server
	now    time.Time
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&infrav1.BootBoxHost{}, &infrav1.BootBoxMachine{}, &clusterv1.Machine{}).Build()
	e := &env{t: t, c: c, bmc: &fakeBMC{}, now: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return e.now }
	factory := func(redfish.Config) (redfish.BMC, error) { return e.bmc, nil }

	mux := http.NewServeMux()
	e.server = httptest.NewServer(mux)
	t.Cleanup(e.server.Close)
	base, _ := url.Parse(e.server.URL + "/capi")
	urls := provision.BootURLs{Base: base}
	bs := &bootserver.Server{Client: c, URLs: urls, HostNamespace: hostNS, KernelURL: "http://pxe/assets/vmlinuz", InitrdURL: "http://pxe/assets/initramfs.xz",
		KernelArgs: []string{"talos.platform=metal"}, WipeArgs: []string{"talos.experimental.wipe=system"}, Now: clock}
	mux.Handle("/", bs.Handler())

	e.r = &MachineReconciler{Client: c, BMC: factory, URLs: urls, HostNamespace: hostNS,
		IdleIPXEURL: "http://pxe.example/boot.ipxe", BootTimeout: 15 * time.Minute, MaxBootAttempts: 2, Now: clock}
	return e
}

func (e *env) reconcile() {
	e.t.Helper()
	if _, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: tenantNS, Name: "bm-xa-0"}}); err != nil {
		e.t.Fatalf("reconcile: %v", err)
	}
}

func (e *env) host() *infrav1.BootBoxHost {
	e.t.Helper()
	h := &infrav1.BootBoxHost{}
	if err := e.c.Get(context.Background(), client.ObjectKey{Namespace: hostNS, Name: "xa06"}, h); err != nil {
		e.t.Fatal(err)
	}
	return h
}

func (e *env) bbm() *infrav1.BootBoxMachine {
	e.t.Helper()
	m := &infrav1.BootBoxMachine{}
	if err := e.c.Get(context.Background(), client.ObjectKey{Namespace: tenantNS, Name: "bm-xa-0"}, m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) reservation() hardware.Reservation {
	e.t.Helper()
	hw, err := hardware.Get(context.Background(), e.c, hostNS, "xa06")
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := hardware.ReservationOf(hw)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) get(u string) (int, string) {
	e.t.Helper()
	resp, err := http.Get(u) //nolint:gosec,noctx // test server
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestLifecycle(t *testing.T) {
	e := newEnv(t, fixtures()...)

	e.reconcile() // finalizer
	e.reconcile() // claim + boot

	h := e.host()
	if h.Status.State != infrav1.HostStateProvisioning || h.Status.ConsumerRef == nil || h.Status.ConsumerRef.UID != "bbm-uid" {
		t.Fatalf("host not claimed: %+v", h.Status)
	}
	if h.Status.Provisioning.BootAttempts != 1 || strings.Join(e.bmc.calls, ",") != "pxe,cycle" {
		t.Fatalf("boot not requested: attempts=%d calls=%v", h.Status.Provisioning.BootAttempts, e.bmc.calls)
	}
	res := e.reservation()
	if !res.AllowPXE || !strings.HasPrefix(res.IPXEURL, e.server.URL+"/capi/i/xa06/") || len(res.IPXEURL) > 127 {
		t.Fatalf("hardware netboot = %+v", res)
	}
	if m := e.bbm(); m.Status.Ready || m.Status.HostRef == nil || m.Status.HostRef.Name != "xa06" {
		t.Fatalf("machine status = %+v", m.Status)
	}

	// Wrong token is refused.
	bad := strings.Replace(res.IPXEURL, "/xa06/", "/xa06/x", 1)
	if code, _ := e.get(bad); code != http.StatusNotFound {
		t.Fatalf("bad token: status %d", code)
	}

	// iPXE script carries the config URL with the same token.
	code, script := e.get(res.IPXEURL)
	if code != http.StatusOK || !strings.Contains(script, "talos.experimental.wipe=system") ||
		!strings.Contains(script, "talos.config="+strings.Replace(res.IPXEURL, "/i/", "/c/", 1)) {
		t.Fatalf("iPXE %d:\n%s", code, script)
	}

	// Talos reboots after the wipe; the next boot must install, not wipe again.
	if code, again := e.get(res.IPXEURL); code != http.StatusOK || strings.Contains(again, "talos.experimental.wipe") {
		t.Fatalf("second iPXE %d:\n%s", code, again)
	}

	// Config is the bootstrap data plus host settings.
	code, cfg := e.get(strings.Replace(res.IPXEURL, "/i/", "/c/", 1))
	if code != http.StatusOK || !strings.Contains(cfg, "providerID: bootbox://cozy-bootbox/xa06") ||
		!strings.Contains(cfg, "hostname: xa06") || !strings.Contains(cfg, "kind: BondConfig") {
		t.Fatalf("config %d:\n%s", code, cfg)
	}
	if e.host().Status.Provisioning.ConfigServedAt == nil {
		t.Fatal("config fetch not recorded")
	}

	e.reconcile()
	m := e.bbm()
	if !m.Status.Ready || ptr.Deref(m.Spec.ProviderID, "") != "bootbox://cozy-bootbox/xa06" {
		t.Fatalf("machine not ready: %+v %v", m.Status, m.Spec.ProviderID)
	}
	if len(m.Status.Addresses) != 2 {
		t.Fatalf("addresses = %v", m.Status.Addresses)
	}

	// CAPI found the node: provisioning completes and the window closes.
	machine := &clusterv1.Machine{}
	_ = e.c.Get(context.Background(), client.ObjectKey{Namespace: tenantNS, Name: "bm-xa-0"}, machine)
	machine.Status.NodeRef = &corev1.ObjectReference{Kind: "Node", Name: "xa06"}
	if err := e.c.Status().Update(context.Background(), machine); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	if s := e.host().Status.State; s != infrav1.HostStateProvisioned {
		t.Fatalf("host state = %s", s)
	}
	if r := e.reservation(); r.AllowPXE || r.IPXEURL != "http://pxe.example/boot.ipxe" {
		t.Fatalf("hardware not idled: %+v", r)
	}
	if code, _ := e.get(res.IPXEURL); code != http.StatusNotFound {
		t.Fatalf("token still valid after provisioning: %d", code)
	}

	// Deleting the machine powers the host off and frees it.
	if err := e.c.Delete(context.Background(), e.bbm()); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	h = e.host()
	if h.Status.State != infrav1.HostStateAvailable || h.Status.ConsumerRef != nil {
		t.Fatalf("host not released: %+v", h.Status)
	}
	if last := e.bmc.calls[len(e.bmc.calls)-1]; last != "off" {
		t.Fatalf("expected power off, calls %v", e.bmc.calls)
	}
}

func TestBootRetryAndGiveUp(t *testing.T) {
	e := newEnv(t, fixtures()...)
	e.reconcile()
	e.reconcile()
	e.now = e.now.Add(16 * time.Minute)
	e.reconcile()
	if a := e.host().Status.Provisioning.BootAttempts; a != 2 {
		t.Fatalf("attempts = %d", a)
	}
	e.now = e.now.Add(16 * time.Minute)
	e.reconcile()
	if a := e.host().Status.Provisioning.BootAttempts; a != 2 {
		t.Fatalf("attempts after max = %d", a)
	}
	c := e.bbm().Status.Conditions
	if len(c) == 0 || c[0].Reason != infrav1.BootFailedReason {
		t.Fatalf("conditions = %+v", c)
	}
}

func TestNoClaimOutsideConsumerNamespaces(t *testing.T) {
	objs := fixtures()
	for _, o := range objs {
		if h, ok := o.(*infrav1.BootBoxHost); ok {
			h.Spec.ConsumerNamespaces = []string{"tenant-other"}
		}
	}
	e := newEnv(t, objs...)
	e.reconcile()
	e.reconcile()
	if h := e.host(); h.Status.ConsumerRef != nil {
		t.Fatal("host claimed from a namespace it does not admit")
	}
	if len(e.bmc.calls) != 0 {
		t.Fatalf("BMC touched: %v", e.bmc.calls)
	}
}

func TestHostInspection(t *testing.T) {
	objs := fixtures()
	for _, o := range objs {
		if h, ok := o.(*infrav1.BootBoxHost); ok {
			h.Status = infrav1.BootBoxHostStatus{}
		}
	}
	e := newEnv(t, objs...)
	r := &HostReconciler{Client: e.c, BMC: func(redfish.Config) (redfish.BMC, error) { return e.bmc, nil }, InspectInterval: 10 * time.Minute}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: hostNS, Name: "xa06"}}); err != nil {
			t.Fatal(err)
		}
	}
	h := e.host()
	if h.Status.State != infrav1.HostStateAvailable || h.Status.Hardware.Model != "PowerEdge R750xa" ||
		!meta.IsStatusConditionTrue(h.Status.Conditions, infrav1.ConditionBMCReachable) {
		t.Fatalf("status = %+v", h.Status)
	}
	if len(h.Finalizers) != 1 {
		t.Fatalf("finalizers = %v", h.Finalizers)
	}
}

func TestIPXEURLFitsDHCPBootFile(t *testing.T) {
	base, _ := url.Parse("http://pxe.infra.kite.ume.de/capi")
	h := &infrav1.BootBoxHost{ObjectMeta: metav1.ObjectMeta{Namespace: hostNS, Name: "xa06"}}
	u, err := provision.BootURLs{Base: base}.IPXE(h, strings.Repeat("a", 32))
	if err != nil || len(u) > provision.MaxIPXEURLLength {
		t.Fatalf("url %q (%d bytes): %v", u, len(u), err)
	}
	h.Name = strings.Repeat("x", 63)
	long, _ := url.Parse("http://pxe.infra.kite.ume.de/some/very/long/prefix/for/the/capi/boot/server")
	if _, err := (provision.BootURLs{Base: long}).IPXE(h, strings.Repeat("a", 32)); err == nil {
		t.Fatal("oversized URL accepted")
	}
}

func TestOldTokenFormatIsReplaced(t *testing.T) {
	objs := fixtures()
	objs = append(objs, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: hostNS, Name: "xa06-boot-token"},
		Data:       map[string][]byte{"token": []byte(strings.Repeat("b", 64))},
	})
	e := newEnv(t, objs...)
	e.reconcile()
	e.reconcile()
	if r := e.reservation(); len(r.IPXEURL) > provision.MaxIPXEURLLength || strings.Contains(r.IPXEURL, strings.Repeat("b", 64)) {
		t.Fatalf("old token still used: %s", r.IPXEURL)
	}
}
