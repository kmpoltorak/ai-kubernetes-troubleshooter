package api

import (
	"bytes"
	"cmp"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"

	"github.com/kmpoltorak/ai-kubernetes-troubleshooter/docs"
)

// repoURL is where links that leave docs/ (README, manifests, tests) point.
const repoURL = "https://github.com/kmpoltorak/ai-kubernetes-troubleshooter/blob/main/"

var (
	// The default renderer drops raw HTML from the Markdown instead of passing it through.
	markdown = goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	hrefAttr = regexp.MustCompile(`href="([^"]*)"`)
	docsPage = template.Must(template.New("doc").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · Kubernetes Troubleshooter</title><link rel="stylesheet" href="/docs/style.css"></head>
<body><nav><a href="/">Kubernetes Troubleshooter</a> · <a href="/docs/">Documentation</a></nav>
<main>{{.Body}}</main></body></html>
`))
)

const docsCSS = `:root{color-scheme:light dark;--fg:#18181b;--muted:#52525b;--bg:#fafafa;--line:#e4e4e7;--code:#f4f4f5;--link:#4338ca}
@media (prefers-color-scheme:dark){:root{--fg:#f4f4f5;--muted:#a1a1aa;--bg:#09090b;--line:#27272a;--code:#18181b;--link:#a5b4fc}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.6 system-ui,sans-serif}
nav{padding:12px 16px;border-bottom:1px solid var(--line);font-size:14px;color:var(--muted)}
main{max-width:860px;margin:0 auto;padding:16px;overflow-wrap:anywhere}
a{color:var(--link)}code,pre{background:var(--code);border-radius:4px;font-size:14px}code{padding:1px 4px}
pre{padding:12px;overflow-x:auto}pre code{padding:0}
table{border-collapse:collapse;display:block;overflow-x:auto;font-size:14px}th,td{border:1px solid var(--line);padding:6px 8px;text-align:left;vertical-align:top}`

type docPage struct {
	Title string
	Body  template.HTML
}

// renderDocs turns each embedded guide into a page named after its file
// ("clusters.md" → "clusters") and builds an index of their titles.
func renderDocs(files fs.FS) (map[string][]byte, []byte) {
	names, _ := fs.Glob(files, "*.md") // the pattern is valid
	pages := make(map[string][]byte, len(names))
	type entry struct{ name, title string }
	var index []entry
	for _, file := range names {
		src, _ := fs.ReadFile(files, file) // listed by Glob above
		name := strings.TrimSuffix(file, ".md")
		title, _, _ := strings.Cut(strings.TrimPrefix(string(src), "# "), "\n")
		var body bytes.Buffer
		_ = markdown.Convert(src, &body) // writing to a buffer cannot fail
		html := hrefAttr.ReplaceAllStringFunc(body.String(), func(attr string) string {
			return `href="` + docLink(hrefAttr.FindStringSubmatch(attr)[1]) + `"`
		})
		pages[name] = page(title, html)
		index = append(index, entry{name, title})
	}
	slices.SortFunc(index, func(a, b entry) int { return cmp.Compare(a.title, b.title) })
	var list strings.Builder
	list.WriteString("<h1>Documentation</h1><ul>")
	for _, e := range index {
		list.WriteString(`<li><a href="/docs/` + e.name + `">` + template.HTMLEscapeString(e.title) + "</a></li>")
	}
	list.WriteString("</ul>")
	return pages, page("Documentation", list.String())
}

func page(title, body string) []byte {
	var out bytes.Buffer
	_ = docsPage.Execute(&out, docPage{Title: title, Body: template.HTML(body)}) // #nosec G203 -- goldmark output of embedded docs, raw HTML dropped
	return out.Bytes()
}

// docLink maps a link written for GitHub to the served pages: another guide
// becomes /docs/<name>, anything outside docs/ opens in the repository.
func docLink(href string) string {
	if href == "" || strings.HasPrefix(href, "#") || strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:") {
		return href
	}
	target, anchor, hasAnchor := strings.Cut(href, "#")
	if hasAnchor {
		anchor = "#" + anchor
	}
	if !strings.Contains(target, "/") && strings.HasSuffix(target, ".md") {
		return "/docs/" + strings.TrimSuffix(target, ".md") + anchor
	}
	return repoURL + strings.TrimPrefix(path.Join("docs", target), "/") + anchor
}

// registerDocs serves the guides under /docs/; like the UI, it is behind
// authentication when that is on.
func registerDocs(mux *http.ServeMux) {
	pages, index := renderDocs(docs.Files)
	write := func(w http.ResponseWriter, contentType string, body []byte) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}
	mux.HandleFunc("GET /docs/{$}", func(w http.ResponseWriter, _ *http.Request) { write(w, "text/html; charset=utf-8", index) })
	mux.HandleFunc("GET /docs/style.css", func(w http.ResponseWriter, _ *http.Request) { write(w, "text/css; charset=utf-8", []byte(docsCSS)) })
	mux.HandleFunc("GET /docs/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.PathValue("name")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		write(w, "text/html; charset=utf-8", body)
	})
}
