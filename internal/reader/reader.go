// Package reader provides projected cluster state and numeric telemetry. Raw
// workload documents and arbitrary MCP tool output never cross this boundary.
package reader

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var resources = map[string][]string{
	"v1":                               {"pods", "services", "endpoints", "namespaces", "nodes", "events", "persistentvolumeclaims", "persistentvolumes", "resourcequotas", "limitranges"},
	"apps/v1":                          {"deployments", "statefulsets", "daemonsets", "replicasets"},
	"batch/v1":                         {"jobs", "cronjobs"},
	"networking.k8s.io/v1":             {"ingresses", "networkpolicies", "ingressclasses"},
	"discovery.k8s.io/v1":              {"endpointslices"},
	"storage.k8s.io/v1":                {"storageclasses", "csidrivers"},
	"metrics.k8s.io/v1beta1":           {"pods", "nodes"},
	"cilium.io/v2":                     {"ciliumnetworkpolicies", "ciliumclusterwidenetworkpolicies", "ciliumendpoints", "ciliumnodes"},
	"source.toolkit.fluxcd.io/v1":      {"gitrepositories", "helmrepositories"},
	"source.toolkit.fluxcd.io/v1beta2": {"helmcharts"},
	"kustomize.toolkit.fluxcd.io/v1":   {"kustomizations"},
	"helm.toolkit.fluxcd.io/v2":        {"helmreleases"},
}
var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,252}$`)
var quantity = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:m|n|u|Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$`)

func K8sAllowed(method, path string) bool {
	if method != "GET" && method != "HEAD" {
		return false
	}
	if strings.ContainsAny(path, "%\\") || strings.Contains(path, "//") {
		return false
	}
	for _, s := range strings.Split(path, "/") {
		if s == "." || s == ".." {
			return false
		}
	}
	if path == "/api" || path == "/api/v1" || path == "/apis" || path == "/version" {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	version := ""
	var rest []string
	if len(parts) >= 2 && parts[0] == "api" && parts[1] == "v1" {
		version = "v1"
		rest = parts[2:]
	} else if len(parts) >= 3 && parts[0] == "apis" {
		version = parts[1] + "/" + parts[2]
		rest = parts[3:]
	} else if len(parts) == 2 && parts[0] == "apis" {
		for v := range resources {
			if strings.HasPrefix(v, parts[1]+"/") {
				return true
			}
		}
		return false
	} else {
		return false
	}
	allowed, ok := resources[version]
	if !ok {
		return false
	}
	if len(rest) == 0 {
		return true
	}
	if len(rest) >= 3 && rest[0] == "namespaces" {
		if !identifier.MatchString(rest[1]) {
			return false
		}
		rest = rest[2:]
	}
	if len(rest) < 1 || len(rest) > 2 {
		return false
	}
	for _, name := range allowed {
		if rest[0] == name {
			return len(rest) == 1 || identifier.MatchString(rest[1])
		}
	}
	return false
}

type object = map[string]any

func obj(v any) object { x, _ := v.(map[string]any); return x }
func id(v any) any {
	s, ok := v.(string)
	if ok && identifier.MatchString(s) {
		return s
	}
	return nil
}
func number(v any) any {
	n, ok := v.(float64)
	if ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
		return n
	}
	return nil
}
func copyFields(dst, src object, fields []string, project func(any) any) {
	for _, f := range fields {
		if v := project(src[f]); v != nil {
			dst[f] = v
		}
	}
}

