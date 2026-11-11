package extract

// picker.go: helpers backing the admin rule picker. PickerDocument turns a
// fetched page into one that is safe to display in a same-origin iframe for
// element picking; ValidateSelector and CountMatches report selector
// validity and match counts for the live preview.

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ValidateSelector reports whether sel is a CSS selector the extractor can
// compile. An empty selector is an error.
func ValidateSelector(sel string) error {
	if strings.TrimSpace(sel) == "" {
		return fmt.Errorf("empty selector")
	}
	_, err := cascadia.Compile(sel)
	return err
}

// CountMatches parses body once and counts, for each selector, how many
// elements it matches. Results are index-aligned with selectors: an
// uncompilable selector yields a count of 0 and a non-nil error at its
// index. Empty selectors count as invalid.
func CountMatches(body []byte, pageURL string, selectors ...string) ([]int, []error) {
	counts := make([]int, len(selectors))
	errs := make([]error, len(selectors))
	doc, err := newDoc(body, pageURL)
	if err != nil {
		for i := range selectors {
			errs[i] = fmt.Errorf("parse html: %w", err)
		}
		return counts, errs
	}
	for i, sel := range selectors {
		m, err := cascadiaSafe(sel)
		if err != nil {
			errs[i] = err
			continue
		}
		if m == nil {
			errs[i] = fmt.Errorf("empty selector")
			continue
		}
		counts[i] = doc.FindMatcher(m).Length()
	}
	return counts, errs
}

// pickerRemovedTags are elements dropped whole-subtree from picker
// documents. Removal (not unwrapping) keeps the sibling positions of every
// surviving element stable, so selectors generated from the picker match
// the page as it was served.
var pickerRemovedTags = map[string]bool{
	"script":   true,
	"noscript": true,
	"iframe":   true,
	"frame":    true,
	"frameset": true,
	"object":   true,
	"embed":    true,
	"applet":   true,
	"portal":   true,
}

// pickerRemovedLinkRels are rel values whose link elements are dropped from
// picker documents. Stylesheet links are kept so the page renders as
// itself; these preload-family links either trigger fetches or resolve to
// scripts and manifests.
var pickerRemovedLinkRels = map[string]bool{
	"preload":       true,
	"prefetch":      true,
	"modulepreload": true,
	"dns-prefetch":  true,
	"preconnect":    true,
	"manifest":      true,
	"import":        true,
}

// pickerURLAttrs are URL-valued attributes checked for hostile schemes and
// self-referencing URLs.
var pickerURLAttrs = map[string]bool{
	"href":       true,
	"src":        true,
	"xlink:href": true,
	"poster":     true,
	"data":       true,
	"action":     true,
	"srcset":     true,
}

