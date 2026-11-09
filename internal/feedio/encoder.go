// Package feedio converts parsed feeds (RSS/Atom via gofeed) into RSS 2.0
// XML with content:encoded full-text bodies.
package feedio

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

// ItemContent pairs a gofeed.Item with its final content HTML (already
// extracted and cleaned, or the original summary as fallback).
type ItemContent struct {
	Item     *gofeed.Item
	HTML     string // cleaned full content, may equal the summary fallback
	Fallback bool   // true when HTML is the original summary (extraction failed)
}

// Output is the full feed ready to serialize.
type Output struct {
	Title        string
	Link         string
	Description  string
	Items        []ItemContent
	LastModified time.Time
}

const (
	// contentNamespace is the RSS content module namespace that carries the
	// cleaned full-article HTML.
	contentNamespace = "http://purl.org/rss/1.0/modules/content/"

	// fallbackComment marks items whose content:encoded is the original
	// summary rather than extracted content.
	fallbackComment = "<!--summary-fallback-->"

	// generatorTag identifies the producing software in the channel.
	generatorTag = "rss-scraper"
)

// ContentType is the HTTP Content-Type for RSS 2.0 output.
func ContentType() string {
	return "application/rss+xml; charset=utf-8"
}

// rssXML is the marshal tree for an RSS 2.0 document. Namespace-prefixed
// names are emitted literally by encoding/xml; xmlns:content declares the
// prefix used by item content:encoded elements.
type rssXML struct {
	XMLName   xml.Name   `xml:"rss"`
	Version   string     `xml:"version,attr"`
	ContentNS string     `xml:"xmlns:content,attr"`
	Channel   rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Generator     string    `xml:"generator"`
	Items         []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string     `xml:"title"`
	Link        string     `xml:"link"`
	GUID        *guidXML   `xml:"guid,omitempty"`
	PubDate     string     `xml:"pubDate,omitempty"`
	Author      string     `xml:"author,omitempty"`
	Categories  []string   `xml:"category,omitempty"`
	Description string     `xml:"description"`
	Encoded     encodedXML `xml:"content:encoded"`
}

// encodedXML emits its Raw content verbatim inside the element, letting us
// write a CDATA section that encoding/xml's tag options cannot express for
// namespaced elements.
type encodedXML struct {
	Raw string `xml:",innerxml"`
}

// cdataEncoded wraps html in a CDATA section so readers receive raw HTML
// instead of entity-escaped text. A literal "]]>" inside the HTML is split
// across sections to keep the XML well-formed.
func cdataEncoded(html string) encodedXML {
	safe := strings.ReplaceAll(html, "]]>", "]]]]><![CDATA[>")
	return encodedXML{Raw: "<![CDATA[" + safe + "]]>"}
}

type guidXML struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// ToRSS renders o as RSS 2.0 XML bytes with per-item content:encoded.
func ToRSS(o Output) ([]byte, error) {
	items := make([]rssItem, 0, len(o.Items))
	for _, ic := range o.Items {
		it := ic.Item
		enc := ic.HTML
		if ic.Fallback {
			// Extraction failed: serve the original summary, flagged for
			// debugging, so the item still carries something readable.
			enc = fallbackComment + firstNonEmpty(it.Description, ic.HTML)
		}
		items = append(items, rssItem{
			Title:       it.Title,
			Link:        it.Link,
			GUID:        guidXMLFor(it),
			PubDate:     rssTime(timeOf(it.PublishedParsed, it.UpdatedParsed)),
			Author:      authorName(it.Author),
			Categories:  categories(it),
			Description: it.Description,
			Encoded:     cdataEncoded(enc),
		})
	}
	var lastMod *time.Time
	if !o.LastModified.IsZero() {
		lastMod = &o.LastModified
	}
	doc := rssXML{
		Version:   "2.0",
		ContentNS: contentNamespace,
		Channel: rssChannel{
			Title:         o.Title,
			Link:          o.Link,
			Description:   o.Description,
			LastBuildDate: rssTime(lastMod),
			Generator:     generatorTag,
			Items:         items,
		},
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode rss: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

// rssTime formats t as RFC 1123Z (RSS pubDate), empty when t is nil.
func rssTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC1123Z)
}

// timeOf returns a when non-nil, else b (both may be nil).
func timeOf(a, b *time.Time) *time.Time {
	if a != nil {
		return a
	}
	return b
}

// guid returns the item's identifier, falling back to its link.
func guid(it *gofeed.Item) string {
	if it.GUID != "" {
		return it.GUID
	}
	return it.Link
}

// guidXMLFor builds the <guid> element: isPermaLink="false" for opaque ids,
// "true" when the value is itself a URL. nil when there is no id at all.
func guidXMLFor(it *gofeed.Item) *guidXML {
	v := guid(it)
	if v == "" {
		return nil
	}
	perm := "false"
	if looksLikeURL(v) {
		perm = "true"
	}
	return &guidXML{IsPermaLink: perm, Value: v}
}

// looksLikeURL reports whether v parses as an absolute http(s) URL.
func looksLikeURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// categories returns the item's non-empty categories as plain strings.
func categories(it *gofeed.Item) []string {
	var out []string
	for _, c := range it.Categories {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// authorName extracts the author name from a gofeed Person.
func authorName(p *gofeed.Person) string {
	if p == nil {
		return ""
	}
	return p.Name
}

// firstNonEmpty returns s if non-empty, else alt.
func firstNonEmpty(s, alt string) string {
	if s != "" {
		return s
	}
	return alt
}
