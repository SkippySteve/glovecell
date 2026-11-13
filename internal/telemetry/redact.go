package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

// Redacted replaces a value that must never reach a log.
const Redacted = "[REDACTED]"

// sensitiveKey matches attribute names whose values are credentials, whatever
// their content. Public key fingerprints ("masterKey") and counts
// ("lease_seconds") are not matched.
var sensitiveKey = regexp.MustCompile(`(?i)(password|passwd|passphrase|(^|[_.-])pass$|secret|token|authori[sz]ation|cookie|api[_-]?key|private[_-]?key|access[_-]?key|signing[_-]?key|credential|session[_-]?id|refresh|bearer|dsn|connection[_-]?(string|uri|url))`)

type rule struct {
	pattern *regexp.Regexp
	replace string
}

// valueRules mirror the cluster collector's transform/redact-body, so a line
// is clean before it leaves the process, whatever sink reads stderr.
var valueRules = []rule{
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[private key redacted]"},
	{regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s:@"']+:[^/\s@"']+@`), "${1}" + Redacted + "@"},
	{regexp.MustCompile(`(?i)((?:proxy-)?authorization["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|digest|token|negotiate)\s+)?[^\s"',;]+`), "${1}" + Redacted},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} " + Redacted},
	{regexp.MustCompile(`(?i)\b((?:set-)?cookie["']?\s*[:=]\s*).*`), "${1}" + Redacted},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`), "[REDACTED_JWT]"},
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|xox[abposr]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,}|sk_live_[A-Za-z0-9]{16,}|(?:AKIA|ASIA)[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|tskey-[A-Za-z0-9-]{10,}|hv[sbr]\.[A-Za-z0-9_-]{20,}|syt_[A-Za-z0-9_-]{20,}|mct_[A-Za-z0-9_-]{20,}|AGE-SECRET-KEY-1[A-Z0-9]{50,})\b`), "[REDACTED_TOKEN]"},
	{regexp.MustCompile(`(?i)(--?[a-z0-9-]*(?:password|passwd|secret|token|api-key|apikey|access-key|client-secret)[ =])[^\s"']+`), "${1}" + Redacted},
	{regexp.MustCompile(`(?i)(["']?[a-z0-9_.-]*(?:password|passwd|pwd|passphrase|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|client[_-]?secret|credentials?|session[_-]?id|sessionid)["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&})\]]+)`), "${1}\"" + Redacted + "\""},
	{regexp.MustCompile(`(?i)([?&](?:code|state|sig|signature|token|access_token|refresh_token|key|x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|x-goog-credential)=)[^&\s"'#]+`), "${1}" + Redacted},
	{regexp.MustCompile(`[A-Za-z0-9+/_-]{120,}={0,2}`), "[REDACTED_BLOB]"},
}

// RedactString removes credentials from free text: messages, error strings
// and any string attribute.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	for _, r := range valueRules {
		s = r.pattern.ReplaceAllString(s, r.replace)
	}
	return s
}

// SensitiveKey reports whether an attribute named key carries a credential.
func SensitiveKey(key string) bool { return sensitiveKey.MatchString(key) }

// redactingHandler wraps another handler and scrubs every message and
// attribute before it is written.
type redactingHandler struct{ next slog.Handler }

// NewRedactingHandler wraps next so that credentials are removed from the
// message and all attributes, including groups, errors and LogValuers.
func NewRedactingHandler(next slog.Handler) slog.Handler { return redactingHandler{next} }

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, RedactString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = redactAttr(a)
	}
	return redactingHandler{h.next.WithAttrs(clean)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindBool, slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindDuration, slog.KindTime:
		// Numbers cannot carry a credential; keep counts such as "tokens".
		return slog.Attr{Key: a.Key, Value: v}
	}
	if SensitiveKey(a.Key) && v.Kind() != slog.KindGroup {
		return slog.String(a.Key, Redacted)
	}
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactString(v.String()))
	case slog.KindGroup:
		group := v.Group()
		clean := make([]any, len(group))
		for i, g := range group {
			clean[i] = redactAttr(g)
		}
		return slog.Group(a.Key, clean...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, RedactString(x.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, RedactString(x.String()))
		case []byte:
			return slog.String(a.Key, RedactString(string(x)))
		case nil:
			return slog.Attr{Key: a.Key, Value: v}
		default:
			// Structs and maps are rendered once so nested credentials
			// cannot slip through the JSON encoder.
			return slog.String(a.Key, RedactString(fmt.Sprintf("%+v", x)))
		}
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// NewLogger returns the standard Glovecell process logger: JSON lines on w,
// with every credential redacted.
func NewLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(NewRedactingHandler(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})))
}

// Logger returns the process default logger.
func Logger() *slog.Logger { return slog.Default() }

// ParseLevel maps LOG_LEVEL-style names to a slog level; unknown names are Info.
func ParseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
