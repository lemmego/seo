package seo

import (
	"context"
	"testing"
	"time"
)

// fakeSource is a Source over a fixed slice, paging the way a real one does.
type fakeSource struct {
	name string
	urls []URL
}

func (f *fakeSource) Name() string                       { return f.name }
func (f *fakeSource) Count(context.Context) (int, error) { return len(f.urls), nil }
func (f *fakeSource) Page(_ context.Context, offset, limit int) ([]URL, error) {
	if offset >= len(f.urls) {
		return nil, nil
	}
	end := offset + limit
	if end > len(f.urls) {
		end = len(f.urls)
	}
	return f.urls[offset:end], nil
}

// Everything lands in one file when it fits.
//
// This is the change that matters to a person rather than a crawler: opening
// /sitemap.xml on a site with eighteen URLs used to show nine file names and
// no pages at all, so "is my article in the sitemap?" answered no for
// everybody who looked. Both shapes are valid; only one is auditable.
func TestEverythingCollectsEverySource(t *testing.T) {
	s := &Sitemap{Sources: []Source{
		NewPages(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "https://example.com/", "https://example.com/jobs"),
		&fakeSource{name: "posts", urls: []URL{{Loc: "https://example.com/p/one"}}},
	}}

	set, err := s.everything(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("everything: %v", err)
	}
	if len(set.URLs) != 3 {
		t.Fatalf("got %d urls, want 3: %+v", len(set.URLs), set.URLs)
	}

	seen := map[string]bool{}
	for _, u := range set.URLs {
		seen[u.Loc] = true
	}
	for _, want := range []string{"https://example.com/", "https://example.com/jobs", "https://example.com/p/one"} {
		if !seen[want] {
			t.Errorf("missing %q", want)
		}
	}
}

// Paging is honoured rather than asking a source for everything at once,
// which is the whole reason Source pages in the first place.
func TestEverythingPagesThroughALargeSource(t *testing.T) {
	urls := make([]URL, 0, 25)
	for i := 0; i < 25; i++ {
		urls = append(urls, URL{Loc: "https://example.com/p/" + string(rune('a'+i%26)) + string(rune('0'+i/10))})
	}
	s := &Sitemap{MaxPerFile: 10, Sources: []Source{&fakeSource{name: "posts", urls: urls}}}

	set, err := s.everything(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("everything: %v", err)
	}
	if len(set.URLs) != 25 {
		t.Errorf("got %d urls across three pages, want 25", len(set.URLs))
	}
}

func TestTotalCountsEverySource(t *testing.T) {
	s := &Sitemap{Sources: []Source{
		NewPages(time.Time{}, "https://example.com/", "https://example.com/jobs"),
		&fakeSource{name: "posts", urls: []URL{{Loc: "a"}, {Loc: "b"}, {Loc: "c"}}},
	}}
	total, err := s.total(context.Background())
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
}

// An index entry without lastmod tells a crawler only that a file exists, so
// one holding the previous copy has to guess whether to re-fetch it.
func TestNewestIsTheMostRecentLastModInTheShard(t *testing.T) {
	older := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)
	source := &fakeSource{name: "posts", urls: []URL{
		{Loc: "a", LastMod: older},
		{Loc: "b", LastMod: newer},
		{Loc: "c", LastMod: older},
	}}
	s := &Sitemap{MaxPerFile: 10, Sources: []Source{source}}

	got, err := s.newest(context.Background(), source, 1)
	if err != nil {
		t.Fatalf("newest: %v", err)
	}
	if !got.Equal(newer) {
		t.Errorf("newest = %s, want %s", got, newer)
	}
}

// A source with no LastMod at all yields the zero time, which formatLastMod
// renders as nothing — an absent lastmod rather than a claim about 1970.
func TestNewestIsZeroWhenNothingDeclaresALastMod(t *testing.T) {
	source := &fakeSource{name: "posts", urls: []URL{{Loc: "a"}, {Loc: "b"}}}
	s := &Sitemap{Sources: []Source{source}}

	got, err := s.newest(context.Background(), source, 1)
	if err != nil {
		t.Fatalf("newest: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("newest = %s, want the zero time", got)
	}
	if formatted := formatLastMod(got); formatted != "" {
		t.Errorf("formatLastMod(zero) = %q, want empty", formatted)
	}
}

// Pages is the source a site forgets: every other one is generated from a
// query, and the home page has to be written down.
func TestPagesIsASourceLikeAnyOther(t *testing.T) {
	when := time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)
	pages := NewPages(when, "https://example.com/", "https://example.com/blogs")

	if pages.Name() != "pages" {
		t.Errorf("Name = %q", pages.Name())
	}
	count, _ := pages.Count(context.Background())
	if count != 2 {
		t.Fatalf("Count = %d, want 2", count)
	}
	first, err := pages.Page(context.Background(), 0, 1)
	if err != nil || len(first) != 1 || first[0].Loc != "https://example.com/" {
		t.Fatalf("Page(0,1) = %+v, %v", first, err)
	}
	if !first[0].LastMod.Equal(when) {
		t.Errorf("LastMod = %s, want %s", first[0].LastMod, when)
	}
	if beyond, _ := pages.Page(context.Background(), 9, 1); len(beyond) != 0 {
		t.Errorf("Page past the end returned %+v", beyond)
	}
}
