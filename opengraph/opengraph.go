// Package opengraph reads the Open Graph metadata off somebody else's page,
// so a submitted link can be shown as a card rather than as a URL.
//
// It is the other direction of the same specification the rest of this module
// writes: [seo.Meta] emits og:title and og:image for your pages, and this
// reads them from theirs.
//
// # This is an SSRF surface, and that is the whole design
//
// Fetching a URL somebody typed means making a request from inside your
// network to a destination they chose. The guards are therefore not optional
// settings:
//
//   - **The address is checked at dial time, not before.** A name is not a
//     destination. Resolving a hostname, checking the result and then handing
//     the name to a dialer leaves a window in which the dialer's own lookup
//     returns something else — a DNS rebind. [net.Dialer.Control] runs after
//     resolution and before connect, on the address the socket will use, so
//     there is no second lookup to race.
//   - **Every redirect hop is checked**, because each one dials again. A
//     public URL that redirects to http://169.254.169.254 is the ordinary
//     shape of this attack, and that address returns cloud credentials.
//   - **Only http and https**, so file://, gopher:// and the rest are gone.
//   - **A byte cap, a timeout and a redirect cap**, because a URL that
//     streams forever is as effective as one that reaches somewhere private.
//
// # What it does not do
//
// It does not render JavaScript, so a page that writes its own meta tags from
// a script unfurls to whatever is in the served HTML — which is what every
// other unfurler sees too, and why sites that care ship the tags server-side.
package opengraph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Preview is what a link unfurls to. Every field may be empty: a page that
// carries no metadata is a normal page, not a failure.
type Preview struct {
	// URL is the address the content was finally served from, after
	// redirects — not the one that was asked for. Store this one: it is what
	// a reader will reach, and the difference is how a shortener is resolved.
	URL string

	Title       string
	Description string

	// Image is absolute, resolved against the page it was found on, and is
	// http or https. A relative og:image is common and a javascript: one is
	// not unheard of.
	Image string

	SiteName string

	// Type is the Open Graph type: "website", "article", "video.movie".
	Type string
}

// Empty reports whether nothing worth showing was found.
func (p Preview) Empty() bool {
	return p.Title == "" && p.Description == "" && p.Image == "" && p.SiteName == ""
}

var (
	// ErrBlockedAddress means the URL resolved somewhere a fetch must not go.
	ErrBlockedAddress = errors.New("opengraph: the address is not allowed")

	// ErrBadURL means the URL could not be used at all.
	ErrBadURL = errors.New("opengraph: the URL cannot be fetched")

	// ErrNotHTML means the response was something this cannot read.
	ErrNotHTML = errors.New("opengraph: the response is not HTML")

	// ErrTooManyRedirects means the chain did not end.
	ErrTooManyRedirects = errors.New("opengraph: too many redirects")
)

// Defaults, each sized for a page's <head> rather than for a page.
const (
	// DefaultMaxBytes is 512KB. The metadata is in the head, and a head that
	// has not finished inside half a megabyte is not going to produce a
	// usable card.
	DefaultMaxBytes = 512 << 10

	// DefaultTimeout bounds the whole fetch, redirects included.
	DefaultTimeout = 8 * time.Second

	// DefaultMaxRedirects is what a shortener chain reasonably needs.
	DefaultMaxRedirects = 5
)

// Options configures a [Fetcher].
type Options struct {
	Timeout      time.Duration
	MaxBytes     int64
	MaxRedirects int

	// UserAgent identifies the fetch. Many sites serve different metadata to
	// a crawler than to a browser, and an honest agent string is how a site
	// owner can tell who is asking.
	UserAgent string

	// AllowPrivateAddresses disables the address guard entirely.
	//
	// It exists so this package's own tests can reach an httptest server,
	// which listens on loopback. Setting it in a deployment turns every
	// submitted link into a request your network will make on a stranger's
	// behalf — to your database, your metadata endpoint, your admin panel.
	// There is deliberately no way to allow one address.
	AllowPrivateAddresses bool
}

