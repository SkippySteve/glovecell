package network

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

// homeLAN mirrors a typical deployment: the whole 192.168.0.0/16 home network is
// a parent, and the node VLAN, VIPs, cluster ranges and gateways are refused.
func homeLAN(t *testing.T) LANConfig {
	t.Helper()
	c, err := ParseLANConfig("192.168.0.0/16, 172.16.5.0/24 10.244.0.0/16", "192.168.105.0/24,192.168.40.4,192.168.50.0/28,10.244.0.0/18,10.244.64.0/18,192.168.110.1,192.168.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLANConfigParsing(t *testing.T) {
	c := homeLAN(t)
	if !c.Enabled() || len(c.Allowed) != 3 || len(c.Denied) != 7 || c.Denied[1] != netip.MustParsePrefix("192.168.40.4/32") {
		t.Fatalf("parsed %#v", c)
	}
	off, err := ParseLANConfig("", "")
	if err != nil || off.Enabled() {
		t.Fatal("empty setting must disable grants", err)
	}
	if err := off.CheckAddress("192.168.110.2", nil); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatal("disabled feature accepted a grant", err)
	}
	for _, bad := range []struct{ allowed, denied string }{
		{"192.168.110.0/24", ""},                            // denylist is mandatory
		{"192.168.110.2/23", "192.168.105.0/24"},            // not canonical
		{"8.8.8.0/24", "192.168.105.0/24"},                  // public range would bypass the HTTPS proxy
		{"0.0.0.0/0", "192.168.105.0/24"},                   // default route
		{"10.0.0.0/7", "192.168.105.0/24"},                  // wider than a private parent
		{"100.64.0.0/10", "192.168.105.0/24"},               // CGNAT is not a home LAN
		{"fd00::/64", "192.168.105.0/24"},                   // IPv6
		{"192.168.110.0/24", "fd00::1"},                     // IPv6 in the denylist
		{"192.168.110.0/24", "lan.example"},                 // hostname
		{"192.168.110.0/24", "192.168.105.1-192.168.105.9"}, // range syntax
	} {
		if _, err := ParseLANConfig(bad.allowed, bad.denied); err == nil {
			t.Errorf("accepted allowed=%q denied=%q", bad.allowed, bad.denied)
		}
	}
}

func TestLANAddressRefusals(t *testing.T) {
	c := homeLAN(t)
	nodes := []netip.Addr{netip.MustParseAddr("192.168.105.4"), netip.MustParseAddr("192.168.105.5"), netip.MustParseAddr("192.168.40.4"), netip.MustParseAddr("172.16.5.77")}
	for _, ok := range []string{"192.168.110.2", "192.168.120.40", "172.16.5.20", "192.168.104.254"} {
		if err := c.CheckAddress(ok, nodes); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	refused := map[string]string{
		// Not a single IPv4 host.
		"192.168.110.0/24":            "not a range",
		"192.168.110.2/32":            "not a range",
		"192.168.110.2-192.168.110.9": "not a range",
		"devbox.lan":                  "hostnames",
		"":                            "hostnames",
		"192.168.110":                 "hostnames",
		"0192.168.110.2":              "hostnames",
		" 192.168.110.2":              "hostnames",
		"fd00::2":                     "only IPv4",
		"::ffff:192.168.110.2":        "only IPv4",
		"fe80::1%eth0":                "only IPv4",
		"::1":                         "only IPv4",
		// Built-in refusals apply whatever the operator configures.
		"127.0.0.1":       "loopback",
		"0.0.0.0":         "unspecified",
		"169.254.169.254": "metadata",
		"169.254.10.1":    "link-local",
		"224.0.0.251":     "multicast",
		"239.255.255.250": "multicast",
		"255.255.255.255": "broadcast",
		"240.0.0.1":       "reserved",
		// Outside the configured parents.
		"10.18.0.5":  "outside the allowed LAN ranges",
		"172.16.0.5": "outside the allowed LAN ranges",
		"172.16.6.5": "outside the allowed LAN ranges",
		"8.8.8.8":    "outside the allowed LAN ranges",
		// Operator denylist: node VLAN, API VIP, service VIPs, pod/service CIDRs, gateways.
		"192.168.105.4":   "denylist",
		"192.168.105.250": "denylist",
		"192.168.105.1":   "denylist",
		"192.168.50.3":    "denylist",
		"192.168.50.15":   "denylist",
		"10.244.0.1":      "denylist",
		"10.244.64.17":    "denylist",
		"10.244.127.254":  "denylist",
		"192.168.110.1":   "denylist",
		"192.168.1.1":     "denylist",
		"192.168.40.4":    "denylist",
		// Node addresses even when the denylist misses them.
		"172.16.5.77": "node address",
		// Network and broadcast addresses.
		"192.168.0.0":     "network or broadcast",
		"192.168.255.255": "network or broadcast",
		"192.168.110.255": "network or broadcast",
		"192.168.110.0":   "network or broadcast",
		"172.16.5.255":    "network or broadcast",
	}
	for address, want := range refused {
		err := c.CheckAddress(address, nodes)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", address, err, want)
		}
	}
	// The allowed parent 10.244.0.0/16 overlaps the cluster ranges; the denylist still wins.
	if err := c.CheckAddress("10.244.200.5", nil); err != nil {
		t.Errorf("address outside the cluster ranges but inside an allowed parent refused: %v", err)
	}
}

