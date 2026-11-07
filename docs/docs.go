// Package docs embeds the published guides, which the API serves under /docs/.
package docs

import "embed"

// Files lists every published guide by name. Local plans and reports live in this
// directory too and must never be embedded, so there is no wildcard;
// TestEmbedsExactlyTheTrackedGuides keeps the list equal to the files in Git.
//
//go:embed analysis-contract.md api.md authentication.md build-metadata.md clusters.md configuration.md
//go:embed data-deletion.md durable-jobs.md evaluation.md kubernetes-testing.md limitations.md operations.md releases.md
var Files embed.FS
