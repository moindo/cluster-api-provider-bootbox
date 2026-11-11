package hardware

import "testing"

func TestSubnet(t *testing.T) {
	for _, c := range []struct{ ip, mask, want string }{
		{"172.19.24.36", "255.255.248.0", "172.19.24.0/21"},
		{"10.1.2.3", "255.255.255.0", "10.1.2.0/24"},
		{"10.1.2.3", "", ""},
		{"", "255.255.255.0", ""},
	} {
		if got := (Reservation{IP: c.ip, Netmask: c.mask}).Subnet(); got != c.want {
			t.Errorf("%s/%s: got %q want %q", c.ip, c.mask, got, c.want)
		}
	}
}
