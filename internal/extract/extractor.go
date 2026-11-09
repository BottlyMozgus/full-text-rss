package extract

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"full-text-rss/internal/model"
)

// Result is the outcome of a successful extraction.
type Result struct {
	// ContentHTML is the cleaned minimal HTML fragment.
	ContentHTML string
	// Title is the article title when one could be determined.
	Title string
	// Extracted is always true on success (kept for symmetry with model.Item).
	Extracted bool
}

// RuleSource supplies per-site extraction rules; the store implements it.
// A nil source means "no rules".
type RuleSource interface {
	RuleForHost(ctx context.Context, host string) (*model.SiteRule, error)
}

// Extractor fetches article pages and extracts cleaned full-text HTML.
type Extractor struct {
	fetcher *Fetcher
	rules   RuleSource // may be nil
}

// NewExtractor builds an Extractor. rules may be nil to disable rule-based
// extraction entirely.
func NewExtractor(f *Fetcher, rules RuleSource) *Extractor {
	return &Extractor{fetcher: f, rules: rules}
}

// Extract fetches articleURL, extracts the main content (site rule selector
// when one matches, go-readability otherwise, with rule-to-readability
// fallback), rewrites relative URLs to absolute, and aggressively cleans the
// HTML. Returns ErrRateLimited passthrough, or ErrExtract on any other
// failure.
func (e *Extractor) Extract(ctx context.Context, articleURL string) (Result, error) {
	// 1. Rule lookup and URL rewrite.
	host := hostOf(articleURL)
	var rule *model.SiteRule
	if e.rules != nil && host != "" {
		r, err := e.rules.RuleForHost(ctx, host)
		if err != nil {
			return Result{}, fmt.Errorf("%w: rule lookup: %v", ErrExtract, err)
		}
		rule = r
	}
	fetchURL := articleURL
	if rule != nil && rule.URLRewriteFrom != "" {
		fetchURL = strings.ReplaceAll(articleURL, rule.URLRewriteFrom, rule.URLRewriteTo)
	}

	// 2. Fetch HTML.
	body, err := e.fetcher.Fetch(ctx, fetchURL)
	if err != nil {
		return Result{}, err // ErrRateLimited and ErrExtract passthrough
	}

	// 3-4. Extract: rule selector first, readability as fallback. Content
	// types are checked inside extractWithRules / extractWithReadability.
	var contentHTML, title string
	if rule != nil && rule.ContentSelector != "" {
		contentHTML, title, err = e.extractWithRules(body, fetchURL, rule)
		if err != nil || strings.TrimSpace(stripTags(contentHTML)) == "" {
			// Rule matched nothing usable; fall back to readability.
			contentHTML, title, err = e.extractWithReadability(body, fetchURL)
			if err != nil {
				return Result{}, fmt.Errorf("%w: readability fallback: %v", ErrExtract, err)
			}
		}
	} else {
		contentHTML, title, err = e.extractWithReadability(body, fetchURL)
		if err != nil {
			return Result{}, fmt.Errorf("%w: readability: %v", ErrExtract, err)
		}
	}
	if strings.TrimSpace(stripTags(contentHTML)) == "" {
		return Result{}, fmt.Errorf("%w: %s: empty content", ErrExtract, articleURL)
	}

	cleaned := Clean(contentHTML)
	if strings.TrimSpace(stripTags(cleaned)) == "" {
		return Result{}, fmt.Errorf("%w: %s: content empty after cleanup", ErrExtract, articleURL)
	}
	return Result{ContentHTML: cleaned, Title: strings.TrimSpace(title), Extracted: true}, nil
}

// extractWithRules selects nodes matching rule.ContentSelector from the
// document, removes rule.StripSelectors, rewrites relative URLs, and renders
// the remaining fragment.
func (e *Extractor) extractWithRules(body []byte, pageURL string, rule *model.SiteRule) (string, string, error) {
	doc, err := newDoc(body, pageURL)
	if err != nil {
		return "", "", fmt.Errorf("parse html: %w", err)
	}

	// Strip selectors first so they cannot pollute the content selection
	// (e.g. share bars nested inside the article element).
	for _, sel := range rule.StripSelectors {
		if sel == "" {
			continue
		}
		m, err := cascadiaSafe(sel)
		if err != nil || m == nil {
			continue
		}
		doc.FindMatcher(m).Remove()
	}

	m, err := cascadiaSafe(rule.ContentSelector)
	if err != nil || m == nil {
		return "", "", fmt.Errorf("bad content selector %q: %v", rule.ContentSelector, err)
	}
	sel := doc.FindMatcher(m)
	if sel.Length() == 0 {
		return "", "", fmt.Errorf("selector %q matched nothing", rule.ContentSelector)
	}

	// Concatenate all matches (a "main column" selector usually matches once,
	// but several sibling articles are also valid).
	var sb strings.Builder
	sel.Each(func(i int, s *goquery.Selection) {
		h, err := s.Html()
		if err == nil {
			sb.WriteString(h)
			sb.WriteString("\n")
		}
	})
	out := sb.String()
	if strings.TrimSpace(out) == "" {
		return "", "", fmt.Errorf("selector %q matched elements without content", rule.ContentSelector)
	}

	title := strings.TrimSpace(doc.Find("title").First().Text())
	out = absolutizeFragment(out, doc.Url.String())
	return out, title, nil
}

