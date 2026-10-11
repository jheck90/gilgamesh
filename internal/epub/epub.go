// Package epub reads the text and metadata of a DRM-free EPUB.
package epub

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Book is the readable content of an EPUB in reading order.
type Book struct {
	Title       string
	Author      string
	Language    string
	Series      string
	SeriesIndex float64 // 0 when the EPUB doesn't say
	Sections    []Section
}

// Section is one spine document, usually a chapter.
type Section struct {
	Href       string
	Title      string // from the table of contents, else the first heading
	Paragraphs []string
}

type container struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

type opfMeta struct {
	Name     string `xml:"name,attr"`
	Content  string `xml:"content,attr"`
	Property string `xml:"property,attr"`
	Refines  string `xml:"refines,attr"`
	ID       string `xml:"id,attr"`
	Value    string `xml:",chardata"`
}

type manifestItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type opf struct {
	Metadata struct {
		Titles    []string  `xml:"title"`
		Creators  []string  `xml:"creator"`
		Languages []string  `xml:"language"`
		Metas     []opfMeta `xml:"meta"`
	} `xml:"metadata"`
	Manifest []manifestItem `xml:"manifest>item"`
	Spine    struct {
		Toc   string `xml:"toc,attr"`
		Items []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

type reader struct {
	files map[string]*zip.File
}

func (r *reader) read(name string) ([]byte, error) {
	f, ok := r.files[name]
	if !ok {
		return nil, fmt.Errorf("epub: missing %s", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// Open parses the EPUB at path.
func Open(p string) (*Book, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	r := &reader{files: map[string]*zip.File{}}
	for _, f := range zr.File {
		r.files[f.Name] = f
	}

	raw, err := r.read("META-INF/container.xml")
	if err != nil {
		return nil, err
	}
	var c container
	if err := xml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("epub: container.xml: %w", err)
	}
	if len(c.Rootfiles) == 0 {
		return nil, errors.New("epub: container.xml lists no package file")
	}
	opfPath := c.Rootfiles[0].FullPath
	raw, err = r.read(opfPath)
	if err != nil {
		return nil, err
	}
	var pkg opf
	if err := xml.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("epub: %s: %w", opfPath, err)
	}

	b := &Book{
		Title:    first(pkg.Metadata.Titles),
		Author:   first(pkg.Metadata.Creators),
		Language: first(pkg.Metadata.Languages),
	}
	b.Series, b.SeriesIndex = seriesOf(pkg.Metadata.Metas)

	base := path.Dir(opfPath)
	items := map[string]manifestItem{}
	for _, it := range pkg.Manifest {
		items[it.ID] = it
	}
	toc := r.tocEntries(base, pkg, items)

	for _, ref := range pkg.Spine.Items {
		if ref.Linear == "no" {
			continue
		}
		it, ok := items[ref.IDRef]
		if !ok || !strings.Contains(it.MediaType, "html") {
			continue
		}
		href, _ := splitHref(base, it.Href)
		raw, err := r.read(href)
		if err != nil {
			return nil, err
		}
		start, anchors := "", map[string]string{}
		for _, e := range toc[href] {
			if e.frag == "" {
				if start == "" {
					start = e.title
				}
			} else if _, ok := anchors[e.frag]; !ok {
				anchors[e.frag] = e.title
			}
		}
		for _, sec := range sections(string(raw), start, anchors) {
			sec.Href = href
			b.Sections = append(b.Sections, sec)
		}
	}
	return b, nil
}

func first(ss []string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// seriesOf reads Calibre's series tags, falling back to EPUB 3 collections.
func seriesOf(metas []opfMeta) (string, float64) {
	var name string
	var idx float64
	for _, m := range metas {
		switch m.Name {
		case "calibre:series":
			name = strings.TrimSpace(m.Content)
		case "calibre:series_index":
			idx, _ = strconv.ParseFloat(strings.TrimSpace(m.Content), 64)
		}
	}
	if name != "" {
		return name, idx
	}
	for _, m := range metas {
		if m.Property != "belongs-to-collection" {
			continue
		}
		isSeries := false
		var pos float64
		for _, r := range metas {
			if r.Refines != "#"+m.ID || m.ID == "" {
				continue
			}
			switch r.Property {
			case "collection-type":
				isSeries = strings.TrimSpace(r.Value) == "series"
			case "group-position":
				pos, _ = strconv.ParseFloat(strings.TrimSpace(r.Value), 64)
			}
		}
		if isSeries {
			return strings.TrimSpace(m.Value), pos
		}
	}
	return "", 0
}

// splitHref resolves href against dir into a file path inside the zip and
// a fragment.
func splitHref(dir, href string) (file, frag string) {
	if i := strings.IndexByte(href, '#'); i >= 0 {
		href, frag = href[:i], href[i+1:]
	}
	if u, err := url.PathUnescape(href); err == nil {
		href = u
	}
	return path.Clean(path.Join(dir, href)), frag
}

type tocEntry struct {
	frag  string
	title string
}

// tocEntries lists, per content document, the table-of-contents entries
// that point into it. A file often holds several chapters (or a part title
// and its first chapter), told apart by fragment. EPUB 3 nav wins over the
// EPUB 2 NCX.
func (r *reader) tocEntries(base string, pkg opf, items map[string]manifestItem) map[string][]tocEntry {
	out := map[string][]tocEntry{}
	add := func(dir, href, title string) {
		title = strings.Join(strings.Fields(title), " ")
		if href == "" || title == "" {
			return
		}
		file, frag := splitHref(dir, href)
		out[file] = append(out[file], tocEntry{frag: frag, title: title})
	}
	for _, it := range pkg.Manifest {
		if !strings.Contains(" "+it.Properties+" ", " nav ") {
			continue
		}
		p, _ := splitHref(base, it.Href)
		raw, err := r.read(p)
		if err != nil {
			break
		}
		for _, l := range navLinks(string(raw)) {
			add(path.Dir(p), l[0], l[1])
		}
		return out
	}
	if it, ok := items[pkg.Spine.Toc]; ok {
		p, _ := splitHref(base, it.Href)
		raw, err := r.read(p)
		if err != nil {
			return out
		}
		var doc ncx
		if xml.Unmarshal(raw, &doc) == nil {
			var walk func([]navPoint)
			walk = func(ps []navPoint) {
				for _, np := range ps {
					add(path.Dir(p), np.Content.Src, np.Label)
					walk(np.Points)
				}
			}
			walk(doc.Points)
		}
	}
	return out
}

type navPoint struct {
	Label   string `xml:"navLabel>text"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Points []navPoint `xml:"navPoint"`
}

type ncx struct {
	Points []navPoint `xml:"navMap>navPoint"`
}

// navLinks returns [href, text] pairs from the toc <nav> of an EPUB 3
// navigation document, or from the first <nav> if none is marked toc.
func navLinks(doc string) [][2]string {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return nil
	}
	var navs []*html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "nav" {
			navs = append(navs, n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(root)
	if len(navs) == 0 {
		return nil
	}
	nav := navs[0]
	for _, n := range navs {
		if attr(n, "epub:type") == "toc" {
			nav = n
			break
		}
	}
	var links [][2]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			links = append(links, [2]string{attr(n, "href"), textOf(n)})
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(nav)
	return links
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		k := a.Key
		if a.Namespace != "" {
			k = a.Namespace + ":" + a.Key
		}
		if k == key || a.Key == key {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

var blockTags = map[string]bool{
	"p": true, "div": true, "li": true, "blockquote": true, "pre": true,
	"section": true, "article": true, "header": true, "footer": true,
	"aside": true, "td": true, "th": true, "tr": true, "dt": true, "dd": true,
	"figcaption": true, "ul": true, "ol": true, "table": true, "hr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

var skipTags = map[string]bool{"head": true, "script": true, "style": true, "svg": true}

var cleaner = strings.NewReplacer(" ", " ", "­", "", "​", "")

// sections flattens an XHTML document into paragraphs of plain text,
// starting a new section at each element whose id is a table-of-contents
// anchor. Text before the first anchor gets startTitle. A section without
// a title takes its first heading.
func sections(doc, startTitle string, anchors map[string]string) []Section {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return nil
	}
	out := []Section{{Title: startTitle}}
	headings := []string{""}
	var buf strings.Builder
	flush := func(isHeading bool) {
		s := strings.Join(strings.Fields(cleaner.Replace(buf.String())), " ")
		buf.Reset()
		if s == "" {
			return
		}
		i := len(out) - 1
		if isHeading && headings[i] == "" {
			headings[i] = s
		}
		out[i].Paragraphs = append(out[i].Paragraphs, s)
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			buf.WriteString(n.Data)
			return
		case html.ElementNode:
			if skipTags[n.Data] {
				return
			}
			if title, ok := anchors[attr(n, "id")]; ok && attr(n, "id") != "" {
				flush(false)
				out = append(out, Section{Title: title})
				headings = append(headings, "")
			}
			if n.Data == "br" {
				flush(false)
				return
			}
		}
		block := n.Type == html.ElementNode && blockTags[n.Data]
		if block {
			flush(false)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			flush(len(n.Data) == 2 && n.Data[0] == 'h' && n.Data[1] >= '1' && n.Data[1] <= '6')
		}
	}
	walk(root)
	flush(false)

	var kept []Section
	for i, sec := range out {
		if len(sec.Paragraphs) == 0 {
			continue
		}
		if sec.Title == "" {
			sec.Title = headings[i]
		}
		kept = append(kept, sec)
	}
	return kept
}
