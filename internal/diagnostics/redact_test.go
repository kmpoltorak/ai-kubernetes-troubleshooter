package diagnostics

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	tests := []struct{ in, secret string }{
		{`connecting to postgres://payments:s3cr3tPass@db.payments.svc:5432/payments`, "s3cr3tPass"},
		{`DB_PASSWORD=hunter2hunter2 starting`, "hunter2hunter2"},
		{`{"api_key": "abcd1234efgh5678"}`, "abcd1234efgh5678"},
		{`token: ghp_abcdefghijklmnopqrstuvwxyz0123`, "ghp_abcdefghijklmnopqrstuvwxyz0123"},
		{`Authorization: Bearer abc.def-ghi_jkl12345`, "abc.def-ghi_jkl12345"},
		{`jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NSJ9.c2lnbmF0dXJlLXZhbHVl`, "eyJzdWIiOiIxMjM0NSJ9"},
		{`aws key AKIAIOSFODNN7EXAMPLE used`, "AKIAIOSFODNN7EXAMPLE"},
		{`openai sk-proj-abcdefghijklmnopqrstu`, "sk-proj-abcdefghijklmnopqrstu"},
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----", "MIIEpAIBAAKCAQEA"},
		{`client_secret="xyz987654321"`, "xyz987654321"},
	}
	for _, tt := range tests {
		got := Redact(tt.in)
		if strings.Contains(got, tt.secret) || !strings.Contains(got, "REDACTED") {
			t.Errorf("Redact(%q) = %q, secret not masked", tt.in, got)
		}
	}
	plain := `dial tcp 10.0.0.5:5432: connect: connection refused`
	if got := Redact(plain); got != plain {
		t.Errorf("plain text changed: %q", got)
	}
}
