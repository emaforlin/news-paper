package epub

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"fmt"
	"hash/crc32"
	"io"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/emaforlin/news-paper/internal/news"
	"github.com/emaforlin/news-paper/internal/xmlx"
)

// Edition is a one-day's article collection
type Edition struct {
	Title    string         // masthead
	Date     time.Time      // edition date, in reader's local time zone
	Language string         // content language dc:language and default xml:lang, e.g. "en"
	Articles []news.Article // in reading order. 0-99 articles
}

func (e Edition) FullTitle() string {
	return fmt.Sprintf("%s · %s", e.Title, e.Date.Format("2006-01-02"))
}

// FullTitle is what shows in the Kindle library; the date makes each edition
// a distinct document so the newest sorts first in "Recent".
func (e Edition) Filename() string {
	slug := strings.ToLower(strings.Join(strings.Fields(e.Title), "-"))
	return fmt.Sprintf("%s-%s.epub", slug, e.Date.Format("2006-01-02"))
}

// DefaultLanguage replaces an invalid Edition.Language.
const DefaultLanguage = "en"

var langRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// ValidLanguage reports whether s is a well-formed language tag such as "en"
// or "pt-BR". Write falls back to DefaultLanguage when Edition.Language isn't.
func ValidLanguage(s string) bool { return langRe.MatchString(s) }

type chapter struct {
	news.Article
	File    string
	Lang    string
	Minutes int
	Domain  string
}

type view struct {
	Edition
	UUID     string
	ISODate  string
	Modified string
	LongDate string
	TotalMin int
	Chapters []chapter
}

// Write streams the EPUB to w.
func Write(w io.Writer, ed Edition) error {
	if len(ed.Articles) > 99 {
		return fmt.Errorf("too many articles: %d", len(ed.Articles))
	}
	// A malformed dc:language can make Send-to-Kindle reject the whole file.
	if !ValidLanguage(ed.Language) {
		ed.Language = DefaultLanguage
	}

	v := view{
		Edition:  ed,
		UUID:     uuidFor(ed.FullTitle()),
		ISODate:  ed.Date.Format("2006-01-02"),
		Modified: ed.Date.UTC().Format("2006-01-02T15:04:05Z"),
		LongDate: LongDate(ed.Date),
	}
	for i, a := range ed.Articles {
		lang := ed.Language
		if ValidLanguage(a.Language) {
			lang = a.Language
		}
		c := chapter{Article: a, File: fmt.Sprintf("a%02d.xhtml", i+1), Lang: lang,
			Minutes: a.ReadingMinutes(), Domain: a.Domain()}
		v.TotalMin += c.Minutes
		v.Chapters = append(v.Chapters, c)
	}

	zw := zip.NewWriter(w)
	// "mimetype" must be the first entry, stored (uncompressed), with no data
	// descriptor: CreateRaw with precomputed CRC/sizes guarantees all three.
	mt := []byte("application/epub+zip")
	hdr := &zip.FileHeader{Name: "mimetype", Method: zip.Store, Modified: ed.Date,
		CRC32: crc32.ChecksumIEEE(mt), CompressedSize64: uint64(len(mt)), UncompressedSize64: uint64(len(mt))}
	f, err := zw.CreateRaw(hdr)
	if err != nil {
		return err
	}
	if _, err := f.Write(mt); err != nil {
		return err
	}

	files := []struct {
		name string
		tpl  *template.Template
		data any
	}{
		{"META-INF/container.xml", tplContainer, v},
		{"OEBPS/content.opf", tplOPF, v},
		{"OEBPS/toc.ncx", tplNCX, v},
		{"OEBPS/nav.xhtml", tplNav, v},
		{"OEBPS/style.css", tplCSS, v},
		{"OEBPS/front.xhtml", tplFront, v},
	}
	for _, c := range v.Chapters {
		files = append(files, struct {
			name string
			tpl  *template.Template
			data any
		}{"OEBPS/" + c.File, tplChapter, c})
	}
	for _, fl := range files {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: fl.name, Method: zip.Deflate, Modified: ed.Date})
		if err != nil {
			return err
		}
		if err := fl.tpl.Execute(f, fl.data); err != nil {
			return fmt.Errorf("%s: %w", fl.name, err)
		}
	}
	return zw.Close()
}

// Bytes is a convenience wrapper around Write.
func Bytes(ed Edition) ([]byte, error) {
	var buf bytes.Buffer
	err := Write(&buf, ed)
	return buf.Bytes(), err
}

// uuidFor derives a stable name-based UUID (v5 layout) from s, so re-running
// the same day yields the same identifier.
func uuidFor(s string) string {
	h := sha1.Sum([]byte("diario:" + s))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

var (
	diasES  = []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}
	mesesES = []string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio",
		"agosto", "septiembre", "octubre", "noviembre", "diciembre"}
)

// LongDate formats "jueves 26 de noviembre de 2026". The front-page labels are
// Spanish regardless of Edition.Language (which is the *content* language the
// Kindle uses for its dictionary). Go has no locales, so it's done by hand.
func LongDate(t time.Time) string {
	return fmt.Sprintf("%s %d de %s de %d", diasES[t.Weekday()], t.Day(), mesesES[t.Month()-1], t.Year())
}

func fmtMinutes(m int) string {
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %02d min", m/60, m%60)
}

var funcs = template.FuncMap{
	"x":   xmlx.Text,
	"a":   xmlx.Attr,
	"uri": escapeURI, // hrefs to external URLs: {{a (uri .URL)}}
	"min": fmtMinutes,
	"inc": func(i int) int { return i + 2 }, // playOrder: front page is 1
}

func mustTpl(name, src string) *template.Template {
	return template.Must(template.New(name).Funcs(funcs).Parse(src))
}
