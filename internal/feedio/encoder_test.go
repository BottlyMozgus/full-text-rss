package feedio

import (
	"strings"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
)

// fixtureOutput builds an Output covering the shapes the encoder must
// handle: full metadata (categories, author, opaque GUID), a URL-as-GUID
// item, and a summary-fallback item.
func fixtureOutput() Output {
	pub1 := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	pub2 := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	pub3 := time.Date(2026, 11, 8, 7, 30, 0, 0, time.UTC)

	full := &gofeed.Item{
		Title:           "Full article",
		Link:            "https://blog.example/posts/one",
		GUID:            "urn:post:one",
		Description:     "short summary one",
		Author:          &gofeed.Person{Name: "Ada"},
		Categories:      []string{"go", "rss"},
		PublishedParsed: &pub1,
	}
	urlGuid := &gofeed.Item{
		Title:           "URL guid article",
		Link:            "https://blog.example/posts/two",
		GUID:            "https://blog.example/posts/two",
		Description:     "short summary two",
		PublishedParsed: &pub2,
	}
	fallback := &gofeed.Item{
		Title:           "Fallback article",
		Link:            "https://blog.example/posts/three",
		Description:     "original summary three",
		PublishedParsed: &pub3,
	}
	return Output{
		Title:        "Example Blog (full text)",
		Link:         "https://blog.example/",
		Description:  "Full-text version of Example Blog",
		LastModified: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
		Items: []ItemContent{
			{Item: full, HTML: "<p>full text one</p>"},
			{Item: urlGuid, HTML: "<p>full text two</p>"},
			{Item: fallback, HTML: "original summary three", Fallback: true},
		},
	}
}

// encodedOf extracts the parsed content:encoded value. gofeed v1.3.0 maps
// <content:encoded> to Item.Content when parsing RSS (the content module is
// not treated as a generic extension); older behavior kept it in
// Extensions["content"]["encoded"], which we check as a fallback.
func encodedOf(t *testing.T, it *gofeed.Item) (string, bool) {
	t.Helper()
	if it.Content != "" {
		return it.Content, true
	}
	enc, ok := it.Extensions["content"]["encoded"]
	if !ok || len(enc) == 0 {
		return "", false
	}
	return enc[0].Value, true
}

func TestToRSSRoundTrip(t *testing.T) {
	out := fixtureOutput()
	xml, err := ToRSS(out)
	if err != nil {
		t.Fatalf("ToRSS: %v", err)
	}
	f, err := gofeed.NewParser().ParseString(string(xml))
	if err != nil {
		t.Fatalf("re-parse: %v\nxml:\n%s", err, xml)
	}

	if f.Title != out.Title {
		t.Errorf("channel title = %q, want %q", f.Title, out.Title)
	}
	if f.Link != out.Link {
		t.Errorf("channel link = %q, want %q", f.Link, out.Link)
	}
	if f.Description != out.Description {
		t.Errorf("channel description = %q, want %q", f.Description, out.Description)
	}
	if f.Generator != "full-text-rss" {
		t.Errorf("generator = %q, want full-text-rss", f.Generator)
	}
	if len(f.Items) != 3 {
		t.Fatalf("item count = %d, want 3", len(f.Items))
	}

	// Item 1: full metadata.
	it := f.Items[0]
	if it.Title != "Full article" {
		t.Errorf("item 1 title = %q", it.Title)
	}
	if it.Link != "https://blog.example/posts/one" {
		t.Errorf("item 1 link = %q", it.Link)
	}
	if it.Description != "short summary one" {
		t.Errorf("item 1 description = %q, want original summary", it.Description)
	}
	if it.GUID != "urn:post:one" {
		t.Errorf("item 1 guid = %q, want urn:post:one", it.GUID)
	}
	if it.Author == nil || it.Author.Name != "Ada" {
		t.Errorf("item 1 author = %+v, want Ada", it.Author)
	}
	if len(it.Categories) != 2 || it.Categories[0] != "go" || it.Categories[1] != "rss" {
		t.Errorf("item 1 categories = %v, want [go rss]", it.Categories)
	}
	if it.PublishedParsed == nil || !it.PublishedParsed.Equal(time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("item 1 pubDate parsed = %v, want 2026-11-09T09:00:00Z", it.PublishedParsed)
	}
	enc, ok := encodedOf(t, it)
	if !ok {
		t.Errorf("item 1 content:encoded missing")
	} else if enc != "<p>full text one</p>" {
		t.Errorf("item 1 content:encoded = %q, want cleaned HTML", enc)
	}

	// Item 2: GUID that is itself a URL.
	it = f.Items[1]
	if it.GUID != "https://blog.example/posts/two" {
		t.Errorf("item 2 guid = %q", it.GUID)
	}

	// Item 3: fallback carries the flagged summary in content:encoded.
	it = f.Items[2]
	enc, ok = encodedOf(t, it)
	if !ok {
		t.Errorf("item 3 content:encoded missing")
	} else {
		if !strings.HasPrefix(enc, "<!--summary-fallback-->") {
			t.Errorf("item 3 content:encoded = %q, want fallback comment prefix", enc)
		}
		if !strings.Contains(enc, "original summary three") {
			t.Errorf("item 3 content:encoded = %q, want summary text", enc)
		}
	}
	if it.Description != "original summary three" {
		t.Errorf("item 3 description = %q, want original summary", it.Description)
	}
}

