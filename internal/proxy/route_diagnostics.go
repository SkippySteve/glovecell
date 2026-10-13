package proxy

import (
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
)

var diagnosticSegment = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z._-]{0,63}|v[0-9]{1,2}|aiserver\.v[0-9]\.[A-Za-z]+)$`)

// Explicit operator diagnostics record route shapes, never headers, bodies,
// query contents, IDs or opaque path parameters. Disabled in normal operation.
func traceRequestRoute(host string, req *http.Request) {
	if os.Getenv("APC_TRACE_PROXY_ROUTES") != "1" {
		return
	}
	segments := strings.Split(req.URL.Path, "/")
	for i, segment := range segments {
		if segment != "" && !diagnosticSegment.MatchString(segment) {
			segments[i] = ":parameter"
		}
	}
	slog.Info("operator proxy route diagnostic", "host", host, "method", req.Method, "route", strings.Join(segments, "/"), "query_present", req.URL.RawQuery != "")
}
