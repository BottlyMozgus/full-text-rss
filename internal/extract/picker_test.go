package extract

// picker_test.go: tests for the rule picker document sanitizer and the
// selector validation/match counting helpers.

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func mustPicker(t *testing.T, body, pageURL string, selfHosts ...string) string {
	t.Helper()
	out, err := PickerDocument([]byte(body), pageURL, selfHosts...)
	if err != nil {
		t.Fatalf("PickerDocument: %v", err)
	}
	return string(out)
}

func TestValidateSelector(t *testing.T) {
	for _, sel := range []string{"div.foo", "article > p:nth-of-type(2)", "#main [data-id]"} {
		if err := ValidateSelector(sel); err != nil {
			t.Errorf("ValidateSelector(%q) = %v, want nil", sel, err)
		}
	}
	for _, sel := range []string{"", "   ", "div[", "p::=="} {
		if err := ValidateSelector(sel); err == nil {
			t.Errorf("ValidateSelector(%q) = nil, want error", sel)
		}
	}
}

func TestCountMatches(t *testing.T) {
	body := []byte(`<!doctype html><html><body>
		<div class="ad">a</div><div class="ad">b</div>
		<article><p>text</p></article>
	</body></html>`)
	counts, errs := CountMatches(body, "https://example.test/x", "div.ad", "article", "div[", "")
	if len(counts) != 4 || len(errs) != 4 {
		t.Fatalf("got %d counts, %d errors, want 4 each", len(counts), len(errs))
	}
	if counts[0] != 2 || counts[1] != 1 {
		t.Errorf("counts = %v, want [2 1 ...]", counts)
	}
	if errs[0] != nil || errs[1] != nil {
		t.Errorf("errs[0:2] = %v %v, want nil nil", errs[0], errs[1])
	}
	if errs[2] == nil || errs[3] == nil {
		t.Errorf("invalid selectors must report errors, got %v", errs)
	}
	if counts[2] != 0 || counts[3] != 0 {
		t.Errorf("invalid selectors must count 0, got %v", counts)
	}
}

const pickerPageHTML = `<!doctype html><html><head>
<title>Pick</title>
<meta charset="iso-8859-1">
<meta http-equiv="refresh" content="5">
<base href="https://cdn.example.test/pages/">
<link rel="stylesheet" href="/style/site.css">
<link rel="preload" href="/fonts/a.woff2" as="font">
<link rel="preload" href="/app.mjs" as="script">
</head><body>
<nav class="menu" onclick="alert(1)"><a href="javascript:void(0)" ping="https://tracker.example.test">menu</a></nav>
<div class="wrap">
	<img srcdoc="&lt;script&gt;alert(1)&lt;/script&gt;" src="/img/one.jpg" srcset="/img/one.jpg 1x, /img/one@2x.jpg 2x" onerror="alert(2)">
	<div class="pickme second">second</div>
	<div class="ad">ad</div>
	<div class="pickme third">third</div>
</div>
<form action="/self/post" formaction="/self/other"><input name="q"></form>
<a href="https://ad.example.test/out">out</a>
<a href="https://127.0.0.1:9977/admin">self link</a>
<img src="https://127.0.0.1:9977/static/img.png" alt="self img">
<object data="movie.swf"><param name="x" value="y"></object>
<script src="/js/app.js"></script>
<script>alert(3)</script>
<noscript><p>no js</p></noscript>
<iframe src="https://frames.example.test/"></iframe>
</body></html>`

func TestPickerDocumentStripsExecutionAndFetch(t *testing.T) {
	out := mustPicker(t, pickerPageHTML, "https://page.example.test/story/1", "127.0.0.1:9977")

	for _, want := range []string{
		"<script", "noscript", "<iframe", "<object", "<embed", "<applet",
		"onclick", "onerror", "srcdoc", "ping=", "formaction",
		"javascript:", "http-equiv", "<base href=\"https://cdn.example.test/pages/\">",
		`rel="preload"`, "movie.swf",
	} {
		if strings.Contains(out, want) {
			t.Errorf("output still contains %q", want)
		}
	}

	// Kept: stylesheets, classes, content, safe links and images.
	for _, want := range []string{
		`rel="stylesheet"`, `class="pickme second"`, `class="pickme third"`,
		`class="ad"`, "/img/one.jpg", "https://ad.example.test/out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lost %q", want)
		}
	}

	// charset meta is the first head child and base follows it. The page's
	// own <base> pointed at the CDN; the picker keeps the effective base
	// (the document's own base resolved against pageURL, same rule as
	// extraction) so relative URLs resolve like they did on the site.
	if !strings.HasPrefix(out[strings.Index(out, "<head>"):], `<head><meta charset="utf-8"/><base href="https://cdn.example.test/pages/"/>`) {
		i := strings.Index(out, "<head>")
		t.Errorf("head does not start with charset+base, got %.120s", out[i:])
	}
}

