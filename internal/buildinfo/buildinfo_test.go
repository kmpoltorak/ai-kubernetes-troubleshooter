package buildinfo

import (
	"encoding/json"
	"testing"
)

func TestCurrent(t *testing.T) {
	oldVersion, oldRevision, oldTime := Version, Revision, BuiltAt
	defer func() { Version, Revision, BuiltAt = oldVersion, oldRevision, oldTime }()
	for _, stamp := range []string{"", "invalid-date", "2026-01-01T12:34:56Z"} {
		t.Run(stamp, func(t *testing.T) {
			Version, Revision, BuiltAt = "0.1.0-test", "0123456789abcdef", stamp
			info := Current()
			raw, err := json.Marshal(info)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 3 || fields["version"] != Version || fields["revision"] != Revision {
				t.Fatalf("unexpected metadata: %s", raw)
			}
			if stamp == "2026-01-01T12:34:56Z" {
				if fields["built_at"] != stamp {
					t.Fatal("build time lost")
				}
			} else if fields["built_at"] != nil {
				t.Fatal("unknown build time fabricated")
			}
			BuiltAt = "changed-after-snapshot"
			if info.BuiltAt != nil && *info.BuiltAt != stamp {
				t.Fatal("snapshot retained mutable state")
			}
		})
	}
}
