package runtime

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// privateRanges is the list of CIDR blocks that are considered private /
// link-local / reserved. We always block the cloud metadata endpoint
// (169.254.169.254) regardless of the ssrf_protection setting.
var (
	// alwaysBlockedRanges are blocked regardless of ssrf_protection flag.
	alwaysBlockedRanges []*net.IPNet

	// privateRanges are only blocked when ssrf_protection is true.
	privateRanges []*net.IPNet
)

func init() {
	for _, cidr := range []string{
		"169.254.0.0/16", // link-local / AWS+GCP metadata
		"100.64.0.0/10",  // CGNAT (RFC 6598) — no user need for this
	} {
		_, block, _ := net.ParseCIDR(cidr)
		alwaysBlockedRanges = append(alwaysBlockedRanges, block)
	}

	for _, cidr := range []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7",  // IPv6 ULA
		"fd00::/8",  // IPv6 ULA (subset)
	} {
		_, block, _ := net.ParseCIDR(cidr)
		privateRanges = append(privateRanges, block)
	}
}

// checkSSRF resolves rawURL and rejects requests to forbidden address ranges.
//
//   - 169.254.x / CGNAT are always blocked (cloud metadata endpoints).
//   - RFC-1918 private ranges are blocked when ssrfProtection is true.
//   - Loopback (127.x / ::1) is always ALLOWED so local MCP servers work.
//   - allowedHosts lists hostnames/IPs exempt from the private-range block.
func checkSSRF(rawURL string, ssrfProtection bool, allowedHosts []string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("ssrf: invalid URL: %w", err)
	}

	host := u.Hostname()

	// Explicit allow-list takes priority.
	for _, h := range allowedHosts {
		if strings.EqualFold(h, host) {
			return nil
		}
	}

	// Resolve the hostname to IPs (may be multiple for a round-robin DNS name).
	ips, err := net.LookupHost(host)
	if err != nil {
		// If resolution fails we let the actual request fail naturally.
		return nil
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}

		// Loopback is always allowed (local services, MCP servers).
		if ip.IsLoopback() {
			continue
		}

		// Always-blocked ranges (metadata endpoints, CGNAT).
		for _, block := range alwaysBlockedRanges {
			if block.Contains(ip) {
				return fmt.Errorf("ssrf: request to %s (%s) is blocked — cloud metadata and link-local addresses are not allowed", host, ipStr)
			}
		}

		// Private RFC-1918 ranges — only blocked when ssrf_protection is enabled.
		if ssrfProtection {
			for _, block := range privateRanges {
				if block.Contains(ip) {
					return fmt.Errorf("ssrf: request to %s (%s) is blocked — private network addresses require ssrf_protection: false or an explicit allow_private_hosts entry", host, ipStr)
				}
			}
		}
	}

	return nil
}
