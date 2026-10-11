// Package seo supplies the parts of a public page that only a crawler reads:
// per-page metadata, structured data, a sitemap, robots.txt, and slugs.
//
// The metadata is rendered in Go and injected into the root template, not
// emitted by a page component. That is deliberate and it is the whole design:
// an Inertia application falls back to the client-rendered container on any
// SSR error, and on that path the head Inertia would have produced is empty.
// So metadata that came from the page component disappears exactly when the
// SSR process is down — while the page still answers 200 and still looks
// right to a person. Rendering it in Go makes it independent of that process.
package seo

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Slugify turns a title into a URL segment.
//
// It folds to ASCII where it can, lowercases, collapses every run of
// non-alphanumerics to a single hyphen, and trims hyphens from both ends. A
// max of 0 means no limit; otherwise it truncates on a hyphen boundary rather
// than mid-word, because a slug cut mid-word reads like a typo.
//
// It can return the empty string — a title of "日本語" has nothing ASCII in
// it. Callers must handle that rather than assuming a non-empty result, which
// is why UniqueSlug takes a fallback.
func Slugify(s string, max int) string {
	var b strings.Builder
	b.Grow(len(s))

	lastHyphen := false
	for _, r := range strings.ToLower(s) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		// An accent folds to its base letter with no separator: é is part of
		// the word, not a boundary.
		if folded, ok := fold[r]; ok {
			b.WriteString(folded)
			lastHyphen = false
			continue
		}
		// A word substitution is its own word, so it needs a boundary on
		// each side. Without one, "C++" folds to "cplusplus" and
		// "Symbols!@#" to "symbols-atsharp".
		if word, ok := foldWord[r]; ok {
			if b.Len() > 0 && !lastHyphen {
				b.WriteByte('-')
			}
			b.WriteString(word)
			lastHyphen = false
			continue
		}
		// Anything else is a separator, collapsed to one hyphen and never
		// leading.
		if !lastHyphen && b.Len() > 0 {
			b.WriteByte('-')
			lastHyphen = true
		}
	}

	slug := strings.Trim(b.String(), "-")
	if max > 0 && len(slug) > max {
		slug = slug[:max]
		// Prefer cutting at the last hyphen, so the slug ends on a whole
		// word. If there is no hyphen at all, a hard cut is all there is.
		if idx := strings.LastIndexByte(slug, '-'); idx > 0 {
			slug = slug[:idx]
		}
		slug = strings.Trim(slug, "-")
	}
	return slug
}

// fold maps the accented Latin letters people actually put in product and
// personal names onto ASCII. It is deliberately not a full Unicode
// normalisation: that needs golang.org/x/text, and this module has no
// dependencies on purpose — a slug helper is not worth a dependency tree.
var fold = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'æ': "ae",
	'ç': "c", 'ć': "c", 'č': "c", 'ĉ': "c",
	'ď': "d", 'đ': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ĕ': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ĝ': "g", 'ğ': "g", 'ģ': "g",
	'ĥ': "h",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ĩ': "i", 'ī': "i", 'į': "i", 'ı': "i",
	'ĵ': "j",
	'ķ': "k",
	'ĺ': "l", 'ļ': "l", 'ł': "l",
	'ñ': "n", 'ń': "n", 'ň': "n", 'ņ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'ŏ': "o", 'ő': "o",
	'œ': "oe",
	'ŕ': "r", 'ř': "r",
	'ś': "s", 'š': "s", 'ş': "s", 'ș': "s",
	'ß': "ss",
	'ţ': "t", 'ť': "t", 'ț': "t",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ũ': "u", 'ū': "u", 'ŭ': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ŵ': "w",
	'ý': "y", 'ÿ': "y", 'ŷ': "y",
	'ź': "z", 'ż': "z", 'ž': "z",
	'þ': "th", 'ð': "d",
}

// foldWord maps characters people type and read as words. Unlike an accent,
// each becomes its own hyphen-separated word, so "C++ and Go" is
// "c-plus-plus-and-go" rather than "cplusplus-and-go".
var foldWord = map[rune]string{
	'&': "and",
	'+': "plus",
	'@': "at",
	'#': "sharp",
}

// UniqueSlug appends -2, -3 … until taken reports the candidate free.
//
// fallback is used when Slugify returns nothing, which happens for a title
// with no ASCII-foldable characters at all. Without it such a title produces
// an empty slug and a URL that collides with its own collection.
//
// This is a nicety, not a guarantee. Two concurrent requests can both find
// "my-app" free, so the column still needs a unique index and the caller
// still needs to handle the violation. What this buys is that the common case
// produces a readable slug instead of a retry loop.
func UniqueSlug(ctx context.Context, title, fallback string, max int, taken func(context.Context, string) (bool, error)) (string, error) {
	base := Slugify(title, max)
	if base == "" {
		base = Slugify(fallback, max)
	}
	if base == "" {
		return "", ErrNoSlug
	}

	candidate := base
	for attempt := 2; attempt <= maxSlugAttempts; attempt++ {
		used, err := taken(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !used {
			return candidate, nil
		}
		candidate = base + "-" + strconv.Itoa(attempt)
	}
	// Rather than looping forever against a pathological collection, fall
	// back to something certainly unique. It is ugly, and it is better than
	// an unbounded loop holding a request open.
	return base + "-" + strconv.FormatInt(time.Now().UnixNano(), 36), nil
}

// maxSlugAttempts bounds the suffix search. Past this the collection has
// hundreds of identically named rows and a readable slug is no longer
// achievable anyway.
const maxSlugAttempts = 100

// ErrNoSlug means neither the title nor the fallback produced any usable
// characters.
var ErrNoSlug = errors.New("seo: neither the title nor the fallback yields a slug")
