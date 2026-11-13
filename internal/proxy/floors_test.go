package proxy

import (
	"testing"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
)

func TestPlatformDenyBeatsProjectAllow(t *testing.T) {
	cfg := Config{
		Platform: PlatformFloors{DenyAlways: []string{"*.evil.example", "blocked.example"}},
		Rules:    []api.HTTPSRule{{Host: "blocked.example", Methods: []string{"GET"}, Paths: []string{"/"}}},
	}
	if Allowed(cfg, "blocked.example", "GET", "/") {
		t.Fatal("project allow must lose to platform deny")
	}
	if Allowed(cfg, "a.evil.example", "GET", "/") {
		t.Fatal("wildcard platform deny must cover subdomains")
	}
}

func TestPlatformAllowBeatsMissingProjectRule(t *testing.T) {
	cfg := Config{
		Platform: PlatformFloors{AllowAlways: []string{"git.example.com"}},
		Rules:    []api.HTTPSRule{{Host: "other.example", Methods: []string{"GET"}, Paths: []string{"/"}}},
	}
	if !Allowed(cfg, "git.example.com", "GET", "/anything") {
		t.Fatal("platform allow.always should admit without a project rule")
	}
	cfg.Platform.DenyAlways = []string{"git.example.com"}
	if Allowed(cfg, "git.example.com", "GET", "/") {
		t.Fatal("platform deny must beat platform allow")
	}
}

func TestPlatformPortFloor(t *testing.T) {
	empty := PlatformFloors{}
	if !PlatformPortAllowed(empty, "443") || PlatformPortAllowed(empty, "8443") {
		t.Fatal("empty ports must keep 443-only")
	}
	capped := PlatformFloors{Ports: []int{443, 8443}}
	if !PlatformPortAllowed(capped, "8443") || PlatformPortAllowed(capped, "80") {
		t.Fatal("explicit port floor must be enforced")
	}
}

func TestValidateRejectsProjectRuleConflictingWithDeny(t *testing.T) {
	cfg := Config{
		Platform: PlatformFloors{DenyAlways: []string{"*.blocked.example"}},
		Rules:    []api.HTTPSRule{{Host: "app.blocked.example", Methods: []string{"GET"}, Paths: []string{"/"}}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected conflict with deny.always")
	}
	ok := Config{
		Platform: PlatformFloors{DenyAlways: []string{"blocked.example"}},
		Rules:    []api.HTTPSRule{{Host: "git.example.com", Methods: []string{"GET"}, Paths: []string{"/"}}},
	}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyFloorsAreInert(t *testing.T) {
	cfg := Config{Rules: []api.HTTPSRule{{Host: "git.example.com", Methods: []string{"GET"}, Paths: []string{"/"}}}}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if !Allowed(cfg, "git.example.com", "GET", "/") || Allowed(cfg, "other.example", "GET", "/") {
		t.Fatal("empty floors must not change project rule behaviour")
	}
}
