package installation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func databaseValues() map[string]any {
	return map[string]any{"controllerImage": "registry.example.com/controller@sha256:" + strings.Repeat("a", 64), "workspaceImage": "registry.example.com/workspace@sha256:" + strings.Repeat("b", 64), "config": map[string]any{"FORGEJO_ORIGIN": "https://git.example.com", "FORGEJO_OWNER": "operator", "PUBLIC_ORIGIN": "https://projects.example.com", "OIDC_ISSUER": "https://identity.example.com/realm", "OIDC_CLIENT_ID": "projects"}}
}

// renderChart renders the chart with values and returns the raw output and
// the parsed objects.
func renderChart(t *testing.T, values map[string]any) ([]byte, []map[string]any, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.json")
	b, _ := json.Marshal(values)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("helm", "template", "qualification", "../../charts/agent-project-controller", "-n", "test-control", "-f", path).CombinedOutput()
	if err != nil {
		return out, nil, fmt.Errorf("%v: %s", err, out)
	}
	var objs []map[string]any
	for _, doc := range strings.Split(string(out), "\n---") {
		var o map[string]any
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil {
			t.Fatalf("parse rendered YAML: %v", err)
		}
		if o != nil {
			objs = append(objs, o)
		}
	}
	return out, objs, nil
}

func find(objs []map[string]any, kind, name string) map[string]any {
	for _, o := range objs {
		md, _ := o["metadata"].(map[string]any)
		if o["kind"] == kind && (name == "" || md["name"] == name) {
			return o
		}
	}
	return nil
}

func dig(o any, path ...string) any {
	for _, p := range path {
		m, ok := o.(map[string]any)
		if !ok {
			return nil
		}
		o = m[p]
	}
	return o
}

func requireHelm(t *testing.T) {
	if os.Getenv("APC_HELM_TEST") != "true" {
		t.Skip("set APC_HELM_TEST=true with Helm installed")
	}
}

func controllerPod(t *testing.T, objs []map[string]any) (env map[string]string, volumes map[string]map[string]any) {
	t.Helper()
	d := find(objs, "Deployment", "controller")
	if d == nil {
		t.Fatal("controller Deployment missing")
	}
	env, volumes = map[string]string{}, map[string]map[string]any{}
	containers := dig(d, "spec", "template", "spec", "containers").([]any)
	for _, e := range containers[0].(map[string]any)["env"].([]any) {
		m := e.(map[string]any)
		v, _ := m["value"].(string)
		env[m["name"].(string)] = v
	}
	for _, v := range dig(d, "spec", "template", "spec", "volumes").([]any) {
		m := v.(map[string]any)
		volumes[m["name"].(string)] = m
	}
	return env, volumes
}

func secretItems(v map[string]any) (string, []string) {
	s := v["secret"].(map[string]any)
	var keys []string
	for _, it := range s["items"].([]any) {
		keys = append(keys, it.(map[string]any)["key"].(string))
	}
	return s["secretName"].(string), keys
}

