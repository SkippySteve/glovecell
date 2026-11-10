package installation

import (
	"slices"
	"strings"
	"testing"
)

// Off: the controller mounts and reads the Forgejo token as before. On: only
// the forge agent's pod mounts forgejo-token; the controller gets a projected
// token for the agent's audience instead.
func TestChartForgeAgentMovesTheAdminToken(t *testing.T) {
	requireHelm(t)
	_, off, err := renderChart(t, databaseValues())
	if err != nil {
		t.Fatal(err)
	}
	if find(off, "Deployment", "forge-agent") != nil {
		t.Fatal("forge agent rendered while off")
	}
	if env, _ := controllerPod(t, off); env["FORGEJO_TOKEN_FILE"] != "/control-secrets/forgejo-token" || env["FORGE_AGENT_URL"] != "" {
		t.Fatalf("default controller env changed: %v", env)
	}

	values := databaseValues()
	values["forgeAgent"] = map[string]any{"enabled": true}
	values["automationAPI"] = map[string]any{"enabled": true}
	out, on, err := renderChart(t, values)
	if err != nil {
		t.Fatal(err)
	}
	env, volumes := controllerPod(t, on)
	if env["FORGEJO_TOKEN_FILE"] != "" || env["FORGE_AGENT_URL"] != "http://forge-agent.test-control-forge.svc:8443" || env["FORGE_AGENT_TOKEN_FILE"] != "/var/run/apc/forge-agent/token" {
		t.Fatalf("controller env %v", env)
	}
	_, keys := secretItems(volumes["control-secrets"])
	if slices.Contains(keys, "forgejo-token") || !slices.Contains(keys, "oidc-client-secret") || !slices.Contains(keys, "api-secret") {
		t.Fatalf("controller secret keys %v", keys)
	}
	src := dig(volumes["forge-agent-token"], "projected", "sources").([]any)
	if len(src) != 1 || dig(src[0], "serviceAccountToken", "audience") != "agent-project-controller-forge-agent" {
		t.Fatalf("projected token %v", src)
	}

	agent := find(on, "Deployment", "forge-agent")
	if agent == nil {
		t.Fatal("forge agent Deployment missing")
	}
	spec := dig(agent, "spec", "template", "spec").(map[string]any)
	if spec["serviceAccountName"] != "forge-agent" {
		t.Fatal("agent service account", spec["serviceAccountName"])
	}
	c := spec["containers"].([]any)[0].(map[string]any)
	if args := c["args"].([]any); len(args) != 1 || args[0] != "forge-agent" {
		t.Fatal("agent args", args)
	}
	agentEnv := map[string]string{}
	for _, e := range c["env"].([]any) {
		agentEnv[e.(map[string]any)["name"].(string)] = e.(map[string]any)["value"].(string)
	}
	if agentEnv["FORGE_AGENT_CALLER"] != "system:serviceaccount:test-control:controller" || agentEnv["FORGEJO_TOKEN_FILE"] != "/forgejo/forgejo-token" {
		t.Fatalf("agent env %v", agentEnv)
	}
	agentVolumes := spec["volumes"].([]any)
	if len(agentVolumes) != 1 {
		t.Fatalf("agent volumes %v", agentVolumes)
	}
	if _, k := secretItems(agentVolumes[0].(map[string]any)); len(k) != 1 || k[0] != "forgejo-token" {
		t.Fatalf("agent mounts %v", k)
	}
	if dig(agent, "metadata", "namespace") != "test-control-forge" {
		t.Fatal("agent namespace", dig(agent, "metadata", "namespace"))
	}
	crb := find(on, "ClusterRoleBinding", "test-control-qualification-forge-agent-review")
	if crb == nil || dig(crb, "roleRef", "name") != "system:auth-delegator" {
		t.Fatal("agent token review binding", crb)
	}
	for _, o := range on {
		if (o["kind"] == "RoleBinding" || o["kind"] == "ClusterRoleBinding") && o["subjects"] != nil {
			for _, s := range o["subjects"].([]any) {
				if dig(s, "name") == "forge-agent" && dig(o, "roleRef", "name") != "system:auth-delegator" {
					t.Fatalf("forge agent bound to %v", dig(o, "roleRef", "name"))
				}
			}
		}
	}
	np := find(on, "NetworkPolicy", "forge-agent-ingress")
	if np == nil || dig(np, "spec", "podSelector", "matchLabels", "app.kubernetes.io/name") != "forge-agent" {
		t.Fatal("forge agent ingress policy", np)
	}
	// The token lives only in the agent's own namespace: no class or mount in
	// the controller's namespace refers to it.
	if find(on, "ForgeClass", "") != nil && dig(find(on, "ForgeClass", ""), "spec", "driver") == "forgejo" {
		t.Fatal("default Forgejo class still points the controller at the token")
	}
	for _, o := range on {
		md, _ := o["metadata"].(map[string]any)
		if o["kind"] == "Namespace" && md["name"] == "test-control-forge" {
			continue
		}
		if o["kind"] != "ClusterRoleBinding" && o["kind"] != "Namespace" && md["name"] != nil && strings.HasPrefix(fmtName(o), "forge-agent") && md["namespace"] != "test-control-forge" {
			t.Fatalf("%s %s outside the agent namespace", o["kind"], md["name"])
		}
	}
	if find(on, "Namespace", "test-control-forge") == nil {
		t.Fatal("agent namespace missing")
	}
	_ = out
	values["forgeAgent"] = map[string]any{"enabled": true, "namespace": "test-control"}
	if _, _, err := renderChart(t, values); err == nil {
		t.Fatal("agent allowed in the controller's namespace")
	}
}

func fmtName(o map[string]any) string {
	n, _ := dig(o, "metadata", "name").(string)
	return n
}
