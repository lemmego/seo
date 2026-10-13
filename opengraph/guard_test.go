package opengraph

import (
	"errors"
	"net/netip"
	"testing"
)

// The table is the security boundary, so it is written as one: every address
// a fetch must refuse, with the reason it is dangerous.
func TestAddressesThatMustNeverBeReached(t *testing.T) {
	blocked := []struct {
		addr string
		why  string
	}{
		{"127.0.0.1", "loopback — every service bound to localhost"},
		{"127.1.2.3", "the whole of 127/8 is loopback, not just .0.1"},
		{"::1", "IPv6 loopback"},
		{"::ffff:127.0.0.1", "IPv4-mapped loopback, which every Is-predicate " +
			"answers false for until it is unmapped"},
		{"::ffff:10.0.0.1", "IPv4-mapped private"},
		{"10.0.0.1", "private"},
		{"10.255.255.255", "private"},
		{"172.16.0.1", "private"},
		{"172.31.255.255", "the top of 172.16/12"},
		{"192.168.1.1", "private"},
		{"fc00::1", "IPv6 unique-local"},
		{"fd12:3456::1", "IPv6 unique-local"},
		{"169.254.169.254", "cloud instance metadata — this one returns credentials"},
		{"169.254.0.1", "link-local"},
		{"fe80::1", "IPv6 link-local"},
		{"0.0.0.0", "unspecified, which routes to localhost on Linux"},
		{"::", "IPv6 unspecified"},
		{"100.64.0.1", "carrier-grade NAT"},
		{"224.0.0.1", "multicast"},
		{"255.255.255.255", "broadcast"},
		{"240.0.0.1", "reserved"},
		{"192.0.0.1", "IETF protocol assignments"},
		{"198.18.0.1", "benchmarking"},
		{"64:ff9b::7f00:1", "NAT64, which maps straight onto 127.0.0.1"},
	}

	for _, each := range blocked {
		t.Run(each.addr, func(t *testing.T) {
			addr, err := netip.ParseAddr(each.addr)
			if err != nil {
				t.Fatalf("the fixture does not parse: %v", err)
			}
			if err := allowAddr(addr); err == nil {
				t.Fatalf("%s was allowed, and it is %s", each.addr, each.why)
			} else if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("refused with %v, which is not ErrBlockedAddress", err)
			}
		})
	}
}

// And the guard must not be so broad that it refuses the public internet,
// which is the failure that would make link previews simply never work.
func TestOrdinaryPublicAddressesAreReachable(t *testing.T) {
	for _, candidate := range []string{
		"93.184.216.34", // example.com
		"1.1.1.1",
		"8.8.8.8",
		"172.15.255.255", // just below 172.16/12
		"172.32.0.1",     // just above it
		"100.63.255.255", // just below the CGNAT range
		"100.128.0.1",    // just above it
		"2606:4700::1111",
		"::ffff:93.184.216.34",
	} {
		addr := netip.MustParseAddr(candidate)
		if err := allowAddr(addr); err != nil {
			t.Errorf("%s was refused: %v", candidate, err)
		}
	}
}

// The boundaries of every range, because an off-by-one in a prefix is the
// kind of mistake a table of pretty addresses does not catch.
func TestRangeBoundaries(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"9.255.255.255", false}, {"10.0.0.0", true}, {"10.255.255.255", true}, {"11.0.0.0", false},
		{"192.167.255.255", false}, {"192.168.0.0", true}, {"192.168.255.255", true}, {"192.169.0.0", false},
		{"169.253.255.255", false}, {"169.254.0.0", true}, {"169.254.255.255", true}, {"169.255.0.0", false},
		{"239.255.255.255", true}, {"240.0.0.0", true},
	}
	for _, each := range cases {
		err := allowAddr(netip.MustParseAddr(each.addr))
		if each.blocked && err == nil {
			t.Errorf("%s should be blocked", each.addr)
		}
		if !each.blocked && err != nil {
			t.Errorf("%s should be reachable: %v", each.addr, err)
		}
	}
}