func TestHelmDatabaseCloudNativePG(t *testing.T) {
	requireHelm(t)
	out, objs, err := renderChart(t, databaseValues())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("kind: Secret\n")) {
		t.Fatal("chart renders a Secret")
	}
	c := find(objs, "Cluster", "qualification-db")
	if c == nil || c["apiVersion"] != "postgresql.cnpg.io/v1" {
		t.Fatalf("CloudNativePG Cluster missing:\n%s", out)
	}
	for path, want := range map[string]any{
		"spec.instances": float64(1), "spec.enablePDB": false, "spec.enableSuperuserAccess": false,
		"spec.bootstrap.initdb.database": "apc", "spec.bootstrap.initdb.owner": "apc", "spec.storage.size": "5Gi",
		"spec.monitoring.enablePodMonitor": false, "spec.resources.limits.memory": "1Gi",
	} {
		if got := dig(c, strings.Split(path, ".")...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if dig(c, "spec", "storage", "storageClass") != nil || dig(c, "spec", "backup") != nil || dig(c, "spec", "plugins") != nil || find(objs, "ScheduledBackup", "") != nil {
		t.Fatal("default Cluster sets a storage class or backups")
	}
	if hba := fmt.Sprint(dig(c, "spec", "postgresql", "pg_hba")); !strings.Contains(hba, "hostnossl all all all reject") {
		t.Fatalf("plaintext not refused: %s", hba)
	}
	np := find(objs, "NetworkPolicy", "qualification-db")
	if np == nil || fmt.Sprint(dig(np, "spec", "podSelector", "matchLabels", "cnpg.io/cluster")) != "qualification-db" || fmt.Sprint(dig(np, "spec", "policyTypes")) != "[Ingress]" {
		t.Fatalf("database NetworkPolicy: %v", np)
	}
	rules := dig(np, "spec", "ingress").([]any)
	if len(rules) != 2 || !strings.Contains(fmt.Sprint(rules[0]), "app.kubernetes.io/name:agent-project-controller") || !strings.Contains(fmt.Sprint(rules[0]), "port:5432") ||
		!strings.Contains(fmt.Sprint(rules[1]), "kubernetes.io/metadata.name:cnpg-system") {
		t.Fatalf("database ingress: %v", rules)
	}
	env, volumes := controllerPod(t, objs)
	if env["DATABASE_URL_FILE"] != "/var/run/apc/database/uri" || env["DATABASE_CA_FILE"] != "/var/run/apc/database-ca/ca.crt" || env["HISTORY_RETENTION"] != "8760h" || env["PROXY_SUMMARY_RETENTION"] != "720h" || env["DATABASE_MAX_CONNS"] != "10" {
		t.Fatalf("controller database env: %v", env)
	}
	if name, keys := secretItems(volumes["database"]); name != "qualification-db-app" || strings.Join(keys, ",") != "uri" {
		t.Fatalf("database volume: %s %v", name, keys)
	}
	if name, keys := secretItems(volumes["database-ca"]); name != "qualification-db-ca" || strings.Join(keys, ",") != "ca.crt" {
		t.Fatalf("CA volume mounts more than ca.crt: %s %v", name, keys)
	}
	if !bytes.Contains(out, []byte("OIDC_APPROVER_GROUPS: \"agent-project-approvers\"")) {
		t.Fatal("approver group not configured")
	}
	svc := find(objs, "Service", "controller")
	if dig(svc, "spec", "publishNotReadyAddresses") != true {
		t.Fatal("console Service hides the console while the database is down")
	}

	// Options.
	v := databaseValues()
	v["database"] = map[string]any{"cloudnativepg": map[string]any{
		"name": "apc", "instances": 3, "imageName": "ghcr.io/cloudnative-pg/postgresql:18.6@sha256:" + strings.Repeat("c", 64),
		"storage":          map[string]any{"size": "20Gi", "storageClass": "local-path"},
		"monitoring":       map[string]any{"enablePodMonitor": true},
		"resources":        map[string]any{"requests": map[string]any{"cpu": "250m"}},
		"extraIngressFrom": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "backup"}}}},
		"backup": map[string]any{"enabled": true, "method": "barmanObjectStore", "retentionPolicy": "14d",
			"barmanObjectStore": map[string]any{"destinationPath": "s3://backups/apc", "s3Credentials": map[string]any{"inheritFromIAMRole": true}}},
	}}
	_, objs, err = renderChart(t, v)
	if err != nil {
		t.Fatal(err)
	}
	c = find(objs, "Cluster", "apc")
	if c == nil || dig(c, "spec", "instances") != float64(3) || dig(c, "spec", "enablePDB") != true || dig(c, "spec", "storage", "storageClass") != "local-path" || dig(c, "spec", "storage", "size") != "20Gi" ||
		dig(c, "spec", "monitoring", "enablePodMonitor") != true || dig(c, "spec", "backup", "barmanObjectStore", "destinationPath") != "s3://backups/apc" || dig(c, "spec", "backup", "retentionPolicy") != "14d" ||
		!strings.HasSuffix(fmt.Sprint(dig(c, "spec", "imageName")), strings.Repeat("c", 64)) || dig(c, "spec", "resources", "requests", "cpu") != "250m" {
		t.Fatalf("Cluster options: %v", c)
	}
	if sb := find(objs, "ScheduledBackup", "apc"); sb == nil || dig(sb, "spec", "method") != "barmanObjectStore" || dig(sb, "spec", "cluster", "name") != "apc" {
		t.Fatalf("ScheduledBackup: %v", sb)
	}
	if np := find(objs, "NetworkPolicy", "apc"); !strings.Contains(fmt.Sprint(np), "app:backup") || !strings.Contains(fmt.Sprint(np), "port:9187") {
		t.Fatalf("NetworkPolicy extras: %v", np)
	}
	_, volumes = controllerPod(t, objs)
	if name, _ := secretItems(volumes["database"]); name != "apc-app" {
		t.Fatalf("named Cluster Secret: %s", name)
	}
	v["database"] = map[string]any{"cloudnativepg": map[string]any{"backup": map[string]any{"enabled": true, "plugin": map[string]any{"parameters": map[string]any{"barmanObjectName": "apc-store"}}}}}
	_, objs, err = renderChart(t, v)
	if err != nil {
		t.Fatal(err)
	}
	c = find(objs, "Cluster", "qualification-db")
	if !strings.Contains(fmt.Sprint(dig(c, "spec", "plugins")), "barmanObjectName:apc-store") || !strings.Contains(fmt.Sprint(dig(c, "spec", "plugins")), "isWALArchiver:true") {
		t.Fatalf("plugin backup: %v", dig(c, "spec", "plugins"))
	}
	if sb := find(objs, "ScheduledBackup", ""); dig(sb, "spec", "method") != "plugin" || dig(sb, "spec", "pluginConfiguration", "name") != "barman-cloud.cloudnative-pg.io" {
		t.Fatalf("plugin ScheduledBackup: %v", sb)
	}

	for name, bad := range map[string]map[string]any{
		"mode":           {"mode": "sqlite"},
		"short history":  {"historyRetention": "1h"},
		"minutes":        {"proxySummaryRetention": "90m"},
		"no digest":      {"cloudnativepg": map[string]any{"imageName": "postgres:18"}},
		"no instances":   {"cloudnativepg": map[string]any{"instances": 0}},
		"barman no path": {"cloudnativepg": map[string]any{"backup": map[string]any{"enabled": true, "method": "barmanObjectStore"}}},
		"unknown key":    {"password": "x"},
	} {
		v := databaseValues()
		v["database"] = bad
		if _, _, err := renderChart(t, v); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	v = databaseValues()
	v["config"].(map[string]any)["DATABASE_URL_FILE"] = "/tmp/x"
	if _, _, err := renderChart(t, v); err == nil {
		t.Error("config.DATABASE_URL_FILE accepted")
	}
}

