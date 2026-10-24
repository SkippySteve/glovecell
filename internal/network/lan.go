package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LANPolicyName is the one CiliumNetworkPolicy per project namespace that
// carries LAN host grants. The controller owns it; nothing else may write it.
const LANPolicyName = "lan-hosts"

// LANPolicyLabel marks that policy so admission and audits can find it.
const LANPolicyLabel = "projects.agentwork.dev/policy"

// Limits per project. Ports are an explicit list: no ranges, so every
// permitted port is visible in the console, the audit record and the policy.
const (
	MaxLANHosts    = 8
	MaxLANPorts    = 16
	MaxLANNoteSize = 200
)

// LANPolicy is implemented by network providers that can grant a worker
// direct TCP to single LAN hosts without widening any other egress.
type LANPolicy interface {
	LANObject(namespace, uid string, hosts []api.LANHost) *unstructured.Unstructured
}

// privateParents are the only ranges an operator may open: LAN grants are a
// path to the home network, never a way around the HTTPS proxy to the internet.
var privateParents = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// alwaysRefused applies whatever the operator configures.
var alwaysRefused = []struct {
	prefix netip.Prefix
	reason string
}{
	{netip.MustParsePrefix("0.0.0.0/8"), "an unspecified address"},
	{netip.MustParsePrefix("127.0.0.0/8"), "a loopback address"},
	{netip.MustParsePrefix("169.254.0.0/16"), "a link-local or cloud metadata address"},
	{netip.MustParsePrefix("224.0.0.0/4"), "a multicast address"},
	{netip.MustParsePrefix("240.0.0.0/4"), "a broadcast or reserved address"},
}

// LANConfig is the operator's home-LAN grant setting. Empty Allowed turns the
// feature off. Denied lists node, VIP, pod, service and gateway ranges.
type LANConfig struct {
	Allowed, Denied []netip.Prefix
}

// ParseLANConfig reads LAN_GRANT_ALLOWED_CIDRS and LAN_GRANT_DENIED_CIDRS
// values: IPv4 prefixes separated by commas or spaces. A bare address in the
// denylist means that one address.
func ParseLANConfig(allowed, denied string) (LANConfig, error) {
	var c LANConfig
	var err error
	if c.Allowed, err = parsePrefixes(allowed, false); err != nil {
		return LANConfig{}, fmt.Errorf("LAN_GRANT_ALLOWED_CIDRS: %w", err)
	}
	if c.Denied, err = parsePrefixes(denied, true); err != nil {
		return LANConfig{}, fmt.Errorf("LAN_GRANT_DENIED_CIDRS: %w", err)
	}
	for _, p := range c.Allowed {
		if p.Bits() < 8 || !slices.ContainsFunc(privateParents, func(parent netip.Prefix) bool { return parent.Bits() <= p.Bits() && parent.Contains(p.Addr()) }) {
			return LANConfig{}, fmt.Errorf("LAN_GRANT_ALLOWED_CIDRS: %s is not inside 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16", p)
		}
	}
	if len(c.Allowed) > 0 && len(c.Denied) == 0 {
		return LANConfig{}, errors.New("LAN_GRANT_DENIED_CIDRS is required when LAN_GRANT_ALLOWED_CIDRS is set; list the node, VIP, pod, service and gateway ranges")
	}
	return c, nil
}

func parsePrefixes(raw string, bare bool) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		var p netip.Prefix
		var err error
		if bare && !strings.Contains(field, "/") {
			var a netip.Addr
			if a, err = netip.ParseAddr(field); err == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			}
		} else {
			p, err = netip.ParsePrefix(field)
		}
		if err != nil || !p.Addr().Is4() || p != p.Masked() {
			return nil, fmt.Errorf("%q is not a canonical IPv4 prefix", field)
		}
		out = append(out, p)
	}
	return out, nil
}

// Enabled reports whether an operator configured any LAN range.
func (c LANConfig) Enabled() bool { return len(c.Allowed) > 0 }

// AllowedStrings lists the configured parents for display.
func (c LANConfig) AllowedStrings() []string {
	out := make([]string, 0, len(c.Allowed))
	for _, p := range c.Allowed {
		out = append(out, p.String())
	}
	return out
}

