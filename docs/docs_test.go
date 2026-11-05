package docs

import (
	"io/fs"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestEmbedsExactlyTheTrackedGuides(t *testing.T) {
	out, err := exec.Command("git", "ls-files", "*.md").Output()
	if err != nil {
		t.Skip("git not available")
	}
	tracked := strings.Fields(string(out))
	embedded, _ := fs.Glob(Files, "*.md")
	slices.Sort(tracked)
	if !slices.Equal(tracked, embedded) {
		t.Fatalf("embed list must match the published guides in Git:\n tracked  %v\n embedded %v", tracked, embedded)
	}
}
