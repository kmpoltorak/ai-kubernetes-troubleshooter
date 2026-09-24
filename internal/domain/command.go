package domain

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

var (
	// Plain arguments: names, flags, selectors (app=x), jsonpath-free output
	// formats. Anything a shell would interpret is rejected.
	plainArg = regexp.MustCompile(`^[A-Za-z0-9._:/=,@-]+$`)
	// Placeholders such as <pod> are allowed as whole arguments; they are
	// meant for a human to fill in.
	placeholder = regexp.MustCompile(`^<[a-z0-9_-]+>$`)

	readOnlyVerbs = map[string][]string{
		"get":      nil,
		"describe": nil,
		"logs":     nil,
		"top":      nil,
		"events":   nil,
		"rollout":  {"status", "history"},
	}

	// Flags that change identity, target or bypass typed access.
	deniedFlags = []string{"--raw", "--as", "--as-group", "--as-uid", "--token", "--kubeconfig", "--server", "-s", "--username", "--password"}
)

// ValidateSafeCommand accepts only read-only kubectl invocations that are
// safe to copy into a terminal: an allowlisted verb, no shell syntax, no
// identity or endpoint overrides, and no access to Secrets.
func ValidateSafeCommand(cmd string) error {
	args := strings.Fields(cmd)
	if len(args) < 2 || args[0] != "kubectl" {
		return errors.New("must be a kubectl command")
	}
	for _, a := range args[1:] {
		if !plainArg.MatchString(a) && !placeholder.MatchString(a) {
			return errors.New("contains shell syntax or unsupported characters")
		}
		flag, _, _ := strings.Cut(a, "=")
		if slices.Contains(deniedFlags, flag) {
			return errors.New("uses a forbidden flag")
		}
		if mentionsSecrets(a) {
			return errors.New("must not access Secrets")
		}
	}
	sub, ok := readOnlyVerbs[args[1]]
	if !ok {
		return errors.New("verb must be one of get, describe, logs, top, events, rollout status, rollout history")
	}
	if sub != nil && (len(args) < 3 || !slices.Contains(sub, args[2])) {
		return errors.New("only rollout status and rollout history are allowed")
	}
	return nil
}

// mentionsSecrets matches secret, secrets, secret/x, secrets.v1 and lists
// such as pods,secrets.
func mentionsSecrets(arg string) bool {
	for _, part := range strings.Split(strings.ToLower(arg), ",") {
		part, _, _ = strings.Cut(part, "/")
		part, _, _ = strings.Cut(part, ".")
		if part == "secret" || part == "secrets" {
			return true
		}
	}
	return false
}
