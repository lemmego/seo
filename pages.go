package seo

import (
	"context"
	"time"
)

// Pages is a Source for the handful of URLs a site has that are not rows in a
// table: the home page, the section indexes, the legal pages.
//
// They are the ones most worth declaring and the easiest to forget, because
// every other source is generated from a query and this one has to be
// written down. A site whose sitemap lists six thousand profiles and not its
// own home page is the normal outcome of building sources table by table.
type Pages struct {
	// SourceName appears in the shard URL when the site is large enough to
	// shard. Defaults to "pages".
	SourceName string
	// URLs are relative paths — "/" and "/jobs" — made absolute against
	// APP_URL by the caller, exactly as every other source's are.
	URLs []URL
}

// NewPages builds a source from paths, giving each the same LastMod.
//
// LastMod on a static page is a claim about the deployment rather than about
// the content, so they all share one: the alternative is pretending a page
// changed when the binary did, which Google learns to ignore. Pass the zero
// time to omit it entirely, which is the honest answer when nobody is
// tracking it.
func NewPages(lastMod time.Time, paths ...string) *Pages {
	urls := make([]URL, 0, len(paths))
	for _, path := range paths {
		urls = append(urls, URL{Loc: path, LastMod: lastMod})
	}
	return &Pages{URLs: urls}
}

func (p *Pages) Name() string {
	if p.SourceName != "" {
		return p.SourceName
	}
	return "pages"
}

func (p *Pages) Count(ctx context.Context) (int, error) { return len(p.URLs), nil }

func (p *Pages) Page(ctx context.Context, offset, limit int) ([]URL, error) {
	if offset >= len(p.URLs) {
		return nil, nil
	}
	end := offset + limit
	if end > len(p.URLs) {
		end = len(p.URLs)
	}
	return p.URLs[offset:end], nil
}
