package mihomo

// privateDestinationRules is the embedded geoip:private-style destination policy.
// Keep this list versioned with the agent: startup must not download GeoIP data.
// Besides RFC1918/ULA it covers loopback, link-local, shared, documentation,
// multicast and reserved ranges (including standard IPv6 translation prefixes).
// See docs/architecture.md for scope and the source of the private range set.
func privateDestinationRules() []string {
	rules := make([]string, 0, 30)
	for _, cidr := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.31.196.0/24", "192.52.193.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	} {
		// Do not add no-resolve: a client-supplied domain must be checked too.
		rules = append(rules, "IP-CIDR,"+cidr+",REJECT")
	}
	for _, cidr := range []string{
		"::/127", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23",
		"2001:db8::/32", "2620:4f:8000::/48", "3fff::/20", "5f00::/16",
		"fc00::/7", "fe80::/10", "ff00::/8",
	} {
		rules = append(rules, "IP-CIDR6,"+cidr+",REJECT")
	}
	return rules
}