func TestHelmDatabaseExternal(t *testing.T) {
	requireHelm(t)
	v := databaseValues()
	v["database"] = map[string]any{"mode": "external"}
	if _, _, err := renderChart(t, v); err == nil || !strings.Contains(err.Error(), "existingSecret") {
		t.Fatalf("external without a Secret: %v", err)
	}
	v["database"] = map[string]any{"mode": "external", "external": map[string]any{"existingSecret": "apc-postgres", "uriKey": "dsn"}}
	_, objs, err := renderChart(t, v)
	if err != nil {
		t.Fatal(err)
	}
	if find(objs, "Cluster", "") != nil || find(objs, "NetworkPolicy", "qualification-db") != nil {
		t.Fatal("external mode renders a CloudNativePG Cluster")
	}
	env, volumes := controllerPod(t, objs)
	if _, ok := env["DATABASE_CA_FILE"]; ok || volumes["database-ca"] != nil {
		t.Fatal("CA file set without caKey")
	}
	if name, keys := secretItems(volumes["database"]); name != "apc-postgres" || strings.Join(keys, ",") != "dsn" || env["DATABASE_URL_FILE"] != "/var/run/apc/database/uri" {
		t.Fatalf("external Secret: %s %v", name, keys)
	}
	v["database"].(map[string]any)["external"].(map[string]any)["caKey"] = "ca.pem"
	_, objs, err = renderChart(t, v)
	if err != nil {
		t.Fatal(err)
	}
	env, volumes = controllerPod(t, objs)
	if name, keys := secretItems(volumes["database-ca"]); name != "apc-postgres" || strings.Join(keys, ",") != "ca.pem" || env["DATABASE_CA_FILE"] != "/var/run/apc/database-ca/ca.crt" {
		t.Fatalf("external CA: %s %v %v", name, keys, env)
	}
}

