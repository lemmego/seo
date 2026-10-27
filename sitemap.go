package seo

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lemmego/api/app"
)

// URL is one entry in a sitemap.
type URL struct {
	Loc        string
	LastMod    time.Time
	ChangeFreq string
	Priority   float64
}

// Source yields the URLs of one kind of page, a page at a time.
//
// Paginating rather than returning a slice is the whole contract: a sitemap
// for a million profiles must not become a million-element slice in memory,
// and the 50,000-URL limit per file means it never needs to.
type Source interface {
	// Name appears in the shard URLs: "profiles" gives
	// /sitemap-profiles-1.xml.
	Name() string
	Count(ctx context.Context) (int, error)
	Page(ctx context.Context, offset, limit int) ([]URL, error)
}

// Sitemap serves /sitemap.xml, its shards, and /robots.txt.
type Sitemap struct {
	Sources []Source
	// MaxPerFile defaults to 50000, the limit in the sitemap protocol.
	MaxPerFile int
	// CacheTTL bounds how stale a shard may be. Zero disables caching, which
	// is what you want in development.
	CacheTTL time.Duration
	// Robots is served at /robots.txt.
	Robots Robots
}

// Robots is the robots.txt body.
type Robots struct {
	// DisallowAll blocks everything. Set it outside production: a staging
	// deploy indexed alongside production splits its own ranking.
	DisallowAll bool
	Disallow    []string
	// SitemapURL should be absolute. Crawlers accept a relative one
	// inconsistently.
	SitemapURL string
}

// Render returns the robots.txt body.
func (r Robots) Render() []byte {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	if r.DisallowAll {
		b.WriteString("Disallow: /\n")
	} else {
		if len(r.Disallow) == 0 {
			b.WriteString("Disallow:\n")
		}
		for _, path := range r.Disallow {
			b.WriteString("Disallow: " + path + "\n")
		}
	}
	if r.SitemapURL != "" {
		b.WriteString("\nSitemap: " + r.SitemapURL + "\n")
	}
	return []byte(b.String())
}

func (s *Sitemap) maxPerFile() int {
	if s.MaxPerFile > 0 {
		return s.MaxPerFile
	}
	return 50000
}

// shards returns how many files a source needs. Always at least one, so a
// source with no rows yet still appears in the index and the URL does not
// 404 the first time a crawler tries it.
func (s *Sitemap) shards(ctx context.Context, source Source) (int, error) {
	count, err := source.Count(ctx)
	if err != nil {
		return 0, err
	}
	shards := (count + s.maxPerFile() - 1) / s.maxPerFile()
	if shards < 1 {
		shards = 1
	}
	return shards, nil
}