// ProjectK8s uses an allowlist, never a credential-name denylist. Fields that
// may carry free text, credentials, commands, URLs or arbitrary maps are absent.
func ProjectK8s(v object) object {
	out := object{}
	copyFields(out, v, []string{"apiVersion", "kind"}, id)
	meta := object{}
	copyFields(meta, obj(v["metadata"]), []string{"name", "namespace", "uid", "resourceVersion"}, id)
	if len(meta) > 0 {
		out["metadata"] = meta
	}
	if items, ok := v["items"].([]any); ok {
		safe := []any{}
		for _, item := range items {
			safe = append(safe, ProjectK8s(obj(item)))
		}
		out["items"] = safe
		return out
	}
	status := object{}
	source := obj(v["status"])
	copyFields(status, source, []string{"replicas", "readyReplicas", "availableReplicas", "updatedReplicas", "unavailableReplicas", "observedGeneration", "currentNumberScheduled", "desiredNumberScheduled", "numberReady", "numberAvailable", "active", "succeeded", "failed"}, number)
	if phase, ok := source["phase"].(string); ok {
		switch phase {
		case "Pending", "Running", "Succeeded", "Failed", "Unknown", "Bound", "Available", "Released", "Active", "Terminating":
			status["phase"] = phase
		}
	}
	if conditions, ok := source["conditions"].([]any); ok {
		safe := []any{}
		for _, c := range conditions {
			c := obj(c)
			kind, _ := c["type"].(string)
			state, _ := c["status"].(string)
			switch kind {
			case "Ready", "Available", "Progressing", "Complete", "Failed", "Healthy", "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable", "PodScheduled", "Initialized", "ContainersReady", "PodReadyToStartContainers":
				if state == "True" || state == "False" || state == "Unknown" {
					safe = append(safe, object{"type": kind, "status": state})
				}
			}
		}
		status["conditions"] = safe
	}
	if len(status) > 0 {
		out["status"] = status
	}
	spec := object{}
	copyFields(spec, obj(v["spec"]), []string{"replicas"}, number)
	if len(spec) > 0 {
		out["spec"] = spec
	}
	usage := object{}
	for _, key := range []string{"cpu", "memory"} {
		if value, ok := obj(v["usage"])[key].(string); ok && quantity.MatchString(value) {
			usage[key] = value
		}
	}
	if len(usage) > 0 {
		out["usage"] = usage
	}
	if containers, ok := v["containers"].([]any); ok {
		safe := []any{}
		for _, c := range containers {
			safe = append(safe, ProjectK8s(obj(c)))
		}
		out["containers"] = safe
	}
	copyFields(out, v, []string{"name"}, id)
	// API discovery contains only reviewed names, kinds and read verbs.
	if list, ok := v["resources"].([]any); ok {
		safe := []any{}
		for _, r := range list {
			r := obj(r)
			name, _ := r["name"].(string)
			version, _ := v["groupVersion"].(string)
			allowed := false
			for _, n := range resources[version] {
				if n == name {
					allowed = true
				}
			}
			if allowed {
				item := object{"name": name, "verbs": []string{"get", "list"}}
				copyFields(item, r, []string{"kind", "singularName"}, id)
				if n, ok := r["namespaced"].(bool); ok {
					item["namespaced"] = n
				}
				safe = append(safe, item)
			}
		}
		out["resources"] = safe
		out["groupVersion"] = id(v["groupVersion"])
	}
	if versions, ok := v["versions"].([]any); ok {
		safe := []any{}
		for _, version := range versions {
			if version == "v1" {
				safe = append(safe, "v1")
				continue
			}
			item := obj(version)
			gv, _ := item["groupVersion"].(string)
			if _, ok := resources[gv]; ok {
				safe = append(safe, object{"groupVersion": gv, "version": id(item["version"])})
			}
		}
		out["versions"] = safe
		if len(safe) > 0 && obj(safe[0]) != nil {
			out["preferredVersion"] = safe[0]
		}
		copyFields(out, v, []string{"name"}, id)
	}

	if groups, ok := v["groups"].([]any); ok {
		safe := []any{}
		for _, g := range groups {
			g := obj(g)
			name, _ := g["name"].(string)
			vs := []any{}
			for _, version := range toArray(g["versions"]) {
				version := obj(version)
				gv, _ := version["groupVersion"].(string)
				if _, ok := resources[gv]; ok {
					vs = append(vs, object{"groupVersion": gv, "version": id(version["version"])})
				}
			}
			if len(vs) > 0 {
				safe = append(safe, object{"name": id(name), "versions": vs, "preferredVersion": vs[0]})
			}
		}
		out["groups"] = safe
	}
	return out
}
func toArray(v any) []any { a, _ := v.([]any); return a }

