package feedio

import (
	"html"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/mmcdole/gofeed"
)

// imgTagRE matches an <img> start tag in article HTML.
var imgTagRE = regexp.MustCompile(`(?i)<img[\s/>]`)

// imageRef is the image chosen for an item and its MIME type for <enclosure>.
type imageRef struct {
	URL  string
	Type string
}

// pickImage returns the first usable image for an item, in order: the item's
// own feed image (enclosure, image, media:thumbnail/content), the channel
// image, the configured default, and the built-in placeholder. The article
// content's own <img> is checked by the caller before this runs.
func pickImage(it *gofeed.Item, o Output) (imageRef, bool) {
	if u, src := itemImage(it); u != "" {
		return imageRef{URL: u, Type: imageType(u, src)}, true
	}
	for _, u := range []string{o.FeedImage, o.DefaultImage, o.PlaceholderImage} {
		if isHTTP(u) {
			return imageRef{URL: u, Type: imageType(u, "")}, true
		}
	}
	return imageRef{}, false
}

// itemImage returns the item-level image URL and its declared MIME type (may
// be empty). Only http(s) URLs qualify.
func itemImage(it *gofeed.Item) (string, string) {
	for _, e := range it.Enclosures {
		if e != nil && isImageType(e.Type) && isHTTP(e.URL) {
			return e.URL, e.Type
		}
	}
	if it.Image != nil && isHTTP(it.Image.URL) {
		return it.Image.URL, ""
	}
	if m := it.Extensions["media"]; m != nil {
		for _, ext := range m["thumbnail"] {
			if u := ext.Attrs["url"]; isHTTP(u) {
				return u, ""
			}
		}
		for _, ext := range m["content"] {
			u := ext.Attrs["url"]
			if isHTTP(u) && (isImageType(ext.Attrs["type"]) || strings.EqualFold(ext.Attrs["medium"], "image")) {
				return u, ext.Attrs["type"]
			}
		}
	}
	return "", ""
}

// imageType infers the MIME type from the URL extension. Unknown extensions
// use the source-declared image type, else image/jpeg.
func imageType(u, declared string) string {
	p := u
	if pu, err := url.Parse(u); err == nil {
		p = pu.Path
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	if isImageType(declared) {
		return declared
	}
	return "image/jpeg"
}

// imgParagraph builds the leading image paragraph. The src is escaped; no
// other page data is emitted.
func imgParagraph(src string) string {
	return `<p><img src="` + html.EscapeString(src) + `" alt=""></p>`
}

// hasImgTag reports whether article HTML already contains an <img>.
func hasImgTag(s string) bool {
	return imgTagRE.MatchString(s)
}

func isImageType(t string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "image/")
}

func isHTTP(u string) bool {
	pu, err := url.Parse(u)
	return err == nil && pu.Host != "" && (pu.Scheme == "http" || pu.Scheme == "https")
}
