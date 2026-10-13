package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkloadAndFluxProjectionWithholdsArbitraryContent(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "HelmRelease", "Kustomization", "Event", "CiliumEndpoint"} {
		t.Run(kind, func(t *testing.T) {
			var v object
			_ = json.Unmarshal([]byte(fmt.Sprintf(`{"apiVersion":"v1","kind":%q,"metadata":{"name":"fixture","namespace":"test","annotations":{"note":"INLINE_PASSWORD"},"labels":{"token":"INLINE_PASSWORD"},"managedFields":[{"password":"INLINE_PASSWORD"}]},"spec":{"replicas":2,"containers":[{"env":[{"name":"PASSWORD","value":"INLINE_PASSWORD"}],"command":["INLINE_PASSWORD"],"image":"INLINE_PASSWORD"}],"values":{"password":"INLINE_PASSWORD"}},"status":{"readyReplicas":1,"message":"INLINE_PASSWORD","conditions":[{"type":"Ready","status":"True","message":"INLINE_PASSWORD"},{"type":"INLINE_PASSWORD","status":"True"}]},"message":"INLINE_PASSWORD"}`, kind)), &v)
			raw, _ := json.Marshal(ProjectK8s(v))
			if strings.Contains(string(raw), "INLINE_PASSWORD") {
				t.Fatal("untrusted text escaped projection")
			}
			if !strings.Contains(string(raw), `"readyReplicas":1`) {
				t.Fatal("lost readiness")
			}
		})
	}
}
func TestTelemetryWithholdsLabelsTextErrorsAndExemplars(t *testing.T) {
	var value any
	_ = json.Unmarshal([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"password":"INLINE_PASSWORD"},"value":[123,"4.5"],"exemplars":"INLINE_PASSWORD"}]},"warnings":["INLINE_PASSWORD"]}`), &value)
	safe, err := ProjectMetrics(value)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(safe)
	if strings.Contains(string(raw), "INLINE_PASSWORD") || strings.Contains(string(raw), "metric") {
		t.Fatal("telemetry text exposed")
	}
	for _, raw := range []string{`{"status":"success","data":{"resultType":"string","result":[1,"INLINE_PASSWORD"]}}`, `{"status":"error","error":"INLINE_PASSWORD"}`, `{"status":"success","data":{"resultType":"scalar","result":[1,"NaN"]}}`} {
		var value any
		_ = json.Unmarshal([]byte(raw), &value)
		if _, err := ProjectMetrics(value); err == nil {
			t.Fatal("unsupported telemetry accepted")
		}
	}
}
func TestDeniedRequestsNeverReachUpstreamAndRecoveryWithholdsHeaders(t *testing.T) {
	calls := 0
	available := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !available {
			w.Header().Set("X-Password", "INLINE_PASSWORD")
			w.WriteHeader(503)
			fmt.Fprint(w, "INLINE_PASSWORD")
			return
		}
		fmt.Fprint(w, `{"kind":"PodList","apiVersion":"v1","items":[{"metadata":{"name":"worker","annotations":{"password":"INLINE_PASSWORD"}},"spec":{"containers":[{"env":[{"value":"INLINE_PASSWORD"}]}]}}]}`)
	}))
	defer upstream.Close()
	s := &Server{Kubernetes: upstream.URL, KubeClient: upstream.Client(), KubeToken: func() (string, error) { return "fixture", nil }}
	for _, path := range []string{"/k8s/api/v1/secrets", "/k8s/api/v1/pods/x/log", "/k8s/api/v1/pods?watch=true", "/k8s/api/v1/pods?allowWatchBookmarks=true", "/k8s/apis/unknown/v1/things"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 403 {
			t.Fatal("denial failed", path)
		}
	}
	if calls != 0 {
		t.Fatal("denial reached upstream")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/k8s/api/v1/pods", nil))
	if w.Code != 502 || strings.Contains(w.Body.String(), "INLINE_PASSWORD") || w.Header().Get("X-Password") != "" {
		t.Fatal("upstream error leaked")
	}
	available = true
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/k8s/api/v1/pods", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "INLINE_PASSWORD") {
		t.Fatal("recovery failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/k8s/api/v1/pods", nil).WithContext(ctx)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 502 {
		t.Fatal("interruption did not fail closed")
	}
}
func TestMCPOnlyReviewedNumericTools(t *testing.T) {
	s := &Server{}
	for _, method := range []string{`"tools/call","params":{"name":"query_loki_logs","arguments":{}}`, `"resources/read","params":{}`, `"tools/call","params":{"name":"update_dashboard","arguments":{}}`} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":`+method+`}`)))
		if !strings.Contains(w.Body.String(), `"error"`) {
			t.Fatal("unsupported tool accepted")
		}
	}
}
