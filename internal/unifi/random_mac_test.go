package unifi

import "testing"

func TestIsRandomMAC(t *testing.T) {
	cases := []struct {
		mac  string
		want bool
	}{
		// Locally administered: second hex digit 2, 6, A, E
		{"a2:bb:cc:dd:ee:ff", true},
		{"a6:bb:cc:dd:ee:ff", true},
		{"aa:bb:cc:dd:ee:ff", true},
		{"ae:bb:cc:dd:ee:ff", true},
		{"AA-BB-CC-DD-EE-FF", true},
		{"aabbccddeeff", true},
		// Globally unique (OUI-assigned)
		{"00:11:22:33:44:55", false},
		{"3c:22:fb:00:00:01", false},
		{"b8:27:eb:12:34:56", false},
		{"f0:9f:c2:aa:bb:cc", false},
		// Degenerate input must not panic
		{"", false},
		{"a", false},
		{":", false},
	}

	for _, c := range cases {
		if got := IsRandomMAC(c.mac); got != c.want {
			t.Errorf("IsRandomMAC(%q) = %v, want %v", c.mac, got, c.want)
		}
	}
}
