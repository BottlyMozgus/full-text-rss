package feedio

import (
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
)

const testPlaceholder = "https://site.example/static/img/fallback.png"

// imageItem returns a bare item with a summary and no image of its own.
func imageItem() *gofeed.Item {
	return &gofeed.Item{
		Title:       "Image test",
		Link:        "https://blog.example/posts/img",
		GUID:        "urn:img",
		Description: "summary",
	}
}

// roundTrip renders o and re-parses it, returning the single item.
func roundTrip(t *testing.T, o Output) (*gofeed.Item, string) {
	t.Helper()
	raw, err := ToRSS(o)
	if err != nil {
		t.Fatalf("ToRSS: %v", err)
	}
	f, err := gofeed.NewParser().ParseString(string(raw))
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, raw)
	}
	if len(f.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(f.Items))
	}
	return f.Items[0], string(raw)
}

func TestImageDefaultWhenNoOwnImage(t *testing.T) {
	it, _ := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d",
		DefaultImage: "https://cdn.example/default.jpg", PlaceholderImage: testPlaceholder,
		Items: []ItemContent{{Item: imageItem(), HTML: "<p>text</p>"}},
	})
	enc, _ := encodedOf(t, it)
	want := `<p><img src="https://cdn.example/default.jpg" alt=""></p><p>text</p>`
	if enc != want {
		t.Errorf("content = %q, want %q", enc, want)
	}
	if len(it.Enclosures) != 1 || it.Enclosures[0].URL != "https://cdn.example/default.jpg" || it.Enclosures[0].Type != "image/jpeg" {
		t.Errorf("enclosure = %+v, want default jpeg", it.Enclosures)
	}
}

func TestImageEnclosureBeatsChannelAndDefault(t *testing.T) {
	item := imageItem()
	item.Enclosures = []*gofeed.Enclosure{
		{URL: "https://audio.example/a.mp3", Type: "audio/mpeg"},
		{URL: "https://img.example/hero.png", Type: "image/png"},
	}
	it, _ := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d",
		FeedImage: "https://blog.example/logo.jpg", DefaultImage: "https://cdn.example/default.jpg",
		PlaceholderImage: testPlaceholder,
		Items:            []ItemContent{{Item: item, HTML: "<p>text</p>"}},
	})
	enc, _ := encodedOf(t, it)
	if !strings.Contains(enc, `src="https://img.example/hero.png"`) {
		t.Errorf("content = %q, want enclosure image", enc)
	}
	if len(it.Enclosures) != 1 || it.Enclosures[0].URL != "https://img.example/hero.png" || it.Enclosures[0].Type != "image/png" {
		t.Errorf("enclosure = %+v, want hero.png image/png", it.Enclosures)
	}
}

func TestImageChannelImageUsedBeforeDefault(t *testing.T) {
	it, _ := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d",
		FeedImage: "https://blog.example/logo.svg", DefaultImage: "https://cdn.example/default.jpg",
		PlaceholderImage: testPlaceholder,
		Items:            []ItemContent{{Item: imageItem(), HTML: "<p>text</p>"}},
	})
	enc, _ := encodedOf(t, it)
	if !strings.Contains(enc, `src="https://blog.example/logo.svg"`) {
		t.Errorf("content = %q, want channel image", enc)
	}
	if len(it.Enclosures) != 1 || it.Enclosures[0].Type != "image/svg+xml" {
		t.Errorf("enclosure = %+v, want svg type", it.Enclosures)
	}
}

