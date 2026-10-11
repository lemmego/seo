package seo

import (
	"context"
	"errors"
	"html/template"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lemmego/api/app"
	"github.com/lemmego/apptest"
	"github.com/lemmego/inertia"
)

func TestSlugify(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Hello World", "hello-world"},
		{"  Trim  Me  ", "trim-me"},
		{"Multiple---Hyphens", "multiple-hyphens"},
		{"Symbols!@#$%^", "symbols-at-sharp"},
		{"José Álvarez", "jose-alvarez"},
		{"Müller & Söhne", "muller-and-sohne"},
		{"Ünïcödé Çhäos", "unicode-chaos"},
		{"C++ and Go", "c-plus-plus-and-go"},
		{"already-a-slug", "already-a-slug"},
		{"UPPER CASE", "upper-case"},
		{"trailing-", "trailing"},
		{"-leading", "leading"},
		{"123 numbers 456", "123-numbers-456"},
		{"straße", "strasse"},
		// Nothing foldable: the caller must handle an empty slug, which is
		// why UniqueSlug takes a fallback.
		{"日本語", ""},
		{"", ""},
		{"!!!", ""},
	} {
		if got := Slugify(tc.in, 0); got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Truncation must land on a word boundary. A slug cut mid-word reads like a
// typo, and it is the first thing a person sees of a URL.
func TestSlugifyTruncatesOnAWordBoundary(t *testing.T) {
	got := Slugify("the quick brown fox jumps over the lazy dog", 20)
	if len(got) > 20 {
		t.Fatalf("Slugify = %q, longer than the limit", got)
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("Slugify = %q, ends on a hyphen", got)
	}
	if got != "the-quick-brown-fox" {
		t.Errorf("Slugify = %q, want it cut at the last whole word", got)
	}

	// A single word longer than the limit has no boundary to cut at, so a
	// hard cut is all there is.
	if got := Slugify("supercalifragilisticexpialidocious", 10); got != "supercalif" {
		t.Errorf("Slugify of one long word = %q", got)
	}
}

func TestUniqueSlugAppendsASuffixUntilFree(t *testing.T) {
	used := map[string]bool{"my-app": true, "my-app-2": true}
	taken := func(_ context.Context, slug string) (bool, error) { return used[slug], nil }

	got, err := UniqueSlug(context.Background(), "My App", "", 0, taken)
	if err != nil {
		t.Fatal(err)
	}
	if got != "my-app-3" {
		t.Errorf("UniqueSlug = %q, want my-app-3", got)
	}
}

func TestUniqueSlugUsesTheFallbackWhenTheTitleYieldsNothing(t *testing.T) {
	taken := func(_ context.Context, _ string) (bool, error) { return false, nil }

	got, err := UniqueSlug(context.Background(), "日本語", "launch-42", 0, taken)
	if err != nil {
		t.Fatal(err)
	}
	if got != "launch-42" {
		t.Errorf("UniqueSlug = %q, want the fallback", got)
	}
}

func TestUniqueSlugReportsWhenNothingIsUsable(t *testing.T) {
	taken := func(_ context.Context, _ string) (bool, error) { return false, nil }
	if _, err := UniqueSlug(context.Background(), "!!!", "???", 0, taken); !errors.Is(err, ErrNoSlug) {
		t.Errorf("err = %v, want ErrNoSlug", err)
	}
}

func TestUniqueSlugPropagatesALookupError(t *testing.T) {
	boom := errors.New("database is down")
	taken := func(_ context.Context, _ string) (bool, error) { return false, boom }
	if _, err := UniqueSlug(context.Background(), "My App", "", 0, taken); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the lookup error", err)
	}
}

// A pathological collection must not hold the request open forever.
func TestUniqueSlugIsBounded(t *testing.T) {
	calls := 0
	taken := func(_ context.Context, _ string) (bool, error) {
		calls++
		return true, nil // everything is taken
	}

	got, err := UniqueSlug(context.Background(), "My App", "", 0, taken)
	if err != nil {
		t.Fatal(err)
	}
	if calls > maxSlugAttempts {
		t.Errorf("made %d lookups, want at most %d", calls, maxSlugAttempts)
	}
	if !strings.HasPrefix(got, "my-app-") {
		t.Errorf("fallback slug = %q", got)
	}
}

