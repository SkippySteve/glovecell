package proxy

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

// PlatformFloors are operator rules no project AccessProfile, HTTPS rule or
// approval can override. Empty floors are inert (APC #106).
type PlatformFloors struct {
	DenyAlways  []string `json:"denyAlways,omitempty"`
	AllowAlways []string `json:"allowAlways,omitempty"`
	// Ports is the TCP port cap for CONNECT/dial. Empty keeps today's
	// hard-coded 443-only behaviour.
	Ports []int `json:"ports,omitempty"`
}

func validatePlatformHost(host string) error {
	if host == "" || len(host) > 253 {
		return errors.New("platform floor host required")
	}
	base, wildcard := strings.CutPrefix(host, "*.")
	check := host
	if wildcard {
		check = base
	}
	if !dnsHost.MatchString(check) || net.ParseIP(check) != nil || !strings.Contains(check, ".") {
		return fmt.Errorf("invalid platform floor host %q", host)
	}
	if wildcard && !WildcardAllowed(base) {
		return fmt.Errorf("platform wildcard floor host %q is not allowed", host)
	}
	return nil
}

func ValidatePlatformFloors(f PlatformFloors) error {
	if len(f.DenyAlways) > 64 || len(f.AllowAlways) > 64 || len(f.Ports) > 16 {
		return errors.New("platform floors exceed size limits")
	}
	for _, host := range append(append([]string{}, f.DenyAlways...), f.AllowAlways...) {
		if err := validatePlatformHost(host); err != nil {
			return err
		}
	}
	seen := map[int]bool{}
	for _, port := range f.Ports {
		if port < 1 || port > 65535 || seen[port] {
			return errors.New("platform floor ports must be unique TCP ports")
		}
		seen[port] = true
	}
	return nil
}

func platformHostListed(patterns []string, host string) bool {
	for _, pattern := range patterns {
		if hostMatches(pattern, host) {
			return true
		}
	}
	return false
}

// PlatformDenied reports a hard deny that wins over project allows and
// platform allows.
func PlatformDenied(f PlatformFloors, host string) bool {
	return platformHostListed(f.DenyAlways, host)
}

// PlatformAllowed reports a hard allow that every cell receives, still
// subject to PlatformDenied.
func PlatformAllowed(f PlatformFloors, host string) bool {
	if PlatformDenied(f, host) {
		return false
	}
	return platformHostListed(f.AllowAlways, host)
}

// PlatformPortAllowed enforces the port cap. Empty Ports keeps 443-only.
func PlatformPortAllowed(f PlatformFloors, port string) bool {
	n, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	if len(f.Ports) == 0 {
		return n == 443
	}
	for _, p := range f.Ports {
		if p == n {
			return true
		}
	}
	return false
}

// ProjectRulesConflictWithDeny reports project HTTPS rules that name a host
// covered by deny.always (exact or wildcard).
func ProjectRulesConflictWithDeny(f PlatformFloors, rules []api.HTTPSRule) error {
	for _, rule := range rules {
		if rule.Source != "" {
			continue
		}
		for _, deny := range f.DenyAlways {
			if rule.Host == deny || hostMatches(deny, rule.Host) {
				return fmt.Errorf("HTTPS rule host %q conflicts with platform deny.always", rule.Host)
			}
			if base, ok := strings.CutPrefix(rule.Host, "*."); ok {
				if deny == "*."+base || hostMatches(deny, "probe."+base) {
					return fmt.Errorf("HTTPS rule host %q conflicts with platform deny.always", rule.Host)
				}
			}
		}
	}
	return nil
}
