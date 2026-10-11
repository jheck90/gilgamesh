// Package epubtest writes small EPUB files for tests.
package epubtest

import (
	"archive/zip"
	"fmt"
	"html"
	"os"
	"strings"
)

type Chapter struct {
	Title string
	Paras []string
	// SameFile puts this chapter in the previous chapter's XHTML file,
	// reached by a fragment link, the way a part title and its first
	// chapter often share a file.
	SameFile bool
}

type Book struct {
	Title, Author, Series, SeriesIndex string
	Chapters                           []Chapter
}

// Write builds an EPUB 3 with a nav document, Calibre series tags and one
// XHTML file per chapter.
func Write(path string, b Book) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	z := zip.NewWriter(f)
	put := func(name, body string) error {
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(body))
		return err
	}
	if err := put("mimetype", "application/epub+zip"); err != nil {
		return err
	}
	if err := put("META-INF/container.xml", `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`); err != nil {
		return err
	}

	var manifest, spine, nav strings.Builder
	var bodies []*strings.Builder
	for i, c := range b.Chapters {
		if !c.SameFile || len(bodies) == 0 {
			n := len(bodies) + 1
			id := fmt.Sprintf("f%d", n)
			fmt.Fprintf(&manifest, `<item id="%s" href="Text/chapter%%20%d.xhtml" media-type="application/xhtml+xml"/>`+"\n", id, n)
			fmt.Fprintf(&spine, `<itemref idref="%s"/>`+"\n", id)
			bodies = append(bodies, &strings.Builder{})
		}
		file := fmt.Sprintf("Text/chapter%%20%d.xhtml", len(bodies)) // escaped space on purpose
		anchor := fmt.Sprintf("c%d", i+1)
		fmt.Fprintf(&nav, `<li><a href="%s#%s">%s</a></li>`+"\n", file, anchor, html.EscapeString(c.Title))
		body := bodies[len(bodies)-1]
		fmt.Fprintf(body, "<h1 id=\"%s\">%s</h1>\n", anchor, html.EscapeString(c.Title))
		for _, p := range c.Paras {
			fmt.Fprintf(body, "<p>%s</p>\n", html.EscapeString(p))
		}
	}
	for i, body := range bodies {
		doc := `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>x</title><style>p{}</style></head>
<body>` + body.String() + `</body></html>`
		if err := put(fmt.Sprintf("OEBPS/Text/chapter %d.xhtml", i+1), doc); err != nil {
			return err
		}
	}
	if err := put("OEBPS/nav.xhtml", `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body>
<nav epub:type="landmarks"><ol><li><a href="Text/chapter%201.xhtml">Begin Reading</a></li></ol></nav>
<nav epub:type="toc"><ol>`+nav.String()+`</ol></nav></body></html>`); err != nil {
		return err
	}
	opf := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:title>` + html.EscapeString(b.Title) + `</dc:title>
  <dc:creator>` + html.EscapeString(b.Author) + `</dc:creator>
  <dc:language>en</dc:language>`
	if b.Series != "" {
		opf += `
  <meta name="calibre:series" content="` + html.EscapeString(b.Series) + `"/>
  <meta name="calibre:series_index" content="` + b.SeriesIndex + `"/>`
	}
	opf += `
</metadata>
<manifest>
<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
` + manifest.String() + `</manifest>
<spine>
` + spine.String() + `</spine>
</package>`
	if err := put("OEBPS/content.opf", opf); err != nil {
		return err
	}
	return z.Close()
}
