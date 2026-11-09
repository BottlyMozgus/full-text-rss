package extract

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// figureImage is an image from the original page that readability dropped,
// together with the normalized text of the last readability paragraph that
// precedes it in document order ("" when none does).
type figureImage struct {
	after string
	src   string
	alt   string
}

// restoreFigureImages re-inserts article images that go-readability dropped.
//
// Readability joins a top candidate's siblings only, so images in separate
// component blocks (for example a BBC hero figure above the text blocks) are
// lost even though they belong to the article. This function finds <img>
// elements inside <figure>, <article>, or <main> in the original document that
// are missing from content, and inserts each one as its own paragraph after
// the last readability paragraph that precedes it on the page. Images inside
// nav/header/footer/aside and data-testid="promo" blocks are skipped. Only src
// and alt are carried over; the caller's Clean step still applies the allowlist.
func restoreFigureImages(body []byte, pageURL, content string) string {
	doc, err := newDoc(body, pageURL)
	if err != nil {
		return content
	}
	frag, err := parseFragment(content)
	if err != nil {
		return content
	}

	outTexts := map[string]bool{}
	outSrcs := map[string]bool{}
	walkNodes(frag, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.DataAtom {
		case atom.P:
			outTexts[nodeText(n)] = true
		case atom.Img:
			outSrcs[attrVal(n, "src")] = true
		}
	})

	// anchor is the text of the nearest preceding readability paragraph.
	// hasAnchor is false when the image precedes every such paragraph, in
	// which case it goes to the start of the content.
	var pending []figureImage
	seen := map[string]bool{}
	anchor, hasAnchor := "", false
	base := doc.Url.String()
	walkNodes(doc.Selection.Nodes[0], func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.DataAtom {
		case atom.P:
			if t := nodeText(n); t != "" && outTexts[t] {
				anchor, hasAnchor = t, true
			}
		case atom.Img:
			src := resolveURL(base, attrVal(n, "src"))
			if src == "" || outSrcs[src] || seen[src] {
				return
			}
			if hasAncestorAny(n, atom.Nav, atom.Header, atom.Footer, atom.Aside) || hasAncestorAttr(n, "data-testid", "promo") {
				return
			}
			if !hasAncestorAny(n, atom.Figure, atom.Article, atom.Main) {
				return
			}
			seen[src] = true
			fi := figureImage{src: src, alt: attrVal(n, "alt")}
			if hasAnchor {
				fi.after = anchor
			}
			pending = append(pending, fi)
		}
	})
	if len(pending) == 0 {
		return content
	}

	var head []figureImage
	byAnchor := map[string][]figureImage{}
	for _, fi := range pending {
		if fi.after == "" {
			head = append(head, fi)
			continue
		}
		byAnchor[fi.after] = append(byAnchor[fi.after], fi)
	}
	walkNodes(frag, func(n *html.Node) {
		if n.Type != html.ElementNode || n.DataAtom != atom.P {
			return
		}
		t := nodeText(n)
		if t == "" {
			return
		}
		items, ok := byAnchor[t]
		if !ok {
			return
		}
		delete(byAnchor, t)
		insertAfterNode(n, items)
	})
	if len(head) > 0 {
		first := firstElementChild(frag)
		for _, fi := range head {
			p := newImageParagraph(fi)
			if first != nil {
				frag.InsertBefore(p, first)
			} else {
				frag.AppendChild(p)
			}
		}
	}

	var sb strings.Builder
	for n := frag.FirstChild; n != nil; n = n.NextSibling {
		_ = html.Render(&sb, n)
	}
	return sb.String()
}

// insertAfterNode places one image paragraph per item directly after anchor,
// keeping the items' order.
func insertAfterNode(anchor *html.Node, items []figureImage) {
	ref := anchor
	for _, fi := range items {
		p := newImageParagraph(fi)
		if ref.NextSibling != nil {
			ref.Parent.InsertBefore(p, ref.NextSibling)
		} else {
			ref.Parent.AppendChild(p)
		}
		ref = p
	}
}

// newImageParagraph builds <p><img src alt></p>.
func newImageParagraph(fi figureImage) *html.Node {
	img := &html.Node{Type: html.ElementNode, Data: "img", DataAtom: atom.Img}
	img.Attr = append(img.Attr, html.Attribute{Key: "src", Val: fi.src})
	if fi.alt != "" {
		img.Attr = append(img.Attr, html.Attribute{Key: "alt", Val: fi.alt})
	}
	p := &html.Node{Type: html.ElementNode, Data: "p", DataAtom: atom.P}
	p.AppendChild(img)
	return p
}

// walkNodes visits n and its descendants in document order.
func walkNodes(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkNodes(c, fn)
	}
}

// nodeText returns the normalized (single-spaced, trimmed) text of n.
func nodeText(n *html.Node) string {
	var sb strings.Builder
	walkNodes(n, func(c *html.Node) {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
			sb.WriteByte(' ')
		}
	})
	return strings.Join(strings.Fields(sb.String()), " ")
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

// hasAncestorAttr reports whether any element ancestor of n has key=val.
func hasAncestorAttr(n *html.Node, key, val string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && attrVal(p, key) == val {
			return true
		}
	}
	return false
}

func hasAncestorAny(n *html.Node, atoms ...atom.Atom) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		for _, a := range atoms {
			if p.DataAtom == a {
				return true
			}
		}
	}
	return false
}

func firstElementChild(n *html.Node) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			return c
		}
	}
	return nil
}
