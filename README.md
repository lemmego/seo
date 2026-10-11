# lemmego/seo

Meta tags, JSON-LD, sitemaps, robots.txt and slugs for Lemmego applications —
with the document head rendered **in Go**, so it survives an SSR outage.

```go
import "github.com/lemmego/seo"

func Show(c app.Context) error {
    user := …
    seo.Share(c, seo.Meta{
        Title:       user.Name + " (@" + user.Handle + ")",
        Description: user.Headline,
        Canonical:   "/@" + user.Handle,
        Type:        "profile",
        Image:       user.AvatarURL,
        JSONLD:      []any{personFor(user)},
    })
    return inertia.Respond(c, "Profiles/Show", props)
}
```

## The one idea worth understanding

**Metadata emitted by a page component disappears exactly when you cannot see
that it has.**

Inertia falls back to the client-rendered container on any SSR error. On that
path the head it would have produced is empty — so a `<Head>` in your React
page renders nothing, while the page still answers **200** and still looks
correct to a person. Only a crawler notices, and only weeks later.

Rendering the head in Go makes it independent of the Node process: correct on
a cold boot, with SSR down, during a deploy, and with Node not installed at
all. Leave the page component owning `document.title` for client-side
navigation, and nothing else.

This has been demonstrated rather than asserted: with the SSR process killed,
a profile page's body degrades to client rendering while the title, the
canonical URL and the `Person` block all survive.

## Install

```bash
go get github.com/lemmego/seo
```

Two steps. Register the provider:

```go
&seo.Provider{
    Defaults: seo.Defaults{
        BaseURL:  config.MustEnv("APP_URL", "http://localhost:8080"),
        SiteName: "Productshore",
        NoIndex:  config.MustEnv("APP_ENV", "development") != "production",
    },
    Sitemap: &seo.Sitemap{
        CacheTTL: time.Hour,
        Robots: seo.Robots{
            SitemapURL: baseURL + "/sitemap.xml",
            Disallow:   []string{"/settings", "/api/"},
        },
    },
    Sources: func(a app.App) []seo.Source {
        return []seo.Source{profiles.NewSource(a), launches.NewSource(a)}
    },
}
```

And add one line to the Inertia root template, **above** `{{ .inertiaHead }}`:

```gohtml
{{ .seoHead }}
{{ .inertiaHead }}
```

Then register `seo.Middleware()` after the session middleware, so a handler
that forgets to call `Share` still emits a title, a canonical URL and the
right robots directive rather than nothing.

## `Defaults` fills what a page leaves empty

The zero `Meta` still renders a title, a canonical URL and a robots
directive. That is the point — a handler that sets nothing should produce a
page a crawler reads correctly, not one with no canonical and an accidental
`index` in staging.

**Set `NoIndex: true` in every environment that is not production.** A staging
deploy indexed alongside production splits its own ranking and is tedious to
undo.

`Canonical` may be a path; `BaseURL` is prepended. A relative canonical is
ignored by some crawlers and a wrong absolute one is worse than none.

## JSON-LD without a schema.org model

```go
seo.Meta{JSONLD: []any{
    map[string]any{
        "@context": "https://schema.org",
        "@type":    "Person",
        "name":     user.Name,
        "url":      baseURL + "/@" + user.Handle,
    },
}}
```

Each value is marshalled into its own `<script type="application/ld+json">`.
This package deliberately does **not** model schema.org: pass your own structs
or a map, so a new schema type never needs a release here.

`JSONLD` escapes so a value cannot break out of the script element — there is
a test named for exactly that, because `</script>` inside a string is the one
way this becomes an XSS hole rather than a formatting bug.

Attribute values are escaped too. A name containing a quote produces a
correct tag, not a broken one.

## Sitemaps that do not load your table into memory

```go
type Source interface {
    Name() string                                              // "profiles"
    Count(ctx context.Context) (int, error)
    Page(ctx context.Context, offset, limit int) ([]URL, error)
}
```

Paginating rather than returning a slice is the whole contract: a sitemap for
a million profiles must not become a million-element slice, and the
50,000-URL limit per file means it never has to.

What gets served:

| Path | Contents |
|---|---|
| `/sitemap.xml` | Always an **index**, even for one small source |
| `/sitemap-profiles-1.xml` | One shard of one source |
| `/robots.txt` | `Robots`, rendered |

It is always an index because the shape of `/sitemap.xml` is then stable: a
site that outgrows one file does not change the URL it has already submitted
to every search engine.

An empty source serves an **empty sitemap, not a 404** — a source with nothing
in it yet is a normal state, and a 404 on a submitted sitemap URL is reported
as an error for as long as it lasts.

`CacheTTL` bounds staleness. Leave it zero in development, where a cached
sitemap is only confusing.

**`Sources` is a function, not a field**, because a source almost always needs
a repository, a repository needs a connection, and neither exists where a
provider literal is written — `LoadProviders` runs before anything is
registered. Assign `Sitemap.Sources` directly only for a source that needs
nothing, like a fixed list of static pages.

## Slugs

```go
seo.Slugify("Hello, World!", 60)          // "hello-world"

slug, err := seo.UniqueSlug(ctx, title, "launch", 60,
    func(ctx context.Context, s string) (bool, error) {
        return repo.SlugTaken(ctx, s)
    })
```

`Slugify` truncates on a **word boundary**, so a long title becomes a
readable slug rather than one ending mid-word.

`UniqueSlug` appends a suffix until the `taken` callback says the slug is
free, is **bounded** so a broken callback cannot loop forever, propagates a
lookup error rather than guessing, and falls back to the supplied word when a
title yields nothing — which happens for a title that is entirely emoji or
entirely punctuation.

Make `taken` return true for **reserved** slugs as well as taken ones. A slug
is derived rather than typed, so no validator can reject it, and a reserved
word reaches the database as a constraint violation at insert time — long
after the person has finished writing.

## Deliberate omissions

No OG image generation, no hreflang, no AMP. Each is a project rather than a
feature, and none of them is needed for a public page to be indexed correctly.

## Testing

```bash
go test ./...
GOWORK=off go test ./...   # as a published module
```

Notable cases: metadata escapes its attribute values, JSON-LD cannot break
out of the script element, `NoIndex` overrides whatever a page asks for, the
sitemap always serves an index, an empty source is not a 404, shards appear at
the limit, and an unknown source or page is refused rather than served empty.
