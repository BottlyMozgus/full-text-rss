package extract

import (
	"context"
	"strings"
	"testing"
)

// figureDropFixture mirrors the BBC layout that go-readability loses: the
// text is one component block, the images are sibling component blocks, and
// a related-story promo thumbnail lives under <main>.
func figureDropFixture() string {
	para := strings.Repeat("This sentence carries enough words to score as article text. ", 6)
	return `<!doctype html><html><head><title>Figure Drop</title></head><body>
<main><article>
<div class="headline"><header><h1>Headline</h1></header></div>
<div class="image"><figure class="fig"><div><picture><img src="/hero.jpg" alt="hero photo"></picture></div></figure></div>
<div class="text"><div class="rt"><p>` + para + `</p><p>` + para + `</p><p>` + para + `</p><p>` + para + `</p></div></div>
<div class="image"><figure class="fig"><div><picture><img src="/mid.jpg" alt="mid photo"></picture></div></figure></div>
<div class="text"><div class="rt"><p>` + para + `</p><p>` + para + `</p><p>` + para + `</p></div></div>
<ul><li><div data-testid="promo"><picture><img src="/promo.jpg" alt="promo thumb"></picture></div></li></ul>
</article></main></body></html>`
}

func TestRestoreFigureImagesKeepsFigureImagesInOrder(t *testing.T) {
	body := []byte(figureDropFixture())
	pageURL := "https://example.test/news/a"

	content, _, err := (&Extractor{}).extractWithReadability(body, pageURL)
	if err != nil {
		t.Fatalf("extractWithReadability: %v", err)
	}
	cleaned := Clean(content)

	for _, want := range []string{
		`src="https://example.test/hero.jpg"`,
		`src="https://example.test/mid.jpg"`,
	} {
		if !strings.Contains(cleaned, want) {
			t.Errorf("cleaned output missing %s:\n%s", want, cleaned)
		}
	}
	if strings.Contains(cleaned, "promo.jpg") {
		t.Errorf("promo thumbnail leaked into article content:\n%s", cleaned)
	}

	hero := strings.Index(cleaned, "hero.jpg")
	mid := strings.Index(cleaned, "mid.jpg")
	if hero < 0 || mid < 0 || hero > mid {
		t.Errorf("image order wrong: hero=%d mid=%d", hero, mid)
	}
	if !strings.Contains(cleaned, `alt="hero photo"`) {
		t.Errorf("alt text not kept on hero image: %s", cleaned)
	}
	assertClean(t, cleaned)
}

func TestRestoreFigureImagesEndToEnd(t *testing.T) {
	srv := servePage(t, figureDropFixture(), "")
	e := NewExtractor(NewFetcher(testConfig(0, 0)), mapRules{})
	res, err := e.Extract(context.Background(), srv.URL+"/news/a")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if n := strings.Count(res.ContentHTML, "<img "); n < 2 {
		t.Errorf("want at least 2 images in output, got %d: %s", n, res.ContentHTML)
	}
	assertAbsolute(t, res.ContentHTML, srv.URL)
}

func TestRestoreFigureImagesNoopWhenPresent(t *testing.T) {
	// Images already in readability output must not be duplicated.
	content := `<p>text one with words</p><p><img src="https://example.test/a.jpg" alt="a"></p>`
	body := []byte(`<html><body><article><p>text one with words</p><figure><img src="/a.jpg" alt="a"></figure></article></body></html>`)
	got := restoreFigureImages(body, "https://example.test/x", content)
	if n := strings.Count(got, "a.jpg"); n != 1 {
		t.Errorf("image duplicated: count=%d in %s", n, got)
	}
}