// CheckAddress refuses everything except a single canonical IPv4 host inside
// an allowed parent and outside the denylist, the built-in ranges and nodes.
func (c LANConfig) CheckAddress(raw string, nodes []netip.Addr) error {
	if !c.Enabled() {
		return errors.New("LAN host grants are disabled on this controller")
	}
	if strings.ContainsAny(raw, "/-") {
		return errors.New("enter a single IPv4 address, not a range or CIDR")
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return errors.New("enter a single IPv4 address such as 192.168.1.20; hostnames are not accepted")
	}
	if !addr.Is4() {
		return errors.New("only IPv4 addresses are supported")
	}
	if addr.String() != raw {
		return errors.New("write the address in canonical dotted form")
	}
	if addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return errors.New("refused: a broadcast address")
	}
	for _, r := range alwaysRefused {
		if r.prefix.Contains(addr) {
			return errors.New("refused: " + r.reason)
		}
	}
	var parent netip.Prefix
	for _, p := range c.Allowed {
		if p.Contains(addr) && (!parent.IsValid() || p.Bits() > parent.Bits()) {
			parent = p
		}
	}
	if !parent.IsValid() {
		return fmt.Errorf("refused: %s is outside the allowed LAN ranges %s", addr, strings.Join(c.AllowedStrings(), ", "))
	}
	for _, d := range c.Denied {
		if d.Contains(addr) {
			return fmt.Errorf("refused: %s is in the operator denylist (%s): node, VIP, cluster or gateway addresses", addr, d)
		}
	}
	if slices.Contains(nodes, addr) {
		return fmt.Errorf("refused: %s is a cluster node address", addr)
	}
	// Network and broadcast addresses of the configured range, and of the /24
	// most home VLANs use, never name a single host.
	octets := addr.As4()
	if parent.Bits() <= 30 && (addr == parent.Masked().Addr() || addr == lastPrefixAddr(parent)) || octets[3] == 0 || octets[3] == 255 {
		return errors.New("refused: a network or broadcast address")
	}
	return nil
}

// CheckPorts accepts 1 to MaxLANPorts distinct TCP ports between 1 and 65535.
func CheckPorts(ports []int32) error {
	if len(ports) == 0 || len(ports) > MaxLANPorts {
		return fmt.Errorf("list between 1 and %d TCP ports", MaxLANPorts)
	}
	seen := map[int32]bool{}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			return errors.New("TCP ports must be between 1 and 65535")
		}
		if seen[p] {
			return fmt.Errorf("port %d is listed twice", p)
		}
		seen[p] = true
	}
	return nil
}

// CheckNote keeps notes short, single-line and printable.
func CheckNote(note string) error {
	if len(note) > MaxLANNoteSize {
		return fmt.Errorf("notes are limited to %d bytes", MaxLANNoteSize)
	}
	for _, r := range note {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return errors.New("notes must be printable text on one line")
		}
	}
	return nil
}

// Check validates a whole project's grants, including the per-project cap.
func (c LANConfig) Check(hosts []api.LANHost, nodes []netip.Addr) error {
	if len(hosts) > MaxLANHosts {
		return fmt.Errorf("a project can hold at most %d LAN host grants", MaxLANHosts)
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		if seen[h.Address] {
			return fmt.Errorf("LAN host %s is granted twice", h.Address)
		}
		seen[h.Address] = true
		if err := c.CheckAddress(h.Address, nodes); err != nil {
			return fmt.Errorf("LAN host %s: %w", h.Address, err)
		}
		if err := CheckPorts(h.Ports); err != nil {
			return fmt.Errorf("LAN host %s: %w", h.Address, err)
		}
		if err := CheckNote(h.Note); err != nil {
			return fmt.Errorf("LAN host %s: %w", h.Address, err)
		}
	}
	return nil
}

// NodeAddresses lists every address recorded on a Node, so a grant can never
// name a node even when the operator's denylist misses one.
func NodeAddresses(ctx context.Context, c client.Client) ([]netip.Addr, error) {
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil {
		return nil, errors.New("cannot list cluster nodes to exclude their addresses")
	}
	var out []netip.Addr
	for _, n := range nodes.Items {
		for _, a := range n.Status.Addresses {
			if addr, err := netip.ParseAddr(a.Address); err == nil {
				out = append(out, addr.Unmap())
			}
		}
	}
	return out, nil
}

// LANObject renders one egress rule per host: toCIDR <address>/32 with the
// listed TCP ports, selecting only this project's worker. It has no ingress
// section, so it changes nothing about inbound traffic, and Cilium allow
// rules are additive, so every other egress stays as the worker policy sets it.
// Nil means no grants: the policy must not exist.
func (Cilium) LANObject(ns, uid string, hosts []api.LANHost) *unstructured.Unstructured {
	if len(hosts) == 0 {
		return nil
	}
	sorted := slices.Clone(hosts)
	slices.SortFunc(sorted, func(a, b api.LANHost) int {
		x, errX := netip.ParseAddr(a.Address)
		y, errY := netip.ParseAddr(b.Address)
		if errX != nil || errY != nil {
			return strings.Compare(a.Address, b.Address)
		}
		return x.Compare(y)
	})
	egress := []any{}
	for _, h := range sorted {
		ports := slices.Clone(h.Ports)
		slices.Sort(ports)
		list := []any{}
		for _, p := range ports {
			list = append(list, map[string]any{"port": strconv.Itoa(int(p)), "protocol": "TCP"})
		}
		egress = append(egress, map[string]any{"toCIDR": []any{h.Address + "/32"}, "toPorts": []any{map[string]any{"ports": list}}})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy",
		"metadata": map[string]any{"name": LANPolicyName, "namespace": ns, "labels": map[string]any{api.OwnerLabel: uid, LANPolicyLabel: LANPolicyName}},
		"spec": map[string]any{
			"endpointSelector": map[string]any{"matchLabels": map[string]any{"projects.agentwork.dev/role": "worker", api.OwnerLabel: uid}},
			"egress":           egress,
		},
	}}
}
