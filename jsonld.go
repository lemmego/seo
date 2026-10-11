package seo

import (
	"bytes"
	"encoding/json"
	"html/template"
	"strings"
)

// JSONLD renders one structured-data block.
//
// This package deliberately models no schema.org types. They are numerous,
// they change, and the ones a given application needs are obvious from its
// own domain — a Person for a profile, a SoftwareApplication for a launch.
// Passing your own struct or a map means a new schema type never waits on a
// release here.
//
//	seo.Meta{JSONLD: []any{map[string]any{
//	    "@context": "https://schema.org",
//	    "@type":    "Person",
//	    "name":     user.Name,
//	    "url":      "https://productshore.com/@" + user.Handle,
//	}}}
func JSONLD(value any) template.HTML {
	script, err := jsonLD(value)
	if err != nil {
		return ""
	}
	return script
}

func jsonLD(value any) (template.HTML, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	// A script element's content is not HTML-escaped by the parser, so the
	// only sequence that can break out of it is a literal </script — and in
	// JSON that can only appear inside a string. Escaping the slash keeps the
	// JSON equivalent and the element intact. The same applies to <!-- and
	// to U+2028/U+2029, which are valid in JSON strings and terminate a line
	// in older JavaScript parsers.
	var b bytes.Buffer
	b.WriteString(`<script type="application/ld+json">`)
	b.WriteString(strings.NewReplacer(
		"</", `<\/`,
		"<!--", `<\!--`,
		" ", ` `,
		" ", ` `,
	).Replace(string(encoded)))
	b.WriteString(`</script>`)
	return template.HTML(b.String()), nil
}
