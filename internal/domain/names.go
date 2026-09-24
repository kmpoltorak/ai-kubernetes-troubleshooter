package domain

import (
	"errors"
	"regexp"
	"strings"
)

var (
	dnsLabel     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// ValidateNamespace accepts a Kubernetes namespace name (RFC 1123 label).
func ValidateNamespace(s string) error {
	switch {
	case s == "":
		return errors.New("is required")
	case len(s) > 63:
		return errors.New("must be at most 63 characters")
	case !dnsLabel.MatchString(s):
		return errors.New("must be a lowercase RFC 1123 label")
	}
	return nil
}

// ValidateObjectName accepts a Kubernetes object name (RFC 1123 subdomain).
// It is the gate in front of every Kubernetes API call built from user input.
func ValidateObjectName(s string) error {
	switch {
	case s == "":
		return errors.New("is required")
	case len(s) > 253:
		return errors.New("must be at most 253 characters")
	case !dnsSubdomain.MatchString(s) || strings.Contains(s, ".."):
		return errors.New("must be a lowercase RFC 1123 subdomain")
	}
	return nil
}