// TestHelmDatabaseKubeconform validates both modes with kubeconform, using
// the CloudNativePG CRD schemas from APC_CNPG_CRDS (a CloudNativePG release
// manifest or CRD file). Skipped unless kubeconform and the CRDs are present.
func TestHelmDatabaseKubeconform(t *testing.T) {
	requireHelm(t)
	crds := os.Getenv("APC_CNPG_CRDS")
	if _, err := exec.LookPath("kubeconform"); err != nil || crds == "" {
		t.Skip("set APC_CNPG_CRDS to a CloudNativePG CRD manifest and install kubeconform")
	}
	raw, err := os.ReadFile(crds)
	if err != nil {
		t.Fatal(err)
	}
	schemas := t.TempDir()
	found := 0
	for _, doc := range strings.Split(string(raw), "\n---") {
		var crd struct {
			Kind string
			Spec struct {
				Group    string
				Names    struct{ Kind string }
				Versions []struct {
					Name   string
					Schema struct {
						OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
					}
				}
			}
		}
		if yaml.Unmarshal([]byte(doc), &crd) != nil || crd.Kind != "CustomResourceDefinition" || crd.Spec.Group != "postgresql.cnpg.io" {
			continue
		}
		dir := filepath.Join(schemas, crd.Spec.Group)
		os.MkdirAll(dir, 0o755)
		for _, v := range crd.Spec.Versions {
			strict(v.Schema.OpenAPIV3Schema)
			b, _ := json.Marshal(v.Schema.OpenAPIV3Schema)
			os.WriteFile(filepath.Join(dir, strings.ToLower(crd.Spec.Names.Kind)+"_"+v.Name+".json"), b, 0o644)
			found++
		}
	}
	if found == 0 {
		t.Fatalf("no postgresql.cnpg.io CRDs in %s", crds)
	}
	backup := databaseValues()
	backup["database"] = map[string]any{"cloudnativepg": map[string]any{"instances": 2, "monitoring": map[string]any{"enablePodMonitor": true},
		"backup": map[string]any{"enabled": true, "plugin": map[string]any{"parameters": map[string]any{"barmanObjectName": "apc-store"}}}}}
	barman := databaseValues()
	barman["database"] = map[string]any{"cloudnativepg": map[string]any{"backup": map[string]any{"enabled": true, "method": "barmanObjectStore",
		"barmanObjectStore": map[string]any{"destinationPath": "s3://b/apc", "s3Credentials": map[string]any{"inheritFromIAMRole": true}}}}}
	external := databaseValues()
	external["database"] = map[string]any{"mode": "external", "external": map[string]any{"existingSecret": "pg", "caKey": "ca.crt"}}
	validate := func(manifest []byte) ([]byte, error) {
		cmd := exec.Command("kubeconform", "-strict", "-summary", "-kubernetes-version", "1.34.1",
			"-schema-location", "default", "-schema-location", filepath.Join(schemas, "{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json"),
			"-skip", "ValidatingAdmissionPolicy,ValidatingAdmissionPolicyBinding,AccessProfile,OrchestratorClass,ForgeClass,ProviderClass,HTTPRoute")
		cmd.Stdin = bytes.NewReader(manifest)
		return cmd.CombinedOutput()
	}
	// The schema check is real: an unknown Cluster field is refused.
	out, _, err := renderChart(t, databaseValues())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := validate(bytes.Replace(out, []byte("  enableSuperuserAccess: false"), []byte("  enableSuperuserAccess: false\n  notACNPGField: true"), 1)); err == nil {
		t.Fatalf("unknown Cluster field accepted: %s", result)
	}
	for name, v := range map[string]map[string]any{"cloudnativepg": databaseValues(), "plugin backup": backup, "barman backup": barman, "external": external} {
		out, _, err := renderChart(t, v)
		if err != nil {
			t.Fatal(err)
		}
		result, err := validate(out)
		if err != nil {
			t.Fatalf("%s: kubeconform: %v\n%s", name, err, result)
		}
		if !strings.Contains(string(result), "Invalid: 0, Errors: 0") {
			t.Fatalf("%s: %s", name, result)
		}
		t.Logf("%s: %s", name, strings.TrimSpace(string(result)))
	}
}

// strict forbids unknown fields in a CRD schema, as openapi2jsonschema
// --strict does, except where the schema preserves unknown fields.
func strict(schema map[string]any) {
	if props, ok := schema["properties"].(map[string]any); ok {
		if _, set := schema["additionalProperties"]; !set && schema["x-kubernetes-preserve-unknown-fields"] != true {
			schema["additionalProperties"] = false
		}
		for _, p := range props {
			if m, ok := p.(map[string]any); ok {
				strict(m)
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties"} {
		if m, ok := schema[key].(map[string]any); ok {
			strict(m)
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := schema[key].([]any); ok {
			for _, x := range list {
				if m, ok := x.(map[string]any); ok {
					strict(m)
				}
			}
		}
	}
}