func TestPickerDocumentSelfHostFiltering(t *testing.T) {
	out := mustPicker(t, pickerPageHTML, "https://page.example.test/story/1", "127.0.0.1:9977")
	if strings.Contains(out, "127.0.0.1:9977") {
		t.Error("self-host URL survived in output")
	}
	// srcset: the self-host candidate list must be gone but the element kept.
	// The fixture srcset has no self-host candidates, so add one via a
	// dedicated document.
	body := `<!doctype html><html><body><img id="mix" src="https://ok.example.test/a.jpg"
		srcset="https://127.0.0.1:9977/a.jpg 1x, https://ok.example.test/a@2x.jpg 2x"></body></html>`
	out = mustPicker(t, body, "https://page.example.test/x", "127.0.0.1:9977")
	if strings.Contains(out, "127.0.0.1:9977") {
		t.Error("self-host srcset candidate survived")
	}
	if !strings.Contains(out, "https://ok.example.test/a@2x.jpg 2x") {
		t.Error("non-self srcset candidate lost")
	}
	if !strings.Contains(out, `src="https://ok.example.test/a.jpg"`) {
		t.Error("non-self src lost")
	}
}

func TestPickerDocumentKeepsSiblingPositions(t *testing.T) {
	out := mustPicker(t, pickerPageHTML, "https://page.example.test/story/1")
	doc, err := newDoc([]byte(out), "https://page.example.test/story/1")
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	third := doc.Find(`.wrap div:nth-of-type(3)`)
	if third.Length() != 1 || third.Text() != "third" {
		t.Errorf("nth-of-type(3) = %d %q, want the .third div", third.Length(), third.Text())
	}
	// Removing script/iframe subtrees must not unwrap them into siblings;
	// .wrap keeps exactly its own img and three divs (the object element
	// and its subtree live outside .wrap and are removed whole).
	doc.Find(".wrap > *").Each(func(i int, s *goquery.Selection) {
		t.Logf("wrap child %d: <%s class=%q>", i, s.Nodes[0].Data, attrVal(s.Nodes[0], "class"))
	})
	if n := doc.Find(".wrap > *").Length(); n != 4 {
		t.Errorf(".wrap has %d children, want 4", n)
	}
	if n := doc.Find("*").FilterFunction(func(_ int, s *goquery.Selection) bool {
		return s.Nodes[0].Data == "param"
	}).Length(); n != 0 {
		t.Errorf("object subtree not removed whole: %d param children remain", n)
	}
}

func TestPickerDocumentMetaBasePlacement(t *testing.T) {
	// Page without its own base: base href must be the pageURL.
	out := mustPicker(t, `<!doctype html><html><head><title>t</title></head><body><p>x</p></body></html>`,
		"https://page.example.test/a/b")
	if !strings.Contains(out, `<base href="https://page.example.test/a/b"/>`) {
		t.Errorf("base href wrong: %s", out)
	}
	// Page with its own base: the effective base is resolved against pageURL.
	out = mustPicker(t, `<!doctype html><html><head><base href="/assets/"></head><body><p>x</p></body></html>`,
		"https://page.example.test/a/b")
	if !strings.Contains(out, `<base href="https://page.example.test/assets/"/>`) {
		t.Errorf("own base not resolved: %s", out)
	}
}

func TestPickerDocumentRejectsNonHTML(t *testing.T) {
	if _, err := PickerDocument([]byte("just some text, not html at all"), "https://x.example.test/"); err == nil {
		t.Error("PickerDocument(garbage) = nil error, want error")
	}
}
