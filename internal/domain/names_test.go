package domain

import (
	"strings"
	"testing"
)

func TestValidateNames(t *testing.T) {
	tests := []struct {
		name  string
		fn    func(string) error
		valid []string
		bad   []string
	}{
		{"namespace", ValidateNamespace,
			[]string{"payments", "a", "kube-system", "ns1"},
			[]string{"", "Payments", "-ns", "ns-", "a.b", "a_b", strings.Repeat("a", 64), "$(id)"}},
		{"object", ValidateObjectName,
			[]string{"payment-service", "payment-service-7c9d8f6b5-x2x9q", "a.b.c", strings.Repeat("a", 253)},
			[]string{"", "Payment", "a..b", ".a", "a.", "a b", "--help", "a/b", strings.Repeat("a", 254)}},
	}
	for _, tt := range tests {
		for _, s := range tt.valid {
			if err := tt.fn(s); err != nil {
				t.Errorf("%s %q: unexpected error %v", tt.name, s, err)
			}
		}
		for _, s := range tt.bad {
			if err := tt.fn(s); err == nil {
				t.Errorf("%s %q: want error", tt.name, s)
			}
		}
	}
}