func TestMetaRendersTheTagsThatMatter(t *testing.T) {
	defaults := Defaults{
		BaseURL:  "https://productshore.com",
		SiteName: "Productshore",
		Locale:   "en_US",
	}
	head := string(defaults.apply(Meta{
		Title:       "Ada Lovelace",
		Description: "Building things on the internet",
		Canonical:   "/@ada",
		Image:       "/avatars/ada.png",
		Type:        "profile",
	}).Render())

	for _, want := range []string{
		"<title>Ada Lovelace</title>",
		`<meta name="description" content="Building things on the internet">`,
		`<link rel="canonical" href="https://productshore.com/@ada">`,
		`<meta property="og:type" content="profile">`,
		`<meta property="og:url" content="https://productshore.com/@ada">`,
		`<meta property="og:image" content="https://productshore.com/avatars/ada.png">`,
		`<meta property="og:site_name" content="Productshore">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta name="robots" content="index,follow">`,
	} {
		if !strings.Contains(head, want) {
			t.Errorf("head is missing %s\ngot:\n%s", want, head)
		}
	}
}

// A relative canonical is ignored by some crawlers, and a wrong absolute one
// is worse than none. An already-absolute value must survive, so a CDN image
// is not mangled.
func TestCanonicalAndImageAreMadeAbsolute(t *testing.T) {
	defaults := Defaults{BaseURL: "https://productshore.com/"}
	m := defaults.apply(Meta{
		Canonical: "launch/my-app",
		Image:     "https://cdn.example.com/a.png",
	})

	if m.Canonical != "https://productshore.com/launch/my-app" {
		t.Errorf("Canonical = %q", m.Canonical)
	}
	if m.Image != "https://cdn.example.com/a.png" {
		t.Errorf("an absolute image was rewritten to %q", m.Image)
	}
}

// Declaring a large card with no image renders as a broken card rather than
// falling back to a summary.
func TestTwitterCardMatchesWhetherThereIsAnImage(t *testing.T) {
	if got := (Defaults{}).apply(Meta{}).TwitterCard; got != "summary" {
		t.Errorf("card with no image = %q, want summary", got)
	}
	if got := (Defaults{}).apply(Meta{Image: "/a.png"}).TwitterCard; got != "summary_large_image" {
		t.Errorf("card with an image = %q, want summary_large_image", got)
	}
}

// A staging deploy indexed alongside production splits its own ranking, and
// it is tedious to undo. NoIndex must win over whatever a page asks for.
func TestNoIndexOverridesThePage(t *testing.T) {
	m := Defaults{NoIndex: true}.apply(Meta{Robots: "index,follow"})
	if m.Robots != "noindex,nofollow" {
		t.Errorf("Robots = %q, want noindex,nofollow", m.Robots)
	}
}

// Attribute values must be escaped, or a title containing a quote closes the
// attribute and everything after it becomes markup.
func TestMetaEscapesAttributeValues(t *testing.T) {
	head := string(Meta{
		Title:       `Ada" onload="alert(1)`,
		Description: "<script>alert(1)</script>",
	}.Render())

	if strings.Contains(head, `onload="alert(1)"`) {
		t.Errorf("an attribute was broken out of:\n%s", head)
	}
	if strings.Contains(head, "<script>") {
		t.Errorf("a script tag survived escaping:\n%s", head)
	}
}

// A </script> inside a JSON string would close the element and turn the rest
// of the document into markup. Escaping the slash keeps the JSON equivalent.
func TestJSONLDCannotBreakOutOfTheScriptElement(t *testing.T) {
	script := string(JSONLD(map[string]any{
		"@type": "Person",
		"name":  `</script><img src=x onerror=alert(1)>`,
	}))

	if strings.Contains(script, "</script><img") {
		t.Errorf("JSON-LD broke out of the script element:\n%s", script)
	}
	if !strings.HasSuffix(script, "</script>") {
		t.Errorf("JSON-LD is not a closed script element:\n%s", script)
	}
	if !strings.Contains(script, `"@type":"Person"`) {
		t.Errorf("JSON-LD lost its content:\n%s", script)
	}
}

