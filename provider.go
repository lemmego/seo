package seo

import "github.com/lemmego/api/app"

// Provider registers the site-wide defaults and, optionally, the sitemap
// routes.
//
//	&seo.Provider{
//	    Defaults: seo.Defaults{
//	        BaseURL:  config.MustEnv("APP_URL", "http://localhost:8080"),
//	        SiteName: "Productshore",
//	        NoIndex:  config.MustEnv("APP_ENV", "development") != "production",
//	    },
//	    Sitemap: &seo.Sitemap{Sources: []seo.Source{…}},
//	}
type Provider struct {
	Defaults Defaults
	Sitemap  *Sitemap

	// Sources builds the sitemap's sources once the container exists.
	//
	// A Source almost always needs a repository, and a repository needs a
	// connection — neither of which is available where a provider literal is
	// written, because LoadProviders runs before anything is registered. So
	// the sources are built here instead of assigned there.
	//
	// Assign Sitemap.Sources directly only for a source that needs nothing,
	// like a fixed list of static pages.
	Sources func(a app.App) []Source
}

func (p *Provider) Provide(a app.App) error {
	a.AddService(p.Defaults)
	if p.Sitemap != nil && p.Sources != nil {
		p.Sitemap.Sources = append(p.Sitemap.Sources, p.Sources(a)...)
	}
	return nil
}

func (p *Provider) AddRoutes() app.RouteCallback {
	return func(a app.App) {
		if p.Sitemap != nil {
			p.Sitemap.Routes(a)
		}
	}
}