func TestToRSSGuidAttributes(t *testing.T) {
	out := fixtureOutput()
	xml, err := ToRSS(out)
	if err != nil {
		t.Fatalf("ToRSS: %v", err)
	}
	s := string(xml)
	if !strings.Contains(s, `<guid isPermaLink="false">urn:post:one</guid>`) {
		t.Errorf("opaque guid not serialized with isPermaLink=\"false\":\n%s", s)
	}
	if !strings.Contains(s, `<guid isPermaLink="true">https://blog.example/posts/two</guid>`) {
		t.Errorf("URL guid not serialized with isPermaLink=\"true\":\n%s", s)
	}
}

func TestToRSSNamespaces(t *testing.T) {
	xml, err := ToRSS(fixtureOutput())
	if err != nil {
		t.Fatalf("ToRSS: %v", err)
	}
	s := string(xml)
	for _, want := range []string{
		`xmlns:content="http://purl.org/rss/1.0/modules/content/"`,
		`<rss version="2.0"`,
		"<content:encoded>",
		"<lastBuildDate>Mon, 09 Nov 2026 10:00:00 +0000</lastBuildDate>",
		"<generator>full-text-rss</generator>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("raw XML missing %q:\n%s", want, s)
		}
	}
}

func TestContentType(t *testing.T) {
	if got, want := ContentType(), "application/rss+xml; charset=utf-8"; got != want {
		t.Fatalf("ContentType = %q, want %q", got, want)
	}
}

func TestToRSSEmpty(t *testing.T) {
	xml, err := ToRSS(Output{Title: "Empty", Link: "https://e.example/", Description: "no items"})
	if err != nil {
		t.Fatalf("ToRSS empty: %v", err)
	}
	f, err := gofeed.NewParser().ParseString(string(xml))
	if err != nil {
		t.Fatalf("re-parse empty: %v\nxml:\n%s", err, xml)
	}
	if len(f.Items) != 0 {
		t.Fatalf("empty feed items = %d, want 0", len(f.Items))
	}
}

// The Atom fixture proves the Atom->RSS mapping: gofeed parses both formats
// into the same universal model, which ToRSS serializes as RSS 2.0.
const atomFixture = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Source</title>
  <link href="https://atom.example/"/>
  <updated>2026-11-09T08:00:00Z</updated>
  <id>urn:uuid:60a76c80-d399-11d9-b93C-0003939e0af6</id>
  <entry>
    <title>Atom entry one</title>
    <link href="https://atom.example/a"/>
    <id>tag:atom.example,2026:one</id>
    <updated>2026-11-09T08:00:00Z</updated>
    <published>2026-11-08T12:00:00Z</published>
    <author><name>Grace</name></author>
    <category term="atom"/>
    <summary>atom summary one</summary>
    <content type="html">&lt;p&gt;atom raw content&lt;/p&gt;</content>
  </entry>
