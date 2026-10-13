package opengraph

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// Networks that must never be reached by a fetch of a URL somebody typed.
//
// The link-local range is the one worth naming: 169.254.169.254 is the
// instance metadata endpoint on AWS, GCP, Azure and DigitalOcean, and reading
// it returns credentials. Most published SSRF incidents are that one address.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, includes 255.255.255.255
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64, which maps straight onto IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
}

// allowAddr reports whether a resolved address may be connected to.
//
// It takes a netip.Addr rather than a host name on purpose: a name is not a
// destination, and every interesting bypass of a name-based check works by
// making the name resolve to something other than what was checked.
func allowAddr(addr netip.Addr) error {
	// An IPv4-mapped IPv6 address is the first bypass to close: ::ffff:127.0.0.1
	// is loopback, and every Is… predicate below answers false for it in its
	// mapped form.
	addr = addr.Unmap()

	if !addr.IsValid() {
		return fmt.Errorf("%w: the address is not valid", ErrBlockedAddress)
	}
	switch {
	case addr.IsLoopback():
		return fmt.Errorf("%w: %s is loopback", ErrBlockedAddress, addr)
	case addr.IsPrivate():
		// 10/8, 172.16/12, 192.168/16 and IPv6 unique-local fc00::/7.
		return fmt.Errorf("%w: %s is a private address", ErrBlockedAddress, addr)
	case addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast():
		return fmt.Errorf("%w: %s is link-local, which is where cloud "+
			"instance metadata lives", ErrBlockedAddress, addr)
	case addr.IsUnspecified():
		return fmt.Errorf("%w: %s is the unspecified address", ErrBlockedAddress, addr)
	case addr.IsMulticast(), addr.IsInterfaceLocalMulticast():
		return fmt.Errorf("%w: %s is multicast", ErrBlockedAddress, addr)
	}
	for _, prefix := range blocked {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: %s is inside the reserved range %s",
				ErrBlockedAddress, addr, prefix)
		}
	}
	return nil
}

// control is installed on the dialer, which is the only place this check is
// worth making.
//
// It runs after the name has been resolved and before the socket connects, so
// what it inspects is the address the connection will actually use. Resolving
// the name separately and checking that instead leaves a window in which the
// second lookup — the one the dialer makes — returns something else: a DNS
// rebind, where a hostname answers with a public address for the check and
// 127.0.0.1 a moment later. Checking here cannot be raced, because there is no
// second lookup.
//
// It also re-runs for every redirect hop, since each one dials again, which is
// the other half: a public URL that 302s to http://169.254.169.254 is the
// ordinary shape of this attack.
func (f *Fetcher) control(network, address string, _ syscall.RawConn) error {
	if f.allowPrivate {
		return nil
	}
	switch network {
	case "tcp4", "tcp6", "tcp":
	default:
		return fmt.Errorf("%w: %q is not a TCP network", ErrBlockedAddress, network)
	}

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q is not an address and port", ErrBlockedAddress, address)
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		// The dialer hands Control a literal address. Anything else means an
		// assumption here is wrong, so refuse rather than guess.
		return fmt.Errorf("%w: %q did not parse as an IP address", ErrBlockedAddress, host)
	}
	return allowAddr(addr)
}
