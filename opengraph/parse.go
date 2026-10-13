package opengraph

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// parse reads the metadata out of a document.
//
// A tokenizer rather than a tree: the tags are all in the head, so this can
// stop at </head> and never build a node for the body — which for a large
// page is most of the work and all of the memory.
func parse(body io.Reader, base *url.URL, contentType string) (Preview, error) {
	// x/net/html assumes UTF-8. A page declaring Shift_JIS or ISO-8859-1 —
	// still common outside English — otherwise yields mojibake in the title,
	// which is worse than no title because it looks like our bug.
	decoded, err := charset.NewReader(body, contentType)
	if err != nil {
		// Unknown encoding. The bytes are still worth reading as UTF-8,
		// since the tag names are ASCII either way.
		decoded = body
	}

	found := map[string]string{}
	tokenizer := html.NewTokenizer(decoded)
	var documentTitle string

	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if err := tokenizer.Err(); err != io.EOF && err != io.ErrUnexpectedEOF {
				// A parse error on a truncated or malformed document still
				// leaves whatever was read before it, and that is usually
				// the head.
				if len(found) == 0 && documentTitle == "" {
					return Preview{}, fmt.Errorf("opengraph: reading the document: %w", err)
				}
			}
			return assemble(found, documentTitle, base), nil

		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := tokenizer.TagName()
			switch string(name) {
			case "meta":
				if hasAttr {
					readMeta(tokenizer, found)
				}
			case "title":
				if tokenizer.Next() == html.TextToken {
					documentTitle = strings.TrimSpace(string(tokenizer.Text()))
				}
			case "body":
				// Everything this reads lives in the head. Some documents
				// omit </head> entirely, so the start of the body is the
				// reliable boundary.
				return assemble(found, documentTitle, base), nil
			}

		case html.EndTagToken:
			if name, _ := tokenizer.TagName(); string(name) == "head" {
				return assemble(found, documentTitle, base), nil
			}
		}
	}
}

// readMeta records one meta tag, keyed by its property or name.
func readMeta(tokenizer *html.Tokenizer, found map[string]string) {
	var key, content string
	for {
		name, value, more := tokenizer.TagAttr()
		switch string(name) {
		case "property", "name":
			// Open Graph specifies property=; Twitter specifies name=; and
			// plenty of pages use the other one. Reading both is what makes
			// this work on real documents rather than on the specification.
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(string(value)))
			}
		case "content":
			content = string(value)
		}
		if !more {
			break
		}
	}
	if key == "" || content == "" {
		return
	}
	// First one wins. A page that repeats og:image is listing alternatives,
	// and the first is the one it means.
	if _, already := found[key]; !already {
		found[key] = strings.TrimSpace(content)
	}
}

// assemble resolves the fields in the order practice has settled on.
func assemble(found map[string]string, documentTitle string, base *url.URL) Preview {
	preview := Preview{
		Title:       first(found, "og:title", "twitter:title"),
		Description: first(found, "og:description", "twitter:description", "description"),
		SiteName:    first(found, "og:site_name", "application-name"),
		Type:        found["og:type"],
	}
	if preview.Title == "" {
		// The <title> element, which is what a browser tab shows and what a
		// page without any Open Graph tags still has.
		preview.Title = documentTitle
	}
	if image := first(found, "og:image", "og:image:url", "og:image:secure_url", "twitter:image"); image != "" {
		preview.Image = absolute(image, base)
	}
	return preview
}

func first(found map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := found[key]; value != "" {
			return value
		}
	}
	return ""
}

// absolute resolves an image reference against the page it was found on, and
// drops anything that is not http or https.
//
// Both halves matter. A relative og:image is ordinary and would otherwise be
// stored as "/img/card.png", which resolves against *our* host when the feed
// renders it. And a javascript: or data: URL in an attribute that ends up in
// an img src is the reason the scheme is checked rather than assumed.
func absolute(reference string, base *url.URL) string {
	parsed, err := url.Parse(strings.TrimSpace(reference))
	if err != nil {
		return ""
	}
	if base != nil {
		parsed = base.ResolveReference(parsed)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.String()
	default:
		return ""
	}
}
