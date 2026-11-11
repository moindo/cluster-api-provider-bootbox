package redfish

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeRedfish serves a minimal Redfish tree with one system.
type fakeRedfish struct {
	mu      sync.Mutex
	power   string
	patches []string
	resets  []string
}

func (f *fakeRedfish) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	root := r.URL.Path == "/redfish/v1/" || r.URL.Path == "/redfish/v1"
	if u, p, ok := r.BasicAuth(); !root && (!ok || u != "op" || p != "pw") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.URL.Path == "/redfish/v1/" || r.URL.Path == "/redfish/v1":
		write(map[string]any{"@odata.id": "/redfish/v1/", "Id": "RootService",
			"Systems": map[string]any{"@odata.id": "/redfish/v1/Systems"}})
	case r.URL.Path == "/redfish/v1/Systems":
		write(map[string]any{"@odata.id": "/redfish/v1/Systems", "Members": []any{
			map[string]any{"@odata.id": "/redfish/v1/Systems/System.Embedded.1"}}})
	case r.URL.Path == "/redfish/v1/Systems/System.Embedded.1" && r.Method == http.MethodGet:
		write(map[string]any{
			"@odata.id": "/redfish/v1/Systems/System.Embedded.1", "Id": "System.Embedded.1",
			"Manufacturer": "Dell Inc.", "Model": "PowerEdge R750xa", "SerialNumber": "G2B32S3",
			"BiosVersion": "1.23.1", "PowerState": f.power,
			"Boot": map[string]any{"BootSourceOverrideTarget": "None", "BootSourceOverrideEnabled": "Disabled"},
			"Actions": map[string]any{"#ComputerSystem.Reset": map[string]any{
				"target": "/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset"}},
		})
	case r.URL.Path == "/redfish/v1/Systems/System.Embedded.1" && r.Method == http.MethodPatch:
		b, _ := io.ReadAll(r.Body)
		f.patches = append(f.patches, string(b))
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/Actions/ComputerSystem.Reset"):
		var body struct{ ResetType string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.resets = append(f.resets, body.ResetType)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func fingerprintOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	sum := sha256.Sum256(srv.Certificate().Raw)
	return FormatFingerprint(sum[:])
}

func TestPinnedInspectAndBoot(t *testing.T) {
	f := &fakeRedfish{power: "Off"}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()

	bmc, err := New(Config{Address: srv.URL, Username: "op", Password: "pw", Fingerprint: fingerprintOf(t, srv)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	info, err := bmc.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "PowerEdge R750xa" || info.PowerState != "Off" || info.SerialNumber != "G2B32S3" {
		t.Fatalf("info = %+v", info)
	}
	if err := bmc.PXEBootOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bmc.PowerCycle(ctx); err != nil {
		t.Fatal(err)
	}
	f.power = "On"
	if err := bmc.PowerCycle(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bmc.PowerOff(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) == 0 || !strings.Contains(f.patches[0], `"BootSourceOverrideTarget":"Pxe"`) ||
		!strings.Contains(f.patches[0], `"BootSourceOverrideEnabled":"Once"`) {
		t.Fatalf("boot patch = %v", f.patches)
	}
	if strings.Join(f.resets, ",") != "On,ForceRestart,ForceOff" {
		t.Fatalf("resets = %v", f.resets)
	}
}

func TestFingerprintMismatch(t *testing.T) {
	srv := httptest.NewTLSServer(&fakeRedfish{power: "On"})
	defer srv.Close()
	wrong := strings.Repeat("AB:", 31) + "AB"
	bmc, err := New(Config{Address: srv.URL, Username: "op", Password: "pw", Fingerprint: wrong})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bmc.Inspect(context.Background()); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("expected fingerprint error, got %v", err)
	}
}

func TestUnpinnedSelfSignedFails(t *testing.T) {
	srv := httptest.NewTLSServer(&fakeRedfish{power: "On"})
	defer srv.Close()
	bmc, _ := New(Config{Address: srv.URL, Username: "op", Password: "pw"})
	if _, err := bmc.Inspect(context.Background()); err == nil {
		t.Fatal("self-signed certificate accepted without pin")
	}
}

func TestParseFingerprint(t *testing.T) {
	in := "E9:82:F9:2A:D5:08:5A:3A:84:83:F9:DF:BA:96:DF:97:FB:22:5A:E5:E5:45:C5:31:48:1B:48:91:40:08:76:10"
	got, err := ParseFingerprint("sha256:" + in)
	if err != nil || got != strings.ToLower(strings.ReplaceAll(in, ":", "")) {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ParseFingerprint("abcd"); err == nil {
		t.Fatal("short fingerprint accepted")
	}
}