// Fetcher unfurls links. Safe for concurrent use.
type Fetcher struct {
	client       *http.Client
	maxBytes     int64
	maxRedirects int
	userAgent    string
	allowPrivate bool
}

// New builds a Fetcher.
func New(opts Options) *Fetcher {
	fetcher := &Fetcher{
		maxBytes:     opts.MaxBytes,
		maxRedirects: opts.MaxRedirects,
		userAgent:    opts.UserAgent,
		allowPrivate: opts.AllowPrivateAddresses,
	}
	if fetcher.maxBytes <= 0 {
		fetcher.maxBytes = DefaultMaxBytes
	}
	if fetcher.maxRedirects <= 0 {
		fetcher.maxRedirects = DefaultMaxRedirects
	}
	if fetcher.userAgent == "" {
		fetcher.userAgent = "lemmego-opengraph/1 (+link preview)"
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	dialer := &net.Dialer{Timeout: timeout, Control: fetcher.control}
	fetcher.client = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			// No connection reuse across fetches. A pooled connection was
			// checked when it was dialled, and the next fetch to the same
			// host would skip Control entirely — which is the guard not
			// running on a request it was meant to cover.
			DisableKeepAlives: true,
			Proxy:             nil,
		},
		CheckRedirect: fetcher.checkRedirect,
	}
	return fetcher
}

func (f *Fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= f.maxRedirects {
		return fmt.Errorf("%w: stopped after %d", ErrTooManyRedirects, len(via))
	}
	// The scheme is re-checked on every hop, because the allowlist on the
	// first URL says nothing about where it points.
	if err := allowedScheme(req.URL); err != nil {
		return err
	}
	// The address itself is checked by Control when this hop dials.
	return nil
}

func allowedScheme(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("%w: %q is not an http or https URL", ErrBadURL, u.Scheme)
	}
}

// Fetch reads the page and returns what it says about itself.
//
// A page with no metadata returns an empty [Preview] and no error, because
// that is an ordinary page rather than a failure — the caller shows the URL,
// which is what it would have shown anyway.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Preview, error) {
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Preview{}, fmt.Errorf("%w: %v", ErrBadURL, err)
	}
	if err := allowedScheme(target); err != nil {
		return Preview{}, err
	}
	if target.Host == "" {
		return Preview{}, fmt.Errorf("%w: it names no host", ErrBadURL)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Preview{}, fmt.Errorf("%w: %v", ErrBadURL, err)
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	// Identity, so the byte cap below counts the bytes that will be parsed
	// rather than the bytes on the wire — a compressed response can expand
	// past the cap after it has been read.
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := f.client.Do(req)
	if err != nil {
		return Preview{}, unwrapFetchError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Preview{}, fmt.Errorf("opengraph: %s answered %d", target.Host, resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !isHTML(contentType) {
		return Preview{}, fmt.Errorf("%w: the server sent %q", ErrNotHTML, contentType)
	}

	// One byte past the cap, so hitting it exactly is not mistaken for
	// overflowing it. A truncated head still parses: the tags that arrived
	// are the tags that are used.
	body := io.LimitReader(resp.Body, f.maxBytes+1)

	preview, err := parse(body, resp.Request.URL, contentType)
	if err != nil {
		return Preview{}, err
	}
	// resp.Request is the last request made, so this is the URL after every
	// redirect — the address a reader will actually reach.
	preview.URL = resp.Request.URL.String()
	return preview, nil
}

// unwrapFetchError digs the real reason out of a url.Error, so a blocked
// address reports itself as one rather than as a dial failure.
func unwrapFetchError(err error) error {
	for _, sentinel := range []error{ErrBlockedAddress, ErrTooManyRedirects, ErrBadURL} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	return fmt.Errorf("opengraph: fetching: %w", err)
}

func isHTML(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	switch strings.ToLower(strings.TrimSpace(media)) {
	case "text/html", "application/xhtml+xml":
		return true
	case "":
		// No Content-Type at all. Parsing is harmless — the worst case is an
		// empty Preview — and refusing would drop pages from servers that
		// simply do not send one.
		return true
	}
	return false
}
