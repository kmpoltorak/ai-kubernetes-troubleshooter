// Package buildinfo exposes only metadata supplied by the binary's builder.
package buildinfo

import "time"

// Set with -ldflags -X; runtime configuration does not supply build metadata.
var (
	Version  = "dev"
	Revision = "unknown"
	BuiltAt  = ""
)

type Info struct {
	Version  string  `json:"version"`
	Revision string  `json:"revision"`
	BuiltAt  *string `json:"built_at"`
}

func Current() Info {
	info := Info{Version: Version, Revision: Revision}
	at := BuiltAt
	if _, err := time.Parse(time.RFC3339, at); err == nil {
		info.BuiltAt = &at
	}
	return info
}
