package extract

import (
	"bytes"
	"strings"

	"github.com/go-shiori/dom"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
) // parseHTML parses a full HTML document with charset detection (handles
// non-UTF8 pages).
func parseHTML(body []byte) (*html.Node, error) {
	return dom.Parse(bytes.NewReader(body))
}

// parseFragment parses an HTML fragment into a detached <div> root.
func parseFragment(fragment string) (*html.Node, error) {
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		ctx.AppendChild(n)
	}
	return ctx, nil
}

// looksLikeHTML reports whether body starts looking like an HTML document
// or fragment. Cheap heuristic used before readability does heavy work.
func looksLikeHTML(body []byte) bool {
	s := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 1024)])))
	if s == "" {
		return false
	}
	return strings.HasPrefix(s, "<!doctype html") ||
		strings.HasPrefix(s, "<html") ||
		strings.Contains(s, "<body") ||
		strings.Contains(s, "<head") ||
		strings.Contains(s, "<p") ||
		strings.Contains(s, "<div") ||
		strings.Contains(s, "<article") ||
		strings.Contains(s, "<br")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// allowedElements is the strict allowlist from CLAUDE.md decisions: text,
// links, images, headings, lists, quotes, code, and tables. Nothing else
// survives. No classes, ids, styles, scripts, or iframes.
var allowedElements = []string{
	"p", "br",
	"a",
	"img",
	"h1", "h2", "h3", "h4", "h5", "h6",
	"ul", "ol", "li",
	"blockquote",
	"pre", "code",
	"strong", "em",
	"table", "thead", "tbody", "tr", "td", "th",
}

// cleaner holds the bluemonday policy built once per process. A fresh
// policy (not UGCPolicy) because UGC permits classes and many attributes;
// our contract requires none of those.
var cleaner = newCleanPolicy()

// newCleanPolicy builds the strict allowlist policy:
//   - elements: allowedElements only
//   - attributes: a[href], img[src alt] — nothing global, no class/id/style
//   - URLs: absolute http(s)/mailto permitted, relative resolved and kept
//     (relative URLs are absolutized before cleaning; bleach keeps sane ones)
//   - script/style content dropped entirely via SkipElementsContent
func newCleanPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	// Only the agreed elements; strip disallowed elements' inner content for
	// containers that make no sense textually, keep text for layout wrappers
	// (div/span etc. are stripped but their text remains readable).
	p.AllowElements(allowedElements...)

	// Never render script/style contents even as text.
	p.SkipElementsContent("script", "style", "noscript", "template", "iframe", "object", "embed")

	// a[href]: http, https, mailto only.
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	p.RequireParseableURLs(true)
	// No rel/target additions; output stays minimal.

	// img[src alt].
	p.AllowAttrs("src").OnElements("img")
	p.AllowAttrs("alt").Matching(bluemonday.SpaceSeparatedTokens).OnElements("img")

	return p
}

// Clean aggressively sanitizes an HTML fragment down to the minimal
// allowlist: text, links, images, headings, lists, quotes, code, tables.
// Classes, ids, inline styles, scripts, iframes, and every other attribute
// are removed.
func Clean(html string) string {
	return cleaner.Sanitize(html)
}

// stripTags returns visible text of an HTML fragment with tags removed;
// used for emptiness checks only.
func stripTags(s string) string {
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
