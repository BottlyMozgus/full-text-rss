package extract

import (
	"context"
	"strings"
	"testing"

	"rss-scraper/internal/model"
)

// stripPageHTML is an article readability handles on its own. The promo
// paragraph and the ad figure sit inside the article, so only a strip
// selector keeps them out of the output.
const stripPageHTML = `<!doctype html>
<html><head><title>Strip Story | example.test</title></head>
<body>
<nav>menu <a href="/nav">nav link</a></nav>
<article class="story">
  <h1>Strip Story</h1>
  <p>Opening paragraph of the story with enough words to count as real article text for the scorer, and then some more.</p>
  <p class="promo">Subscribe to our newsletter for daily promo spam, delivered straight to your inbox every single morning.</p>
  <p>Middle paragraph of the story, again with plenty of words so that readability keeps the whole article body together.</p>
  <figure class="ad"><img src="/img/ad-banner.jpg" alt="ad banner"></figure>
  <p>Closing paragraph of the story, which wraps things up with a final long sentence about nothing in particular.</p>
</article>
<footer>footer junk</footer>
</body></html>`

const stripPageURL = "https://example.test/story/strip"

func TestExtractBodyModesAndStripSelectors(t *testing.T) {
	const (
		promo = "daily promo spam"
		ad    = "ad-banner.jpg"
		body  = "Middle paragraph of the story"
	)
	cases := []struct {
		name     string
		rule     *model.SiteRule
		wantMode string
		want     []string
		wantNot  []string
	}{
		{
			name:     "no rule keeps everything readability finds",
			rule:     nil,
			wantMode: ModeReadability,
			want:     []string{body, promo, ad},
		},
		{
			name:     "rule without selectors behaves like no rule",
			rule:     ruleFor("example.test", "", nil, "/story/", "/amp/"),
			wantMode: ModeReadability,
			want:     []string{body, promo, ad},
		},
		{
			name:     "strip only applies on the readability path",
			rule:     ruleFor("example.test", "", []string{".promo", "figure.ad"}, "", ""),
			wantMode: ModeReadability,
			want:     []string{body, "Opening paragraph", "Closing paragraph"},
			wantNot:  []string{promo, ad},
		},
		{
			name:     "content selector with strip",
			rule:     ruleFor("example.test", "article.story", []string{".promo"}, "", ""),
			wantMode: ModeRule,
			want:     []string{body, "Strip Story", ad},
			wantNot:  []string{promo, "footer junk", "nav link"},
		},
		{
			name:     "dead content selector falls back and still strips",
			rule:     ruleFor("example.test", "div.does-not-exist", []string{".promo", "figure.ad"}, "", ""),
			wantMode: ModeReadabilityFallback,
			want:     []string{body},
			wantNot:  []string{promo, ad},
		},
		{
			name:     "dead content selector without strip falls back",
			rule:     ruleFor("example.test", "div.does-not-exist", nil, "", ""),
			wantMode: ModeReadabilityFallback,
			want:     []string{body, promo},
		},
		{
			name:     "invalid content selector falls back",
			rule:     ruleFor("example.test", "div[[", []string{".promo"}, "", ""),
			wantMode: ModeReadabilityFallback,
			want:     []string{body},
			wantNot:  []string{promo},
		},
		{
			name:     "invalid and blank strip selectors are skipped",
			rule:     ruleFor("example.test", "", []string{"p[[", "", "  ", ".promo"}, "", ""),
			wantMode: ModeReadability,
			want:     []string{body, ad},
			wantNot:  []string{promo},
		},
	}
	e := &Extractor{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := e.ExtractBody([]byte(stripPageHTML), stripPageURL, tc.rule)
			if err != nil {
				t.Fatalf("ExtractBody: %v", err)
			}
			if res.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", res.Mode, tc.wantMode)
			}
			if !res.Extracted {
				t.Error("Extracted = false, want true")
			}
			if !strings.Contains(res.Title, "Strip Story") {
				t.Errorf("Title = %q, want page title", res.Title)
			}
			for _, w := range tc.want {
				if !strings.Contains(res.ContentHTML, w) {
					t.Errorf("content missing %q: %s", w, res.ContentHTML)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(res.ContentHTML, w) {
					t.Errorf("content still has %q: %s", w, res.ContentHTML)
				}
			}
			assertClean(t, res.ContentHTML)
		})
	}
}