func TestRobotsRender(t *testing.T) {
	blocked := string(Robots{DisallowAll: true, SitemapURL: "https://x.com/sitemap.xml"}.Render())
	if !strings.Contains(blocked, "Disallow: /\n") {
		t.Errorf("DisallowAll did not block everything:\n%s", blocked)
	}
	if !strings.Contains(blocked, "Sitemap: https://x.com/sitemap.xml") {
		t.Errorf("no sitemap line:\n%s", blocked)
	}

	open := string(Robots{Disallow: []string{"/admin", "/tasker"}}.Render())
	if !strings.Contains(open, "Disallow: /admin\n") || !strings.Contains(open, "Disallow: /tasker\n") {
		t.Errorf("paths missing:\n%s", open)
	}
	if strings.Contains(open, "Disallow: /\n") {
		t.Errorf("an open robots.txt blocked everything:\n%s", open)
	}
}

// A zero LastMod published as the Go epoch tells a crawler the page last
// changed in year 1, which is worse than saying nothing.
func TestFormatLastModOmitsTheZeroTime(t *testing.T) {
	if got := formatLastMod(time.Time{}); got != "" {
		t.Errorf("formatLastMod(zero) = %q, want empty", got)
	}
	if got := formatLastMod(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)); !strings.HasPrefix(got, "2026-03-01T12:00:00") {
		t.Errorf("formatLastMod = %q", got)
	}
}

// A sitemap where every URL declares 0.5 has said nothing.
func TestFormatPriorityOmitsTheDefault(t *testing.T) {
	if got := formatPriority(0.5); got != "" {
		t.Errorf("formatPriority(0.5) = %q, want empty", got)
	}
	if got := formatPriority(0); got != "" {
		t.Errorf("formatPriority(0) = %q, want empty", got)
	}
	if got := formatPriority(0.9); got != "0.9" {
		t.Errorf("formatPriority(0.9) = %q", got)
	}
	if got := formatPriority(5); got != "1.0" {
		t.Errorf("formatPriority(5) = %q, want it clamped", got)
	}
}

// Share must publish the rendered head to the root template, because that is
// what makes it independent of the SSR process. If this ever stops working,
// every public page silently loses its metadata on the SSR fallback path and
// only a crawler notices.
func TestShareReachesTheRootTemplate(t *testing.T) {
	a := apptest.New(t,
		apptest.WithProviders(&Provider{Defaults: Defaults{
			BaseURL:  "https://productshore.com",
			SiteName: "Productshore",
		}}),
		apptest.WithRoutes(func(a app.App) {
			a.Router().Get("/probe", func(c app.Context) error {
				Share(c, Meta{Title: "Ada Lovelace", Canonical: "/@ada"})
				// Read back what Share put on the request, which is what the
				// root template renders.
				head, _ := inertia.GetTemplateData(c.RequestContext())[TemplateKey].(template.HTML)
				return c.JSON(app.M{"head": string(head)})
			})
		}),
	)

	res := a.Get("/probe").Send().AssertStatus(200)
	head, _ := res.JSONValue("head").(string)
	for _, want := range []string{
		"<title>Ada Lovelace</title>",
		`href="https://productshore.com/@ada"`,
		`content="Productshore"`,
	} {
		if !strings.Contains(head, want) {
			t.Errorf("the shared head is missing %s\ngot:\n%s", want, head)
		}
	}
}

