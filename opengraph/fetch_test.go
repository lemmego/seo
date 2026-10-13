package opengraph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve starts a test server returning the given HTML.
func serve(t *testing.T, html string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	}))
	t.Cleanup(server.Close)
	return server
}

// permissive is the only way to reach an httptest server, which listens on
// loopback — the address the guard exists to refuse.
func permissive() *Fetcher {
	return New(Options{AllowPrivateAddresses: true})
}

// The test that proves the guard is installed rather than merely written: a
// default Fetcher must refuse a loopback server. If Control is ever dropped
// from the dialer, every other test here still passes and this one does not.
func TestTheGuardIsOnByDefault(t *testing.T) {
	server := serve(t, `<html><head><meta property="og:title" content="Secret"></head></html>`)

	_, err := New(Options{}).Fetch(context.Background(), server.URL)
	if err == nil {
		t.Fatal("a default Fetcher reached a loopback address, so the SSRF " +
			"guard is not wired into the dialer")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("it failed with %v rather than reporting a blocked address", err)
	}
}

func TestOpenGraphTagsAreRead(t *testing.T) {
	server := serve(t, `<html><head>
		<meta property="og:title" content="Sidekick">
		<meta property="og:description" content="A small helper that grew up">
		<meta property="og:image" content="https://cdn.example/card.png">
		<meta property="og:site_name" content="Sidekick">
		<meta property="og:type" content="website">
		<title>ignored because og:title wins</title>
	</head><body>…</body></html>`)

	preview, err := permissive().Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetching: %v", err)
	}
	if preview.Title != "Sidekick" {
		t.Errorf("title is %q", preview.Title)
	}
	if preview.Description != "A small helper that grew up" {
		t.Errorf("description is %q", preview.Description)
	}
	if preview.Image != "https://cdn.example/card.png" {
		t.Errorf("image is %q", preview.Image)
	}
	if preview.Type != "website" {
		t.Errorf("type is %q", preview.Type)
	}
	if preview.Empty() {
		t.Error("the preview reports itself empty")
	}
}

// Most of the web has no Open Graph tags at all, so the fallbacks are the
// common path rather than the edge.
func TestItFallsBackToTwitterTagsAndThenToTheTitleElement(t *testing.T) {
	twitter := serve(t, `<html><head>
		<meta name="twitter:title" content="From Twitter">
		<meta name="twitter:image" content="https://cdn.example/t.png">
	</head></html>`)
	preview, err := permissive().Fetch(context.Background(), twitter.URL)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Title != "From Twitter" || preview.Image != "https://cdn.example/t.png" {
		t.Fatalf("got %+v", preview)
	}

	plain := serve(t, `<html><head>
		<title>  Just a page  </title>
		<meta name="description" content="Plain old description">
	</head></html>`)
	preview, err = permissive().Fetch(context.Background(), plain.URL)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Title != "Just a page" {
		t.Fatalf("title is %q, and should be the trimmed <title>", preview.Title)
	}
	if preview.Description != "Plain old description" {
		t.Fatalf("description is %q", preview.Description)
	}
}

// A relative og:image is ordinary. Stored unresolved it would point at our
// own host when the feed renders it.
func TestARelativeImageIsResolvedAgainstThePage(t *testing.T) {
	server := serve(t, `<html><head><meta property="og:image" content="/img/card.png"></head></html>`)

	preview, err := permissive().Fetch(context.Background(), server.URL+"/posts/1")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Image != server.URL+"/img/card.png" {
		t.Fatalf("image is %q, want it resolved against the page", preview.Image)
	}
}

// An attribute that ends up in an img src has to be a scheme a browser will
// treat as an image.
func TestADangerousImageSchemeIsDropped(t *testing.T) {
	for _, reference := range []string{
		"javascript:alert(1)",
		"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"file:///etc/passwd",
	} {
		server := serve(t, `<html><head><meta property="og:image" content="`+reference+`"></head></html>`)
		preview, err := permissive().Fetch(context.Background(), server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if preview.Image != "" {
			t.Errorf("%q survived as %q", reference, preview.Image)
		}
	}
}

func TestANonHTMLResponseIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		fmt.Fprint(w, "PK\x03\x04")
	}))
	t.Cleanup(server.Close)

	if _, err := permissive().Fetch(context.Background(), server.URL); !errors.Is(err, ErrNotHTML) {
		t.Fatalf("got %v, want ErrNotHTML", err)
	}
}

