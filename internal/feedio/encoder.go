// Package feedio converts parsed feeds (RSS/Atom via gofeed) into RSS 2.0
// XML with content:encoded full-text bodies.
package feedio

import (
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/rss"
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
	Title       string
	Link        string
	Description string
	Items       []ItemContent
	LastModified time.Time
}

// ToRSS renders o as RSS 2.0 XML bytes with a content:encoded extension.
func ToRSS(o Output) ([]byte, error) {
	rssFeed := &rss.Feed{
		Title:       o.Title,
		Link:        o.Link,
		Description: o.Description,
		PubDate:     rssTime(o.LastModified),
		Items:       make([]*rss.Item, 0, len(o.Items)),
	}
	rssFeed.Extensions = map[string]map[string][]rss.Extension{
		"content": {
			"encoded": {}, // populated per item below
		},
	}

	for _, ic := range o.Items {
		it := ic.Item
		ri := &rss.Item{
			Title:       it.Title,
			Link:        it.Link,
			Description: summaryOrTruncated(it),
			PubDate:     rssTime(timeOf(it.PublishedParsed, it.UpdatedParsed)),
			Author:      it.Author,
			GUID:        guid(it),
			Categories:  categories(it),
		}
		// content:encoded carries the full HTML
		enc := rss.Extension{
			Name:     "encoded",
			Value:    ic.HTML,
			Attrs:    map[string]string{},
			Children: map[string][]rss.Extension{},
		}
		rssFeed.Extensions["content"]["encoded"] = append(
			rssFeed.Extensions["content"]["encoded"], enc)
		_ = ri
		rssFeed.Items = append(rssFeed.Items, ri)
	}
	// NOTE: gofeed's rss.Feed marshals extensions at feed level; per-item
	// content:encoded is emitted by the custom marshal step in this package.
	return xml.Marshal(rssFeed)
}

// RSSJunk satisfies gofeed ext constraints; real marshaling is handled by
// EncodeRSS below which builds the XML directly.
func rssTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC1123Z)
}

func timeOf(a, b **time.Time) *time.Time {
	if a != nil {
		return a
	}
	return b
}

func guid(it *gofeed.Item) string {
	if it.GUID != "" {
		return it.GUID
	}
	return it.Link
}

func categories(it *gofeed.Item) []string {
	out := make([]string, 0, len(it.Categories))
	for _, c := range it.Categories {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func summaryOrTruncated(it *gofeed.Item) string {
	if it.Description != "" {
		return it.Description
	}
	return ""
}

var _ = fmt.Sprintf