// Sitemap must always serve an index, even when every source fits in one
// shard. Switching from a bare urlset to an index later changes URLs a
// crawler has already fetched, for no benefit.
func TestSitemapAlwaysServesAnIndex(t *testing.T) {
	sitemap := &Sitemap{Sources: []Source{&fixedSource{name: "profiles", urls: []URL{
		{Loc: "/@ada", LastMod: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Priority: 0.8},
	}}}}

	a := apptest.New(t,
		apptest.WithProviders(&Provider{
			Defaults: Defaults{BaseURL: "https://productshore.com"},
			Sitemap:  sitemap,
		}),
	)

	a.Get("/sitemap.xml").Send().AssertStatus(200).
		AssertSee("<sitemapindex").
		AssertSee("https://productshore.com/sitemap/profiles-1.xml")

	a.Get("/sitemap/profiles-1.xml").Send().AssertStatus(200).
		AssertSee("<urlset").
		AssertSee("<loc>https://productshore.com/@ada</loc>").
		AssertSee("<lastmod>2026-03-01T00:00:00Z</lastmod>").
		AssertSee("<priority>0.8</priority>")
}

// A source with no rows yet still gets one shard, so the URL in the index
// does not 404 the first time a crawler follows it.
func TestSitemapServesAnEmptySourceWithoutA404(t *testing.T) {
	sitemap := &Sitemap{Sources: []Source{&fixedSource{name: "launches"}}}

	a := apptest.New(t,
		apptest.WithProviders(&Provider{
			Defaults: Defaults{BaseURL: "https://productshore.com"},
			Sitemap:  sitemap,
		}),
	)

	a.Get("/sitemap.xml").Send().AssertStatus(200).
		AssertSee("sitemap/launches-1.xml")
	a.Get("/sitemap/launches-1.xml").Send().AssertStatus(200).AssertSee("<urlset")
}

func TestSitemapShardsAtTheLimit(t *testing.T) {
	var urls []URL
	for i := 0; i < 5; i++ {
		urls = append(urls, URL{Loc: "/p/" + strconv.Itoa(i)})
	}
	sitemap := &Sitemap{MaxPerFile: 2, Sources: []Source{&fixedSource{name: "p", urls: urls}}}

	a := apptest.New(t,
		apptest.WithProviders(&Provider{
			Defaults: Defaults{BaseURL: "https://x.com"},
			Sitemap:  sitemap,
		}),
	)

	// 5 URLs at 2 per file is 3 shards.
	index := a.Get("/sitemap.xml").Send().AssertStatus(200)
	for _, want := range []string{"sitemap/p-1.xml", "sitemap/p-2.xml", "sitemap/p-3.xml"} {
		index.AssertSee(want)
	}
	index.AssertDontSee("sitemap/p-4.xml")

	// The last shard holds the remainder.
	a.Get("/sitemap/p-3.xml").Send().AssertStatus(200).
		AssertSee("<loc>https://x.com/p/4</loc>").
		AssertDontSee("/p/3<")
}

func TestSitemapRefusesAnUnknownSourceOrPage(t *testing.T) {
	sitemap := &Sitemap{Sources: []Source{&fixedSource{name: "profiles"}}}
	a := apptest.New(t,
		apptest.WithProviders(&Provider{Sitemap: sitemap}),
	)

	a.Get("/sitemap/nothing-1.xml").Send().AssertStatus(404)
	a.Get("/sitemap/profiles-0.xml").Send().AssertStatus(404)
	a.Get("/sitemap/profiles-abc.xml").Send().AssertStatus(404)
}

func TestRobotsIsServed(t *testing.T) {
	sitemap := &Sitemap{Robots: Robots{
		DisallowAll: true,
		SitemapURL:  "https://x.com/sitemap.xml",
	}}
	a := apptest.New(t,
		apptest.WithProviders(&Provider{Sitemap: sitemap}),
	)

	a.Get("/robots.txt").Send().AssertStatus(200).
		AssertHeader("Content-Type", "text/plain; charset=utf-8").
		AssertSee("Disallow: /").
		AssertSee("Sitemap: https://x.com/sitemap.xml")
}

// fixedSource is a Source over a fixed slice.
type fixedSource struct {
	name string
	urls []URL
}

func (s *fixedSource) Name() string { return s.name }

func (s *fixedSource) Count(context.Context) (int, error) { return len(s.urls), nil }

func (s *fixedSource) Page(_ context.Context, offset, limit int) ([]URL, error) {
	if offset >= len(s.urls) {
		return nil, nil
	}
	end := offset + limit
	if end > len(s.urls) {
		end = len(s.urls)
	}
	return s.urls[offset:end], nil
}
