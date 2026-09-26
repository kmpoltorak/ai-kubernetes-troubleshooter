package diagnostics

import "regexp"

// Redaction runs on every free-form string that comes from the cluster
// (logs, event and container messages) before it is stored or sent to an
// LLM. It is pattern based, so it lowers rather than eliminates risk; the
// primary control is that Secret objects are never read.
var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	// A block without END runs to the end of the text; an END left over
	// means the sample started inside a block, so everything before it goes.
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[REDACTED PRIVATE KEY]"},
	{regexp.MustCompile(`\A[\s\S]*-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED PRIVATE KEY]"},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]+`), "[REDACTED JWT]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [REDACTED]"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "[REDACTED AWS KEY]"},
	{regexp.MustCompile(`\b(sk|pk|rk)-[A-Za-z0-9_-]{16,}\b`), "[REDACTED API KEY]"},
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|xox[abprs]-[A-Za-z0-9-]{10,})\b`), "[REDACTED TOKEN]"},
	// user:password@ in connection strings and URLs.
	{regexp.MustCompile(`(://[^:/\s@]+):[^@/\s]+@`), "$1:[REDACTED]@"},
	// key=value / key: value / "key": "value" for sensitive key names.
	{regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|client[_-]?secret|credentials?)["']?\s*[:=]\s*["']?)[^\s"',;&]+`), "${1}[REDACTED]"},
}

// Redact masks credentials in s.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}