</feed>`

func TestToRSSFromAtom(t *testing.T) {
	src, err := gofeed.NewParser().ParseString(atomFixture)
	if err != nil {
		t.Fatalf("parse atom: %v", err)
	}
	if len(src.Items) != 1 {
		t.Fatalf("atom items = %d, want 1", len(src.Items))
	}
	updated := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	// Simulate the extraction pipeline: item content is the extracted HTML;
	// the Atom <summary> stays the description.
	items := make([]ItemContent, 0, len(src.Items))
	for _, it := range src.Items {
		items = append(items, ItemContent{Item: it, HTML: "<p>extracted full text</p>"})
	}
	xml, err := ToRSS(Output{
		Title:        src.Title,
		Link:         src.Link,
		Description:  "converted from atom",
		Items:        items,
		LastModified: updated,
	})
	if err != nil {
		t.Fatalf("ToRSS: %v", err)
	}
	f, err := gofeed.NewParser().ParseString(string(xml))
	if err != nil {
		t.Fatalf("re-parse rss: %v\nxml:\n%s", err, xml)
	}

	if f.Title != "Atom Source" {
		t.Errorf("title = %q, want Atom Source (from Atom)", f.Title)
	}
	if len(f.Items) != 1 {
		t.Fatalf("rss items = %d, want 1", len(f.Items))
	}
	it := f.Items[0]
	if it.Title != "Atom entry one" {
		t.Errorf("title = %q", it.Title)
	}
	if it.Link != "https://atom.example/a" {
		t.Errorf("link = %q", it.Link)
	}
	// Atom <id> maps to GUID; opaque tag URI -> isPermaLink="false".
	if it.GUID != "tag:atom.example,2026:one" {
		t.Errorf("guid = %q, want atom id", it.GUID)
	}
	if it.Author == nil || it.Author.Name != "Grace" {
		t.Errorf("author = %+v, want Grace", it.Author)
	}
	if len(it.Categories) != 1 || it.Categories[0] != "atom" {
		t.Errorf("categories = %v, want [atom]", it.Categories)
	}
	if it.Description != "atom summary one" {
		t.Errorf("description = %q, want atom summary", it.Description)
	}
	if it.PublishedParsed == nil || !it.PublishedParsed.Equal(time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("published = %v, want 2026-11-08T12:00:00Z (atom <published>)", it.PublishedParsed)
	}
	enc, ok := encodedOf(t, it)
	if !ok {
		t.Errorf("content:encoded missing")
	} else if enc != "<p>extracted full text</p>" {
		t.Errorf("content:encoded = %q, want extracted HTML", enc)
	}
}

func TestToRSSFallbackWithoutDescription(t *testing.T) {
	it := &gofeed.Item{
		Title: "no summary",
		Link:  "https://x.example/n",
	}
	xml, err := ToRSS(Output{
		Title: "T", Link: "https://x.example/", Description: "d",
		Items: []ItemContent{{Item: it, HTML: "", Fallback: true}},
	})
	if err != nil {
		t.Fatalf("ToRSS: %v", err)
	}
	f, err := gofeed.NewParser().ParseString(string(xml))
	if err != nil {
		t.Fatalf("re-parse: %v\nxml:\n%s", err, xml)
	}
	enc, ok := encodedOf(t, f.Items[0])
	if !ok {
		t.Fatalf("content:encoded missing")
	}
	if !strings.HasPrefix(enc, "<!--summary-fallback-->") {
		t.Errorf("content:encoded = %q, want fallback prefix", enc)
	}
}