// extractWithReadability runs go-readability over the page and returns its
// content HTML and title.
func (e *Extractor) extractWithReadability(body []byte, pageURL string) (string, string, error) {
	// Cheap pre-check: readability is expensive and pointless on fragments
	// that do not even look like HTML.
	if !looksLikeHTML(body) {
		return "", "", errors.New("content is not HTML")
	}
	u, err := url.Parse(pageURL)
	if err != nil {
		return "", "", fmt.Errorf("parse url: %w", err)
	}
	art, err := readability.FromReader(strings.NewReader(string(body)), u)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(art.Content) == "" {
		return "", "", errors.New("readability produced empty content")
	}
	return art.Content, art.Title, nil
}

// newDoc parses body into a goquery document. A <base href> in the document
// overrides pageURL for relative-URL resolution.
func newDoc(body []byte, pageURL string) (*goquery.Document, error) {
	parsed, err := parseHTML(body)
	if err != nil {
		return nil, err
	}
	base := pageURL
	doc := goquery.NewDocumentFromNode(parsed)
	if b, exists := doc.Find("base[href]").First().Attr("href"); exists && strings.TrimSpace(b) != "" {
		if abs := resolveURL(pageURL, b); abs != "" {
			base = abs
		}
	}
	doc.Url, _ = url.Parse(base)
	return doc, nil
}

// absolutizeFragment parses an HTML fragment, rewrites a[href] and img[src]
// (plus srcset best-effort) to absolute URLs against pageURL, and returns
// the fragment serialized.
func absolutizeFragment(fragment, pageURL string) string {
	frag, err := parseFragment(fragment)
	if err != nil {
		return fragment
	}
	doc := goquery.NewDocumentFromNode(frag)
	absolutizeSelection(doc.Selection, pageURL)
	var sb strings.Builder
	for n := frag.FirstChild; n != nil; n = n.NextSibling {
		_ = html.Render(&sb, n)
	}
	return sb.String()
}

// absolutizeSelection rewrites link/image URLs inside s to absolute form.
func absolutizeSelection(s *goquery.Selection, pageURL string) {
	s.Find("*").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		n := sel.Nodes[0]
		if n.Type != html.ElementNode {
			return true
		}
		switch n.DataAtom {
		case atom.A:
			absolutizeAttr(sel, "href", pageURL)
		case atom.Img:
			absolutizeAttr(sel, "src", pageURL)
			absolutizeAttr(sel, "srcset", pageURL)
		}
		return true
	})
}

// absolutizeAttr rewrites one URL-valued attribute on sel to absolute form
// against pageURL, dropping it when it cannot be resolved.
func absolutizeAttr(sel *goquery.Selection, attr, pageURL string) {
	val, ok := sel.Attr(attr)
	if !ok || strings.TrimSpace(val) == "" {
		return
	}
	switch attr {
	case "srcset":
		sel.SetAttr("srcset", absolutizeSrcset(val, pageURL))
	default:
		if abs := resolveURL(pageURL, val); abs != "" {
			sel.SetAttr(attr, abs)
		} else {
			sel.RemoveAttr(attr)
		}
	}
}

// absolutizeSrcset rewrites every URL in a srcset candidate list.
func absolutizeSrcset(val, pageURL string) string {
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fields := strings.Fields(p)
		if len(fields) > 0 {
			if abs := resolveURL(pageURL, fields[0]); abs != "" {
				fields[0] = abs
			}
		}
		out = append(out, strings.Join(fields, " "))
	}
	return strings.Join(out, ", ")
}

// resolveURL resolves ref against base, returning "" when either side is
// unusable (javascript:, data: with hostile intent stays untouched by
// cleanup later; here we only fail on parse errors).
func resolveURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}

// hostOf returns the lowercase host of rawURL, "" when it has none.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
