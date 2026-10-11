package seo

import (
	"html/template"
	"strings"
)

// Meta is everything one page's <head> needs for search and social.
//
// Every field is optional; Defaults fills what a page leaves empty. The zero
// Meta still renders a title, a canonical URL and a robots directive, which
// is the point — a handler that forgets to set anything should still produce
// a page a crawler can read correctly, not one with no canonical and an
// accidental index in staging.
type Meta struct {
	Title       string
	Description string
	// Canonical may be a path. Defaults.BaseURL is prepended, because a
	// relative canonical is ignored by some crawlers and a wrong absolute one
	// is worse than none.
	Canonical string
	// Robots defaults to "index,follow" in production and
	// "noindex,nofollow" everywhere else.
	Robots string
	Image  string
	// ImageAlt is read by screen readers on some platforms, so it is worth
	// setting even though no crawler ranks on it.
	ImageAlt string
	// Type is the Open Graph type: "website", "profile", "article".
	Type        string
	SiteName    string
	TwitterCard string
	TwitterSite string
	Locale      string
	// JSONLD values are marshalled into one script tag each. This package
	// does not model schema.org — pass your own structs or a map, so a new
	// schema type never needs a release here.
	JSONLD []any
	// Extra is the escape hatch for a tag this struct does not model.
	Extra []Tag
}

// Tag is a raw meta tag. Exactly one of Name or Property should be set.
type Tag struct {
	Name     string
	Property string
	Content  string
}

// Defaults fill any field a page left empty.
type Defaults struct {
	BaseURL     string
	SiteName    string
	TwitterSite string
	Locale      string
	// DefaultImage is used for a page with no image of its own, so a shared
	// link is never a bare grey box.
	DefaultImage string
	// NoIndex forces noindex,nofollow regardless of what a page asks for.
	// Set it in every environment that is not production: a staging deploy
	// indexed alongside production splits its own ranking and is tedious to
	// undo.
	NoIndex bool
}

func (d Defaults) apply(m Meta) Meta {
	if m.SiteName == "" {
		m.SiteName = d.SiteName
	}
	if m.TwitterSite == "" {
		m.TwitterSite = d.TwitterSite
	}
	if m.Locale == "" {
		m.Locale = d.Locale
	}
	if m.Image == "" {
		m.Image = d.DefaultImage
	}
	if m.Type == "" {
		m.Type = "website"
	}
	if m.TwitterCard == "" {
		// A large card for a page with an image, a summary otherwise.
		// Declaring summary_large_image with no image renders as a broken
		// card rather than falling back.
		if m.Image != "" {
			m.TwitterCard = "summary_large_image"
		} else {
			m.TwitterCard = "summary"
		}
	}
	m.Canonical = absolute(d.BaseURL, m.Canonical)
	m.Image = absolute(d.BaseURL, m.Image)

	switch {
	case d.NoIndex:
		m.Robots = "noindex,nofollow"
	case m.Robots == "":
		m.Robots = "index,follow"
	}
	return m
}

// absolute resolves a path against the base URL. An already-absolute value is
// left alone, so a CDN image URL survives.
func absolute(base, value string) string {
	if value == "" || base == "" {
		return value
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(value, "/")
}

// Render returns the <head> fragment.
//
// It returns template.HTML, so html/template will not escape it — the content
// of every attribute is escaped individually on the way in, which is the
// correct boundary. Returning a plain string and marking it safe at the call
// site would put that decision in every template instead of here.
func (m Meta) Render() template.HTML {
	var b strings.Builder

	if m.Title != "" {
		b.WriteString("<title>" + escape(m.Title) + "</title>\n")
	}
	writeMeta(&b, "name", "description", m.Description)
	writeMeta(&b, "name", "robots", m.Robots)
	if m.Canonical != "" {
		b.WriteString(`<link rel="canonical" href="` + escape(m.Canonical) + `">` + "\n")
	}

	writeMeta(&b, "property", "og:type", m.Type)
	writeMeta(&b, "property", "og:title", m.Title)
	writeMeta(&b, "property", "og:description", m.Description)
	writeMeta(&b, "property", "og:url", m.Canonical)
	writeMeta(&b, "property", "og:site_name", m.SiteName)
	writeMeta(&b, "property", "og:locale", m.Locale)
	writeMeta(&b, "property", "og:image", m.Image)
	writeMeta(&b, "property", "og:image:alt", m.ImageAlt)

	writeMeta(&b, "name", "twitter:card", m.TwitterCard)
	writeMeta(&b, "name", "twitter:site", m.TwitterSite)
	writeMeta(&b, "name", "twitter:title", m.Title)
	writeMeta(&b, "name", "twitter:description", m.Description)
	writeMeta(&b, "name", "twitter:image", m.Image)

	for _, tag := range m.Extra {
		if tag.Name != "" {
			writeMeta(&b, "name", tag.Name, tag.Content)
		} else if tag.Property != "" {
			writeMeta(&b, "property", tag.Property, tag.Content)
		}
	}

	for _, value := range m.JSONLD {
		if script, err := jsonLD(value); err == nil {
			b.WriteString(string(script) + "\n")
		}
	}

	return template.HTML(b.String())
}

func writeMeta(b *strings.Builder, attr, key, content string) {
	if content == "" {
		return
	}
	b.WriteString(`<meta ` + attr + `="` + key + `" content="` + escape(content) + `">` + "\n")
}

// escape escapes an attribute value. html/template's HTMLEscapeString covers
// the five characters that matter in an attribute context.
func escape(s string) string {
	return template.HTMLEscapeString(s)
}