func TestImageMediaThumbnailAndImageField(t *testing.T) {
	thumb := imageItem()
	thumb.Extensions = map[string]map[string][]ext.Extension{
		"media": {"thumbnail": {{Attrs: map[string]string{"url": "https://img.example/thumb.webp"}}}},
	}
	it, _ := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d", PlaceholderImage: testPlaceholder,
		Items: []ItemContent{{Item: thumb, HTML: "<p>text</p>"}},
	})
	if len(it.Enclosures) != 1 || it.Enclosures[0].URL != "https://img.example/thumb.webp" || it.Enclosures[0].Type != "image/webp" {
		t.Errorf("media:thumbnail enclosure = %+v", it.Enclosures)
	}

	withImage := imageItem()
	withImage.Image = &gofeed.Image{URL: "https://img.example/header.gif"}
	it, _ = roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d", PlaceholderImage: testPlaceholder,
		Items: []ItemContent{{Item: withImage, HTML: "<p>text</p>"}},
	})
	if len(it.Enclosures) != 1 || it.Enclosures[0].URL != "https://img.example/header.gif" || it.Enclosures[0].Type != "image/gif" {
		t.Errorf("item image enclosure = %+v", it.Enclosures)
	}
}

func TestImageContentImgUntouched(t *testing.T) {
	const body = `<p>intro</p><img src="https://blog.example/x.png" alt="x"><p>more</p>`
	it, raw := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d",
		FeedImage: "https://blog.example/logo.png", DefaultImage: "https://cdn.example/default.jpg",
		PlaceholderImage: testPlaceholder,
		Items:            []ItemContent{{Item: imageItem(), HTML: body}},
	})
	enc, _ := encodedOf(t, it)
	if enc != body {
		t.Errorf("content = %q, want unchanged %q", enc, body)
	}
	if len(it.Enclosures) != 0 {
		t.Errorf("enclosure = %+v, want none when content has an image", it.Enclosures)
	}
	if strings.Contains(raw, "logo.png") || strings.Contains(raw, "default.jpg") {
		t.Errorf("fallback image leaked into output:\n%s", raw)
	}
}

func TestImagePlaceholderWhenNothingElse(t *testing.T) {
	it, _ := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d", PlaceholderImage: testPlaceholder,
		Items: []ItemContent{{Item: imageItem(), HTML: "<p>text</p>"}},
	})
	enc, _ := encodedOf(t, it)
	if !strings.Contains(enc, `src="`+testPlaceholder+`"`) {
		t.Errorf("content = %q, want placeholder", enc)
	}
	if len(it.Enclosures) != 1 || it.Enclosures[0].URL != testPlaceholder || it.Enclosures[0].Type != "image/png" {
		t.Errorf("enclosure = %+v, want placeholder png", it.Enclosures)
	}
}

func TestImageFallbackSummaryItemGetsImage(t *testing.T) {
	it, _ := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d", PlaceholderImage: testPlaceholder,
		Items: []ItemContent{{Item: imageItem(), HTML: "summary text", Fallback: true}},
	})
	enc, _ := encodedOf(t, it)
	if !strings.HasPrefix(enc, "<!--summary-fallback--><p><img src=") {
		t.Errorf("content = %q, want fallback marker then image", enc)
	}
}

func TestImageSrcIsEscaped(t *testing.T) {
	it, raw := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d",
		DefaultImage: `https://cdn.example/a.jpg?x=1&y="2"`,
		Items:        []ItemContent{{Item: imageItem(), HTML: "<p>t</p>"}},
	})
	enc, _ := encodedOf(t, it)
	if !strings.Contains(enc, `src="https://cdn.example/a.jpg?x=1&amp;y=&#34;2&#34;"`) {
		t.Errorf("content = %q, want escaped src", enc)
	}
	if strings.Contains(raw, `"2"`) {
		t.Errorf("unescaped quote in output")
	}
}

func TestImageNonHTTPConfiguredValueIgnored(t *testing.T) {
	it, _ := roundTrip(t, Output{
		Title: "T", Link: "https://blog.example/", Description: "d",
		DefaultImage: "javascript:alert(1)", PlaceholderImage: testPlaceholder,
		Items: []ItemContent{{Item: imageItem(), HTML: "<p>t</p>"}},
	})
	enc, _ := encodedOf(t, it)
	if strings.Contains(enc, "javascript") || !strings.Contains(enc, testPlaceholder) {
		t.Errorf("content = %q, want placeholder and no javascript URL", enc)
	}
}
