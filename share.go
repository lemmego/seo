package seo

import (
	"strings"

	"github.com/lemmego/api/app"
	"github.com/lemmego/inertia"
)

// TemplateKey is the root-template variable the rendered head is published
// under. Add it to the Inertia root template, above {{ .inertiaHead }}:
//
//	{{ .seoHead }}
//	{{ .inertiaHead }}
const TemplateKey = "seoHead"

// Share renders the metadata in Go and publishes it to the root template.
//
// Why Go rather than a <Head> component in the page: Inertia falls back to
// the client-rendered container on any SSR error, and on that path the head
// it would have produced is empty. So metadata emitted by the page component
// vanishes exactly when the SSR process is down — while the page still
// answers 200 and still looks correct to a person. Only a crawler notices,
// and only weeks later.
//
// Rendering here makes the head independent of that process: correct on a
// cold boot, with SSR down, during a deploy, and with Node not installed at
// all. Leave the page component owning document.title for client-side
// navigation, and nothing else.
//
// Call it immediately before inertia.Respond.
func Share(c app.Context, m Meta) {
	defaults, _ := app.Lookup[Defaults](c.App())
	if m.Locale == "" {
		m.Locale = OpenGraphLocale(c.Locale())
	}
	rendered := defaults.apply(m).Render()
	c.SetRequest(c.Request().WithContext(
		inertia.SetTemplateDatum(c.RequestContext(), TemplateKey, rendered),
	))
}

// Middleware shares the site-wide defaults for every page, so a handler that
// forgets to call Share still emits a title, a canonical URL and the right
// robots directive rather than nothing.
//
// Register it after the session middleware and before the routes.
func Middleware() app.Handler {
	return func(c app.Context) error {
		Share(c, Meta{Canonical: c.Request().URL.Path})
		return c.Next()
	}
}

// OpenGraphLocale converts a BCP 47 language tag to the form og:locale wants.
//
// Open Graph asks for language_TERRITORY with an underscore — "en_US", not
// "en-US" — which is the one place in this module where the tag a request
// negotiated is not the tag that gets emitted. A hyphen there is not a
// validation error anybody reports; the scraper simply ignores it.
//
// An empty tag stays empty, so the per-page value falls through to
// Defaults.Locale and then to no tag at all. That matters because Defaults is
// a boot-time value in the container, shared by every request: it can only
// ever carry the site's one fallback language, which is why a per-request
// locale has to be resolved here instead.
func OpenGraphLocale(tag string) string {
	return strings.ReplaceAll(tag, "-", "_")
}
