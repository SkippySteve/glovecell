package installation

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testHubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl phone"

func renderGloveHub(t *testing.T, mutate func(values map[string]any)) ([]byte, error) {
	t.Helper()
	values := map[string]any{
		"controllerImage":     "registry.example.com/controller@sha256:" + strings.Repeat("a", 64),
		"workspaceImage":      "registry.example.com/workspace@sha256:" + strings.Repeat("b", 64),
		"config":              map[string]any{"FORGEJO_ORIGIN": "https://git.example.com", "FORGEJO_OWNER": "operator", "PUBLIC_ORIGIN": "https://projects.example.com", "OIDC_ISSUER": "https://identity.example.com/realm", "OIDC_CLIENT_ID": "projects"},
		"herdrCore":           map[string]any{"enabled": true},
		"orchestratorClasses": map[string]any{"herdr": map[string]any{"enabled": true}},
		"gloveHub": map[string]any{
			"enabled":        true,
			"image":          "registry.example.com/glove-hub@sha256:" + strings.Repeat("c", 64),
			"authorizedKeys": []string{testHubKey},
			"service":        map[string]any{"type": "LoadBalancer"},
			"networkPolicy":  map[string]any{"sshFromCIDRs": []string{"10.0.0.0/24"}},
		},
	}
	if mutate != nil {
		mutate(values)
	}
	path := filepath.Join(t.TempDir(), "values.json")
	b, _ := json.Marshal(values)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return exec.Command("helm", "template", "qualification", "../../charts/agent-project-controller", "-n", "test-control", "-f", path).CombinedOutput()
}

func gloveHub(values map[string]any) map[string]any { return values["gloveHub"].(map[string]any) }

func TestHelmGloveHub(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
	out, err := renderGloveHub(t, func(v map[string]any) { delete(v, "gloveHub") })
	if err != nil {
		t.Fatalf("default render: %v: %s", err, out)
	}
	if bytes.Contains(out, []byte("glove-hub")) || bytes.Contains(out, []byte("GLOVE_HUB")) {
		t.Fatal("glove-hub rendered while disabled")
	}

	out, err = renderGloveHub(t, nil)
	if err != nil {
		t.Fatalf("render: %v: %s", err, out)
	}
	docs := voiceDocs(t, out)
	const ns = "test-control-glove-hub"
	ns0 := docs["Namespace/"+ns]
	if ns0 == nil {
		t.Fatal("no glove-hub namespace")
	}
	deploy := docs["Deployment/glove-hub"]
	if deploy == nil {
		t.Fatal("no glove-hub Deployment")
	}
	pod := deploy["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if pod["serviceAccountName"] != "glove-hub" {
		t.Fatalf("service account %v", pod["serviceAccountName"])
	}
	for _, c := range pod["containers"].([]any) {
		sc := c.(map[string]any)["securityContext"].(map[string]any)
		if sc["readOnlyRootFilesystem"] != true || sc["allowPrivilegeEscalation"] != false {
			t.Fatalf("container %v security %v", c.(map[string]any)["name"], sc)
		}
	}

	// The exec role is bound only per cell by the controller.
	role := docs["ClusterRole/test-control-qualification-glove-hub-exec"]
	if role == nil {
		t.Fatal("no exec ClusterRole")
	}
	for key, d := range docs {
		if strings.HasPrefix(key, "ClusterRoleBinding/") && strings.Contains(string(mustJSON(t, d)), "glove-hub") {
			t.Fatalf("%s grants glove-hub cluster-wide", key)
		}
	}
	controller := string(mustJSON(t, docs["ClusterRole/test-control-qualification"]))
	if !strings.Contains(controller, "test-control-qualification-glove-hub-exec") {
		t.Fatal("controller cannot bind the glove-hub exec role")
	}
	config := docs["ConfigMap/controller-config"]["data"].(map[string]any)
	if config["GLOVE_HUB_EXEC_CLUSTER_ROLE"] != "test-control-qualification-glove-hub-exec" || config["GLOVE_HUB_NAMESPACE"] != ns || config["GLOVE_HUB_SERVICE_ACCOUNT"] != "glove-hub" {
		t.Fatalf("controller config %v", config)
	}

	hub := docs["ConfigMap/glove-hub"]["data"].(map[string]any)
	if !strings.Contains(hub["sshd_config"].(string), "ForceCommand /usr/local/bin/herdr-core connect") {
		t.Fatal("sshd does not force herdr-core connect")
	}
	if strings.TrimSpace(hub["authorized_keys"].(string)) != "restrict "+testHubKey {
		t.Fatalf("authorized_keys %q", hub["authorized_keys"])
	}
	policy := string(mustJSON(t, docs["CiliumNetworkPolicy/glove-hub"]))
	if !strings.Contains(policy, `"fromCIDR":["10.0.0.0/24"]`) || !strings.Contains(policy, `"toEntities":["kube-apiserver"]`) {
		t.Fatalf("policy %s", policy)
	}
	if docs["NetworkPolicy/default-deny"] == nil {
		t.Fatal("no default-deny in the hub namespace")
	}
	if docs["Service/glove-hub"]["spec"].(map[string]any)["externalTrafficPolicy"] != "Local" {
		t.Fatal("LoadBalancer does not keep the client address")
	}

	// No client networks: nothing gets in.
	out, err = renderGloveHub(t, func(v map[string]any) { delete(gloveHub(v), "networkPolicy") })
	if err != nil {
		t.Fatalf("render: %v: %s", err, out)
	}
	if p := string(mustJSON(t, voiceDocs(t, out)["CiliumNetworkPolicy/glove-hub"])); !strings.Contains(p, "ingressDeny") || strings.Contains(p, "fromCIDR") {
		t.Fatalf("policy without sshFromCIDRs: %s", p)
	}

	for name, mutate := range map[string]func(map[string]any){
		"herdr-core off": func(v map[string]any) {
			v["herdrCore"] = map[string]any{"enabled": false}
			v["orchestratorClasses"] = map[string]any{"herdr": map[string]any{"enabled": false}}
		},
		"tag image":        func(v map[string]any) { gloveHub(v)["image"] = "registry.example.com/glove-hub:latest" },
		"key with options": func(v map[string]any) { gloveHub(v)["authorizedKeys"] = []string{`command="sh" ` + testHubKey} },
		"rsa key":          func(v map[string]any) { gloveHub(v)["authorizedKeys"] = []string{"ssh-rsa AAAAB3NzaC1yc2E= x"} },
		"two keys in one":  func(v map[string]any) { gloveHub(v)["authorizedKeys"] = []string{testHubKey + "\n" + testHubKey} },
		"bad cidr": func(v map[string]any) {
			gloveHub(v)["networkPolicy"] = map[string]any{"sshFromCIDRs": []string{"0.0.0.0/0 evil"}}
		},
		"same namespace": func(v map[string]any) { gloveHub(v)["namespace"] = "test-control" },
		"not cilium": func(v map[string]any) {
			v["config"].(map[string]any)["NETWORK_PROVIDER"] = "kubernetes"
		},
	} {
		if out, err := renderGloveHub(t, mutate); err == nil {
			t.Errorf("%s: rendered: %s", name, out)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
