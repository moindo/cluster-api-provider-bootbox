// Package hardware reads and updates Tinkerbell Hardware objects, which hold
// the DHCP reservations smee answers from. It works on unstructured objects
// to avoid depending on the Tinkerbell module.
package hardware

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GVK of tinkerbell.org/v1alpha1 Hardware.
var GVK = schema.GroupVersionKind{Group: "tinkerbell.org", Version: "v1alpha1", Kind: "Hardware"}

// Reservation is the part of the first DHCP interface the provider uses.
type Reservation struct {
	Hostname string
	MAC      string
	IP       string
	IPXEURL  string
	AllowPXE bool
}

// Get loads a Hardware object.
func Get(ctx context.Context, c client.Reader, namespace, name string) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(GVK)
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, u); err != nil {
		return nil, err
	}
	return u, nil
}

func interfaces(u *unstructured.Unstructured) ([]any, error) {
	ifaces, found, err := unstructured.NestedSlice(u.Object, "spec", "interfaces")
	if err != nil {
		return nil, err
	}
	if !found || len(ifaces) == 0 {
		return nil, fmt.Errorf("hardware %s has no interfaces", u.GetName())
	}
	return ifaces, nil
}

// ReservationOf returns the reservation of the first interface.
func ReservationOf(u *unstructured.Unstructured) (Reservation, error) {
	ifaces, err := interfaces(u)
	if err != nil {
		return Reservation{}, err
	}
	iface, ok := ifaces[0].(map[string]any)
	if !ok {
		return Reservation{}, fmt.Errorf("hardware %s: malformed interface", u.GetName())
	}
	r := Reservation{}
	r.Hostname, _, _ = unstructured.NestedString(iface, "dhcp", "hostname")
	r.MAC, _, _ = unstructured.NestedString(iface, "dhcp", "mac")
	r.IP, _, _ = unstructured.NestedString(iface, "dhcp", "ip", "address")
	r.IPXEURL, _, _ = unstructured.NestedString(iface, "netboot", "ipxe", "url")
	r.AllowPXE, _, _ = unstructured.NestedBool(iface, "netboot", "allowPXE")
	return r, nil
}

// SetNetboot points every interface at ipxeURL and sets allowPXE. It returns
// whether the object changed; the caller persists it.
func SetNetboot(u *unstructured.Unstructured, ipxeURL string, allowPXE bool) (bool, error) {
	ifaces, err := interfaces(u)
	if err != nil {
		return false, err
	}
	changed := false
	for i := range ifaces {
		iface, ok := ifaces[i].(map[string]any)
		if !ok {
			return false, fmt.Errorf("hardware %s: malformed interface %d", u.GetName(), i)
		}
		oldURL, _, _ := unstructured.NestedString(iface, "netboot", "ipxe", "url")
		oldAllow, _, _ := unstructured.NestedBool(iface, "netboot", "allowPXE")
		if oldURL == ipxeURL && oldAllow == allowPXE {
			continue
		}
		if err := unstructured.SetNestedField(iface, ipxeURL, "netboot", "ipxe", "url"); err != nil {
			return false, err
		}
		if err := unstructured.SetNestedField(iface, allowPXE, "netboot", "allowPXE"); err != nil {
			return false, err
		}
		ifaces[i] = iface
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, unstructured.SetNestedSlice(u.Object, ifaces, "spec", "interfaces")
}