func TestLANPortsNotesAndProjectLimits(t *testing.T) {
	for _, ports := range [][]int32{{22}, {22, 80, 443, 8080}, {1, 65535}, make16()} {
		if err := CheckPorts(ports); err != nil {
			t.Errorf("%v refused: %v", ports, err)
		}
	}
	for _, ports := range [][]int32{nil, {}, {0}, {-1}, {65536}, {22, 22}, append(make16(), 17)} {
		if err := CheckPorts(ports); err == nil {
			t.Errorf("%v accepted", ports)
		}
	}
	for _, note := range []string{"line\nbreak", "tab\tseparated", "bell\a", strings.Repeat("x", MaxLANNoteSize+1), "bad \xff byte"} {
		if CheckNote(note) == nil {
			t.Errorf("note %q accepted", note)
		}
	}
	if CheckNote("Steve's desktop — SSH") != nil || CheckNote("") != nil {
		t.Error("printable note refused")
	}
	c := homeLAN(t)
	var hosts []api.LANHost
	for i := 1; i <= MaxLANHosts; i++ {
		hosts = append(hosts, api.LANHost{Address: "192.168.110." + string(rune('1'+i)), Ports: []int32{22}})
	}
	if err := c.Check(hosts, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Check(append(hosts, api.LANHost{Address: "192.168.111.2", Ports: []int32{22}}), nil); err == nil {
		t.Error("more than the per-project cap accepted")
	}
	if err := c.Check([]api.LANHost{{Address: "192.168.110.2", Ports: []int32{22}}, {Address: "192.168.110.2", Ports: []int32{80}}}, nil); err == nil {
		t.Error("duplicate address accepted")
	}
	if err := c.Check([]api.LANHost{{Address: "192.168.110.2", Ports: []int32{22}, Note: "a\nb"}}, nil); err == nil {
		t.Error("control characters in a stored note accepted")
	}
}

func make16() []int32 {
	out := []int32{}
	for i := int32(1); i <= MaxLANPorts; i++ {
		out = append(out, i)
	}
	return out
}

func TestCiliumLANObjectSelectsOnlyTheWorker(t *testing.T) {
	if (Cilium{}).LANObject("apc-x", "uid", nil) != nil {
		t.Fatal("no grants must mean no policy")
	}
	var provider Provider = Cilium{}
	if _, ok := provider.(LANPolicy); !ok {
		t.Fatal("Cilium must support LAN grants")
	}
	var native Provider = NetworkPolicy{}
	if _, ok := native.(LANPolicy); ok {
		t.Fatal("the unqualified NetworkPolicy provider must refuse LAN grants")
	}
	obj := (Cilium{}).LANObject("apc-x", "uid", []api.LANHost{{Address: "172.16.5.20", Ports: []int32{8080, 80}}, {Address: "192.168.110.2", Ports: []int32{22}}})
	raw, _ := json.Marshal(obj.Object)
	want := `{"apiVersion":"cilium.io/v2","kind":"CiliumNetworkPolicy","metadata":{"labels":{"projects.agentwork.dev/policy":"lan-hosts","projects.agentwork.dev/workspace-uid":"uid"},"name":"lan-hosts","namespace":"apc-x"},"spec":{"egress":[{"toCIDR":["172.16.5.20/32"],"toPorts":[{"ports":[{"port":"80","protocol":"TCP"},{"port":"8080","protocol":"TCP"}]}]},{"toCIDR":["192.168.110.2/32"],"toPorts":[{"ports":[{"port":"22","protocol":"TCP"}]}]}],"endpointSelector":{"matchLabels":{"projects.agentwork.dev/role":"worker","projects.agentwork.dev/workspace-uid":"uid"}}}}`
	if string(raw) != want {
		t.Fatalf("policy\n%s\nwant\n%s", raw, want)
	}
}