func TestExtractBodyErrors(t *testing.T) {
	e := &Extractor{}
	cases := []struct {
		name string
		body string
		rule *model.SiteRule
	}{
		{"non-HTML without rule", "\x00\x01 not html at all", nil},
		{"non-HTML with strip rule", "\x00\x01 not html at all", ruleFor("example.test", "", []string{".x"}, "", "")},
		{"non-HTML with content rule", "\x00\x01 not html at all", ruleFor("example.test", "article", nil, "", "")},
		{"everything stripped", stripPageHTML, ruleFor("example.test", "", []string{"body"}, "", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := e.ExtractBody([]byte(tc.body), stripPageURL, tc.rule)
			if err == nil || !strings.Contains(err.Error(), ErrExtract.Error()) {
				t.Fatalf("err = %v, want ErrExtract (content %q)", err, res.ContentHTML)
			}
		})
	}
}

// Without strip or content selectors the pipeline must produce exactly what
// the raw-body readability path produces.
func TestExtractBodyNoSelectorsMatchesReadabilityPath(t *testing.T) {
	e := &Extractor{}
	for _, page := range []string{stripPageHTML, articleHTML} {
		raw, _, err := e.extractWithReadability([]byte(page), stripPageURL)
		if err != nil {
			t.Fatalf("extractWithReadability: %v", err)
		}
		for _, rule := range []*model.SiteRule{nil, ruleFor("example.test", "", nil, "/a/", "/b/")} {
			res, err := e.ExtractBody([]byte(page), stripPageURL, rule)
			if err != nil {
				t.Fatalf("ExtractBody: %v", err)
			}
			if res.ContentHTML != Clean(raw) {
				t.Errorf("ExtractBody output differs from the readability path:\n got %s\nwant %s", res.ContentHTML, Clean(raw))
			}
		}
	}
}

// Extract (fetch plus rule lookup) ends in the same pipeline as ExtractBody.
func TestExtractAppliesStripOnlyRule(t *testing.T) {
	srv := servePage(t, stripPageHTML, "")
	rules := mapRules{
		hostOf(srv.URL): ruleFor(hostOf(srv.URL), "", []string{".promo", "figure.ad"}, "", ""),
	}
	e := NewExtractor(NewFetcher(testConfig(0, 0)), rules)
	res, err := e.Extract(context.Background(), srv.URL+"/story/strip")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if res.Mode != ModeReadability {
		t.Errorf("Mode = %q, want %q", res.Mode, ModeReadability)
	}
	if !strings.Contains(res.ContentHTML, "Middle paragraph of the story") {
		t.Errorf("content lost the article body: %s", res.ContentHTML)
	}
	for _, gone := range []string{"daily promo spam", "ad-banner.jpg"} {
		if strings.Contains(res.ContentHTML, gone) {
			t.Errorf("strip-only rule left %q in readability output: %s", gone, res.ContentHTML)
		}
	}

	want, err := e.ExtractBody([]byte(stripPageHTML), srv.URL+"/story/strip", rules[hostOf(srv.URL)])
	if err != nil {
		t.Fatalf("ExtractBody: %v", err)
	}
	if res.ContentHTML != want.ContentHTML {
		t.Errorf("Extract and ExtractBody disagree:\n got %s\nwant %s", res.ContentHTML, want.ContentHTML)
	}
}

func TestExtractReportsModes(t *testing.T) {
	srv := servePage(t, articleHTML, "")
	host := hostOf(srv.URL)
	cases := []struct {
		name string
		rule *model.SiteRule
		want string
	}{
		{"no rule", nil, ModeReadability},
		{"content selector", ruleFor(host, "div.article-body", nil, "", ""), ModeRule},
		{"dead content selector", ruleFor(host, "div.nope", nil, "", ""), ModeReadabilityFallback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := mapRules{}
			if tc.rule != nil {
				rules[host] = tc.rule
			}
			e := NewExtractor(NewFetcher(testConfig(0, 0)), rules)
			res, err := e.Extract(context.Background(), srv.URL+"/story/m")
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if res.Mode != tc.want {
				t.Errorf("Mode = %q, want %q", res.Mode, tc.want)
			}
		})
	}
}