// PickerDocument parses body into a document stripped of anything that
// could execute or fetch scripts (scripts, frames, plugin content, event
// handler attributes, javascript: URLs, meta refresh) and safe to serve on
// the admin origin inside a sandboxed iframe. selfHosts lists hosts
// (hostname or hostname:port, case-insensitive) whose URLs are removed from
// href/src/srcset/poster/action attributes so a hostile page cannot make
// the admin's browser send authenticated requests to rss-scraper itself.
// Stylesheets, classes, ids and inline style attributes are kept so the
// page renders as itself and pickers can generate selectors. Returns an
// error for non-HTML input.
func PickerDocument(body []byte, pageURL string, selfHosts ...string) ([]byte, error) {
	if !looksLikeHTML(body) {
		return nil, fmt.Errorf("content is not HTML")
	}
	root, err := parseHTML(body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	doc := goquery.NewDocumentFromNode(root)

	// Effective base before removal, like newDoc: the document's own
	// <base href> resolved against pageURL wins.
	base := pageURL
	if b, exists := doc.Find("base[href]").First().Attr("href"); exists && strings.TrimSpace(b) != "" {
		if abs := resolveURL(pageURL, b); abs != "" {
			base = abs
		}
	}

	removePickerElements(doc)
	scrubPickerAttrs(doc, base, selfHosts)
	insertPickerMetaBase(doc, base)

	var out bytes.Buffer
	if err := html.Render(&out, root); err != nil {
		return nil, fmt.Errorf("render html: %w", err)
	}
	return out.Bytes(), nil
}

// removePickerElements drops script-bearing and plugin elements, preload
// links, meta refresh family, and base elements whole-subtree.
func removePickerElements(doc *goquery.Document) {
	doc.Find("*").Each(func(_ int, s *goquery.Selection) {
		n := s.Nodes[0]
		if n.Type != html.ElementNode {
			return
		}
		drop := pickerRemovedTags[n.Data] ||
			(n.DataAtom == atom.Link && linkRelDropped(s)) ||
			(n.DataAtom == atom.Meta && metaIsHTTP(s)) ||
			n.Data == "base"
		if drop {
			s.Remove()
		}
	})
}

// linkRelDropped reports whether a link element's rel contains a dropped
// value. Rel is a space-separated token list.
func linkRelDropped(s *goquery.Selection) bool {
	rel, ok := s.Attr("rel")
	if !ok {
		return false
	}
	for _, tok := range strings.Fields(strings.ToLower(rel)) {
		if pickerRemovedLinkRels[tok] {
			return true
		}
	}
	return false
}

// metaIsHTTP reports whether a meta element sets http-equiv (refresh,
// CSP overrides, and friends).
func metaIsHTTP(s *goquery.Selection) bool {
	v, ok := s.Attr("http-equiv")
	return ok && strings.TrimSpace(v) != ""
}

// scrubPickerAttrs walks all elements and removes attributes that could
// execute code, navigate, or fetch scripts; hostile schemes; and URLs
// pointing at selfHosts.
func scrubPickerAttrs(doc *goquery.Document, base string, selfHosts []string) {
	doc.Find("*").Each(func(_ int, s *goquery.Selection) {
		n := s.Nodes[0]
		if n.Type != html.ElementNode {
			return
		}
		var drop []string
		for _, a := range n.Attr {
			name := strings.ToLower(a.Key)
			if strings.HasPrefix(name, "on") || name == "srcdoc" || name == "ping" || name == "formaction" {
				drop = append(drop, a.Key)
				continue
			}
			if name == "action" && n.DataAtom == atom.Form {
				drop = append(drop, a.Key)
				continue
			}
			if !pickerURLAttrs[name] {
				continue
			}
			if pickerSchemeHostile(a.Val) {
				drop = append(drop, a.Key)
				continue
			}
			if name == "srcset" {
				if cleaned := filterSrcsetHosts(a.Val, base, selfHosts); cleaned != a.Val {
					if cleaned == "" {
						drop = append(drop, a.Key)
					} else {
						s.SetAttr(name, cleaned)
					}
				}
				continue
			}
			if hitsSelfHost(resolveURL(base, a.Val), selfHosts) {
				drop = append(drop, a.Key)
			}
		}
		for _, name := range drop {
			s.RemoveAttr(name)
		}
	})
}

// pickerSchemeHostile reports whether val is a URL scheme the picker must
// not keep (script execution, or HTML served as data).
func pickerSchemeHostile(val string) bool {
	v := strings.ToLower(strings.TrimFunc(val, func(r rune) bool {
		return r <= ' ' || r == 0x7f
	}))
	return strings.HasPrefix(v, "javascript:") ||
		strings.HasPrefix(v, "vbscript:") ||
		strings.HasPrefix(v, "data:text/html")
}

// filterSrcsetHosts drops srcset candidates whose first URL resolves to a
// selfHost, keeping the rest.
func filterSrcsetHosts(val, base string, selfHosts []string) string {
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fields := strings.Fields(p)
		if len(fields) > 0 && hitsSelfHost(resolveURL(base, fields[0]), selfHosts) {
			continue
		}
		out = append(out, strings.Join(fields, " "))
	}
	return strings.Join(out, ", ")
}

// hitsSelfHost reports whether raw (an absolute URL) points at one of
// selfHosts. Hosts compare by lowercase hostname and port separately, so
// implicit default ports do not defeat the check.
func hitsSelfHost(raw string, selfHosts []string) bool {
	if raw == "" || len(selfHosts) == 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	for _, sh := range selfHosts {
		sh = strings.ToLower(strings.TrimSpace(sh))
		if sh == "" {
			continue
		}
		shHost, shPort := splitHostPort(sh)
		if shHost != host {
			continue
		}
		if shPort == "" || port == "" || shPort == port {
			return true
		}
	}
	return false
}

// splitHostPort splits "host" or "host:port" (no bracket forms needed;
// picker self hosts come from r.Host).
func splitHostPort(s string) (string, string) {
	if i := strings.LastIndexByte(s, ':'); i >= 0 && !strings.Contains(s[i:], "]") {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// insertPickerMetaBase makes meta charset the first head child and inserts
// the effective base right after it.
func insertPickerMetaBase(doc *goquery.Document, base string) {
	head := doc.Find("head").First()
	if head.Length() == 0 {
		return
	}
	// Drop any charset meta we inherited; ours goes first.
	head.Children().Each(func(_ int, s *goquery.Selection) {
		if s.Nodes[0].DataAtom == atom.Meta {
			if _, ok := s.Attr("charset"); ok {
				s.Remove()
			}
		}
	})
	metaNode := &html.Node{
		Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta,
		Attr: []html.Attribute{{Key: "charset", Val: "utf-8"}},
	}
	baseNode := &html.Node{
		Type: html.ElementNode, Data: "base", DataAtom: atom.Base,
		Attr: []html.Attribute{{Key: "href", Val: base}},
	}
	headNode := head.Nodes[0]
prepend:
	for _, n := range []*html.Node{metaNode, baseNode} {
		// The parser may have moved stray content into head before any
		// explicit head child; prepend regardless of existing children.
		if c := headNode.FirstChild; c != nil {
			headNode.InsertBefore(n, c)
		} else {
			headNode.AppendChild(n)
		}
		_ = n
		break prepend
	}
	// baseNode must land right after metaNode, not at the same position.
	metaNode.Parent.InsertBefore(baseNode, metaNode.NextSibling)
}