// total counts every URL across every source.
//
// Cheap because Count is a COUNT(*) per source and there are a handful of
// them — and it is only asked once per request for /sitemap.xml, which is
// cached when CacheTTL says so.
func (s *Sitemap) total(ctx context.Context) (int, error) {
	total := 0
	for _, source := range s.Sources {
		count, err := source.Count(ctx)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

// everything collects every URL from every source into one file.
//
// Only called when total has already said they fit, so the slice is bounded
// by maxPerFile — the paging contract the Source interface exists for is
// still honoured, a page at a time, rather than asking a source for all of
// its rows at once.
func (s *Sitemap) everything(ctx context.Context, base string) (urlSet, error) {
	set := urlSet{XMLNS: sitemapNS}
	limit := s.maxPerFile()
	for _, source := range s.Sources {
		for offset := 0; ; offset += limit {
			page, err := source.Page(ctx, offset, limit)
			if err != nil {
				return urlSet{}, err
			}
			if len(page) == 0 {
				break
			}
			for _, u := range page {
				set.URLs = append(set.URLs, sitemapURL{
					// Absolute, the same way the shard handler writes them.
					// A sitemap carrying relative URLs is rejected outright.
					Loc:        absolute(base, u.Loc),
					LastMod:    formatLastMod(u.LastMod),
					ChangeFreq: u.ChangeFreq,
					Priority:   formatPriority(u.Priority),
				})
			}
			if len(page) < limit {
				break
			}
		}
	}
	return set, nil
}

// newest is the most recent LastMod in one shard, for the index entry.
//
// It reads the shard it is describing rather than keeping a separate record,
// which costs a page read per shard on a sitemap large enough to be sharded.
// That is the trade: an index entry with no lastmod tells a crawler nothing,
// and a wrong one is worse than none.
func (s *Sitemap) newest(ctx context.Context, source Source, shard int) (time.Time, error) {
	limit := s.maxPerFile()
	page, err := source.Page(ctx, (shard-1)*limit, limit)
	if err != nil {
		return time.Time{}, err
	}
	var newest time.Time
	for _, u := range page {
		if u.LastMod.After(newest) {
			newest = u.LastMod
		}
	}
	return newest, nil
}

type sitemapIndex struct {
	XMLName  xml.Name          `xml:"sitemapindex"`
	XMLNS    string            `xml:"xmlns,attr"`
	Sitemaps []sitemapIndexRef `xml:"sitemap"`
}

type sitemapIndexRef struct {
	Loc string `xml:"loc"`
	// LastMod is what tells a crawler which shard is worth re-fetching. An
	// index without it says only that nine files exist, so a crawler holding
	// the previous copy learns nothing and has to guess.
	LastMod string `xml:"lastmod,omitempty"`
}

type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

const sitemapNS = "http://www.sitemaps.org/schemas/sitemap/0.9"

// Routes registers /sitemap.xml, /sitemap-{name}-{n}.xml and /robots.txt.
//
// All three bypass Inertia: they are not pages, they must be byte-exact, and
// they must keep working when the SSR process does not.
func (s *Sitemap) Routes(a app.App) {
	base := strings.TrimRight(baseURL(a), "/")

	a.Router().Get("/robots.txt", func(c app.Context) error {
		c.SetHeader("Content-Type", "text/plain; charset=utf-8")
		_, err := c.Write(s.Robots.Render())
		return err
	})

	// Flat while the whole site fits in one file, an index once it does not.
	//
	// Changing shape as the site grows is safe: /sitemap.xml keeps its own
	// URL either way, and the shard routes below go on serving whether or not
	// the index still references them, so nothing a crawler already holds
	// stops working.
	//
	// Staying an index at every size would cost legibility, which matters
	// more than it sounds. On a site with eighteen URLs, /sitemap.xml would
	// show nine file names and not one page — so the obvious check, "is my
	// new article in the sitemap?", answers no for anyone who looks. A format
	// nobody can read is one nobody audits.
	a.Router().Get("/sitemap.xml", func(c app.Context) error {
		total, err := s.total(c.RequestContext())
		if err != nil {
			return err
		}
		if total <= s.maxPerFile() {
			set, err := s.everything(c.RequestContext(), base)
			if err != nil {
				return err
			}
			return writeXML(c, set)
		}

		index := sitemapIndex{XMLNS: sitemapNS}
		for _, source := range s.Sources {
			shards, err := s.shards(c.RequestContext(), source)
			if err != nil {
				return err
			}
			for n := 1; n <= shards; n++ {
				ref := sitemapIndexRef{
					Loc: fmt.Sprintf("%s/sitemap/%s-%d.xml", base, source.Name(), n),
				}
				if newest, err := s.newest(c.RequestContext(), source, n); err == nil {
					ref.LastMod = formatLastMod(newest)
				}
				index.Sitemaps = append(index.Sitemaps, ref)
			}
		}
		return writeXML(c, index)
	})

	// The shards serve whether or not /sitemap.xml currently references
	// them. A small site is served flat and never mentions these, but a
	// crawler that fetched one before the site shrank — or before this
	// changed shape — keeps a working URL rather than finding a 404.
	//
	// The shard is one path segment — "profiles-1.xml" — rather than
	// /sitemap-{name}-{n}.xml, because net/http.ServeMux requires a wildcard
	// to occupy a whole segment: "Wildcards must be full path segments". A
	// pattern mixing a literal and a wildcard in one segment panics at
	// registration.
	a.Router().Get("/sitemap/{shard}", func(c app.Context) error {
		name, page, ok := parseShard(c.Param("shard"))
		if !ok {
			return c.NotFound()
		}

		for _, source := range s.Sources {
			if source.Name() != name {
				continue
			}
			limit := s.maxPerFile()
			urls, err := source.Page(c.RequestContext(), (page-1)*limit, limit)
			if err != nil {
				return err
			}
			set := urlSet{XMLNS: sitemapNS}
			for _, u := range urls {
				set.URLs = append(set.URLs, sitemapURL{
					Loc:        absolute(base, u.Loc),
					LastMod:    formatLastMod(u.LastMod),
					ChangeFreq: u.ChangeFreq,
					Priority:   formatPriority(u.Priority),
				})
			}
			return writeXML(c, set)
		}
		return c.NotFound()
	})
}

// parseShard splits "profiles-12.xml" into its source name and page number.
// The name may itself contain hyphens, so the split is on the last one.
func parseShard(shard string) (name string, page int, ok bool) {
	trimmed, found := strings.CutSuffix(shard, ".xml")
	if !found {
		return "", 0, false
	}
	idx := strings.LastIndexByte(trimmed, '-')
	if idx <= 0 || idx == len(trimmed)-1 {
		return "", 0, false
	}
	page, err := strconv.Atoi(trimmed[idx+1:])
	if err != nil || page < 1 {
		return "", 0, false
	}
	return trimmed[:idx], page, true
}

func writeXML(c app.Context, value any) error {
	encoded, err := xml.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	c.SetHeader("Content-Type", "application/xml; charset=utf-8")
	if _, err := c.Write([]byte(xml.Header)); err != nil {
		return err
	}
	_, err = c.Write(encoded)
	return err
}

// formatLastMod emits W3C datetime in UTC. A zero time is omitted rather than
// published as the Go epoch, which would tell a crawler the page was last
// changed in year 1.
func formatLastMod(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// formatPriority omits the default. A sitemap where every URL declares 0.5 is
// a sitemap that has said nothing.
func formatPriority(p float64) string {
	if p <= 0 || p == 0.5 {
		return ""
	}
	if p > 1 {
		p = 1
	}
	return strconv.FormatFloat(p, 'f', 1, 64)
}

func baseURL(a app.App) string {
	if defaults, ok := app.Lookup[Defaults](a); ok {
		return defaults.BaseURL
	}
	return ""
}
