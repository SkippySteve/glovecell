package proxy

import "strings"

// VendorTelemetryHosts is deliberately finite. Do not allow shared telemetry
// domains or wildcard tenants: these are the collection hosts used by the
// provider clients. Cursor/Grok requests on their model hosts still require
// their existing live broker grant and bearer-token checks.
func VendorTelemetryHosts() []string {
	return []string{
		"ab.chatgpt.com",
		"api.anthropic.com",
		"http-intake.logs.us5.datadoghq.com",
		"browser-intake-us5-datadoghq.com",
	}
}

func vendorTelemetryEndpoint(host, path string) bool {
	var prefix string
	switch host {
	case "ab.chatgpt.com":
		// OpenAI's dedicated analytics host; accept its changing collection
		// paths without allowing another chatgpt.com host or inference route.
		return true
	case "api.anthropic.com":
		prefix = "/api/event_logging"
	case "http-intake.logs.us5.datadoghq.com", "browser-intake-us5-datadoghq.com":
		prefix = "/api/v2/logs"
	default:
		return false
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