type Server struct {
	Kubernetes, Grafana       string
	KubeClient, GrafanaClient *http.Client
	KubeToken, GrafanaToken   func() (string, error)
}

func (s *Server) fetch(ctx context.Context, client *http.Client, base, path string, token func() (string, error)) (any, error) {
	if client == nil || token == nil {
		return nil, errors.New("reader unavailable")
	}
	value, err := token()
	if err != nil || value == "" {
		return nil, errors.New("reader authorization unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+value)
	req.Header.Set("Accept", "application/json")
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }
	res, err := clone.Do(req)
	if err != nil {
		return nil, errors.New("reader upstream unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("reader upstream rejected request")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
	if err != nil || len(raw) > 8<<20 {
		return nil, errors.New("reader response too large")
	}
	var result any
	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("reader response invalid")
	}
	return result, nil
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		reply(w, 200, object{"ready": true})
		return
	}
	if r.URL.Path == "/mcp" && r.Method == "POST" {
		s.mcp(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/k8s")
	if path == r.URL.Path || !K8sAllowed(r.Method, path) {
		reply(w, 403, object{"kind": "Status", "status": "Failure", "reason": "Forbidden", "message": "projected reader denied request"})
		return
	}
	for key := range r.URL.Query() {
		if key != "limit" && key != "labelSelector" && key != "fieldSelector" {
			reply(w, 403, object{"kind": "Status", "status": "Failure", "reason": "Forbidden"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	target := path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	data, err := s.fetch(ctx, s.KubeClient, s.Kubernetes, target, s.KubeToken)
	if err != nil {
		reply(w, 502, object{"kind": "Status", "status": "Failure", "reason": "ServiceUnavailable", "message": "projected reader unavailable"})
		return
	}
	if path == "/version" {
		out := object{}
		copyFields(out, obj(data), []string{"major", "minor", "gitVersion"}, id)
		reply(w, 200, out)
		return
	}
	if r.Method == "HEAD" {
		w.WriteHeader(200)
		return
	}
	reply(w, 200, ProjectK8s(obj(data)))
}

func sample(v any) any {
	a := toArray(v)
	if len(a) != 2 || number(a[0]) == nil {
		return nil
	}
	text, ok := a[1].(string)
	if !ok {
		return nil
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return []any{a[0], n}
}
func ProjectMetrics(value any) (any, error) {
	data := obj(obj(value)["data"])
	kind, _ := data["resultType"].(string)
	if obj(value)["status"] != "success" {
		return nil, errors.New("metrics unavailable")
	}
	out := object{"resultType": kind}
	switch kind {
	case "scalar":
		if v := sample(data["result"]); v != nil {
			out["result"] = v
		} else {
			return nil, errors.New("invalid scalar")
		}
	case "vector", "matrix":
		results := []any{}
		for _, row := range toArray(data["result"]) {
			row := obj(row)
			item := object{}
			if kind == "vector" {
				v := sample(row["value"])
				if v == nil {
					return nil, errors.New("invalid sample")
				}
				item["value"] = v
			} else {
				values := []any{}
				for _, point := range toArray(row["values"]) {
					v := sample(point)
					if v == nil {
						return nil, errors.New("invalid sample")
					}
					values = append(values, v)
				}
				item["values"] = values
			}
			results = append(results, item)
		}
		out["result"] = results
	default:
		return nil, errors.New("textual telemetry denied")
	}
	return out, nil
}
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err != nil || json.Unmarshal(raw, &request) != nil {
		reply(w, 400, object{"error": "invalid request"})
		return
	}
	if len(request.ID) == 0 {
		if request.Method == "notifications/initialized" {
			w.WriteHeader(202)
			return
		}
		reply(w, 403, object{"error": "notification denied"})
		return
	}
	response := object{"jsonrpc": "2.0", "id": request.ID}
	fail := func() {
		response["error"] = object{"code": -32602, "message": "projected reader denied or unavailable"}
		reply(w, 200, response)
	}
	switch request.Method {
	case "initialize":
		response["result"] = object{"protocolVersion": "2024-11-05", "capabilities": object{"tools": object{}}, "serverInfo": object{"name": "projected-cluster-reader", "version": "1"}}
	case "ping":
		response["result"] = object{}
	case "tools/list":
		response["result"] = object{"tools": []any{object{"name": "list_datasources", "description": "Datasource identifiers and types. Names and configuration are withheld.", "inputSchema": object{"type": "object", "properties": object{}, "additionalProperties": false}}, object{"name": "query_prometheus", "description": "Numeric Prometheus samples. Labels, exemplars, log text and errors are withheld.", "inputSchema": object{"type": "object", "required": []string{"datasourceUid", "expr"}, "properties": object{"datasourceUid": object{"type": "string"}, "expr": object{"type": "string"}, "endTime": object{"type": "string"}, "queryType": object{"type": "string", "enum": []string{"instant"}}}, "additionalProperties": false}}}}
	case "tools/call":
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		var projected any
		switch request.Params.Name {
		case "list_datasources":
			if len(request.Params.Arguments) != 0 {
				fail()
				return
			}
			data, e := s.fetch(ctx, s.GrafanaClient, s.Grafana, "/api/datasources", s.GrafanaToken)
			if e != nil {
				fail()
				return
			}
			safe := []any{}
			for _, ds := range toArray(data) {
				ds := obj(ds)
				uid := id(ds["uid"])
				kind, _ := ds["type"].(string)
				switch kind {
				case "prometheus", "loki", "tempo":
					if uid != nil {
						safe = append(safe, object{"uid": uid, "type": kind})
					}
				}
			}
			projected = safe
		case "query_prometheus":
			args := request.Params.Arguments
			for key := range args {
				if key != "datasourceUid" && key != "expr" && key != "endTime" && key != "queryType" {
					fail()
					return
				}
			}
			uid, ok := args["datasourceUid"].(string)
			expr, valid := args["expr"].(string)
			if !ok || !valid || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(uid) || len(expr) == 0 || len(expr) > 8192 {
				fail()
				return
			}
			if kind, ok := args["queryType"]; ok && kind != "instant" {
				fail()
				return
			}
			query := url.Values{"query": {expr}}
			if t, ok := args["endTime"].(string); ok && t != "now" {
				query.Set("time", t)
			}
			data, e := s.fetch(ctx, s.GrafanaClient, s.Grafana, "/api/datasources/proxy/uid/"+uid+"/api/v1/query?"+query.Encode(), s.GrafanaToken)
			if e != nil {
				fail()
				return
			}
			projected, e = ProjectMetrics(data)
			if e != nil {
				fail()
				return
			}
		default:
			fail()
			return
		}
		encoded, _ := json.Marshal(projected)
		response["result"] = object{"content": []any{object{"type": "text", "text": string(encoded)}}, "isError": false}
	default:
		fail()
		return
	}
	reply(w, 200, response)
}
func Run(ctx context.Context) error {
	ca, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		return errors.New("reader Kubernetes trust unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("reader Kubernetes trust invalid")
	}
	kube := &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	token := func(path string) func() (string, error) {
		return func() (string, error) { raw, e := os.ReadFile(path); return strings.TrimSpace(string(raw)), e }
	}
	s := &Server{Kubernetes: os.Getenv("READER_KUBERNETES_URL"), Grafana: os.Getenv("READER_GRAFANA_URL"), KubeClient: kube, GrafanaClient: &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{Proxy: nil}}, KubeToken: token("/var/run/secrets/kubernetes.io/serviceaccount/token"), GrafanaToken: token(os.Getenv("READER_GRAFANA_TOKEN_FILE"))}
	if s.Kubernetes == "" {
		s.Kubernetes = "https://kubernetes.default.svc.cluster.local"
	}
	if s.Grafana == "" {
		return errors.New("reader Grafana endpoint required")
	}
	for _, endpoint := range []string{s.Kubernetes, s.Grafana} {
		u, e := url.Parse(endpoint)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("invalid trusted reader endpoint")
		}
	}
	server := &http.Server{Addr: ":8080", Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() { <-ctx.Done(); _ = server.Close() }()
	return server.ListenAndServe()
}