// A URL that streams forever is as effective a denial of service as one that
// reaches somewhere private.
func TestTheBodyIsCapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><meta property="og:title" content="Capped">`)
		// Then never stop.
		filler := strings.Repeat("<!-- padding -->", 1024)
		for i := 0; i < 10_000; i++ {
			if _, err := fmt.Fprint(w, filler); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	fetcher := New(Options{AllowPrivateAddresses: true, MaxBytes: 64 << 10})
	preview, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetching: %v", err)
	}
	// The tag arrived before the cap, so it is still used: a truncated head
	// yields whatever was read.
	if preview.Title != "Capped" {
		t.Fatalf("title is %q", preview.Title)
	}
}

func TestOnlyHTTPAndHTTPSAreFetched(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"gopher://example.com/",
		"ftp://example.com/x",
		"javascript:alert(1)",
	} {
		if _, err := permissive().Fetch(context.Background(), raw); !errors.Is(err, ErrBadURL) {
			t.Errorf("%q produced %v, want ErrBadURL", raw, err)
		}
	}
}

// A public URL that redirects somewhere private is the ordinary shape of this
// attack, so the hop is checked rather than only the URL that was submitted.
func TestARedirectToABlockedAddressIsRefused(t *testing.T) {
	target := serve(t, `<html><head><title>Internal</title></head></html>`)

	// The guard is on, so the redirect's destination — loopback — is refused
	// when that hop dials. The first hop has to be reachable for the test to
	// reach the redirect at all, which is what AllowPrivateAddresses would
	// break, so this exercises the dial-time check directly instead.
	fetcher := New(Options{})
	if err := fetcher.control("tcp", strings.TrimPrefix(target.URL, "http://"), nil); err == nil {
		t.Fatal("the dialer's check allowed a loopback destination")
	}

	// And the redirect checker refuses a scheme change on any hop.
	req, _ := http.NewRequest(http.MethodGet, "file:///etc/passwd", nil)
	if err := fetcher.checkRedirect(req, nil); !errors.Is(err, ErrBadURL) {
		t.Fatalf("a redirect to file:// produced %v", err)
	}
}

func TestARedirectLoopStops(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/again", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	_, err := permissive().Fetch(context.Background(), server.URL)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("got %v, want ErrTooManyRedirects", err)
	}
}

// The URL that comes back is the one after redirects, which is how a
// shortener is resolved to the thing it points at.
func TestTheFinalURLIsReported(t *testing.T) {
	final := serve(t, `<html><head><meta property="og:title" content="Arrived"></head></html>`)
	shortener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/article", http.StatusMovedPermanently)
	}))
	t.Cleanup(shortener.Close)

	preview, err := permissive().Fetch(context.Background(), shortener.URL+"/abc")
	if err != nil {
		t.Fatal(err)
	}
	if preview.URL != final.URL+"/article" {
		t.Fatalf("reported %q, want the address after the redirect", preview.URL)
	}
	if preview.Title != "Arrived" {
		t.Fatalf("title is %q", preview.Title)
	}
}

// A page with nothing to say is an ordinary page, not a failure — the caller
// shows the URL, which is what it would have shown anyway.
func TestAPageWithNoMetadataIsNotAnError(t *testing.T) {
	server := serve(t, `<html><head></head><body><p>hello</p></body></html>`)

	preview, err := permissive().Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("a bare page produced an error: %v", err)
	}
	if !preview.Empty() {
		t.Fatalf("got %+v, want an empty preview", preview)
	}
}

// x/net/html assumes UTF-8, so a page declaring another encoding yields
// mojibake in the title unless the charset is honoured — which looks like our
// bug rather than theirs.
func TestADeclaredCharsetIsHonoured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
		// 0xE9 is é in ISO-8859-1 and invalid on its own in UTF-8.
		w.Write([]byte("<html><head><title>caf\xe9</title></head></html>"))
	}))
	t.Cleanup(server.Close)

	preview, err := permissive().Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Title != "café" {
		t.Fatalf("title is %q, want %q", preview.Title, "café")
	}
}

// Some documents never close their head, so the start of the body is the
// boundary that actually holds.
func TestParsingStopsAtTheBody(t *testing.T) {
	server := serve(t, `<html><head><meta property="og:title" content="Real">
		<body><meta property="og:title" content="Injected by a comment">`)

	preview, err := permissive().Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Title != "Real" {
		t.Fatalf("title is %q — a tag in the body was read", preview.Title)
	}
}
