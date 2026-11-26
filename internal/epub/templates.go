package epub

// Every interpolated string goes through x (text) or a (attribute), except
// .XHTML, which the sanitizer already emitted as well-formed XHTML. External
// URLs in href also go through uri first: valid XML is not enough, epubcheck
// (and Send-to-Kindle) reject an href that is not a valid URI (RSC-020).

var tplContainer = mustTpl("container", `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`)

var tplOPF = mustTpl("opf", `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid" xml:lang="{{a .Language}}">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="uid">urn:uuid:{{.UUID}}</dc:identifier>
    <dc:title>{{x .FullTitle}}</dc:title>
    <dc:language>{{x .Language}}</dc:language>
    <dc:creator>{{x .Title}}</dc:creator>
    <dc:date>{{.ISODate}}</dc:date>
    <meta property="dcterms:modified">{{.Modified}}</meta>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="css" href="style.css" media-type="text/css"/>
    <item id="front" href="front.xhtml" media-type="application/xhtml+xml"/>
{{- range .Chapters}}
    <item id="{{.File | printf "%.3s"}}" href="{{.File}}" media-type="application/xhtml+xml"/>
{{- end}}
  </manifest>
  <spine toc="ncx">
    <itemref idref="front"/>
{{- range .Chapters}}
    <itemref idref="{{.File | printf "%.3s"}}"/>
{{- end}}
  </spine>
</package>
`)

var tplNCX = mustTpl("ncx", `<?xml version="1.0" encoding="UTF-8"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="urn:uuid:{{.UUID}}"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>
  <docTitle><text>{{x .FullTitle}}</text></docTitle>
  <navMap>
    <navPoint id="np-front" playOrder="1"><navLabel><text>Portada</text></navLabel><content src="front.xhtml"/></navPoint>
{{- range $i, $c := .Chapters}}
    <navPoint id="np-{{$i}}" playOrder="{{inc $i}}"><navLabel><text>{{x $c.Title}}</text></navLabel><content src="{{$c.File}}"/></navPoint>
{{- end}}
  </navMap>
</ncx>
`)

var tplNav = mustTpl("nav", `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="{{a .Language}}" lang="{{a .Language}}">
<head><meta charset="utf-8"/><title>Índice</title></head>
<body>
<nav epub:type="toc" id="toc">
<h1>Índice</h1>
<ol>
<li><a href="front.xhtml">Portada</a></li>
{{- range .Chapters}}
<li><a href="{{.File}}">{{x .Title}}</a></li>
{{- end}}
</ol>
</nav>
</body>
</html>
`)

var tplCSS = mustTpl("css", `body { margin: 0 2%; line-height: 1.4; }
h1 { font-size: 1.5em; line-height: 1.2; margin: 0 0 0.3em 0; }
h2 { font-size: 1.2em; }
h3, h4, h5, h6 { font-size: 1em; }
.masthead { font-size: 2.6em; text-align: center; letter-spacing: 0.05em; margin: 0.8em 0 0.1em 0; }
.date { text-align: center; font-style: italic; margin: 0; }
.stats { text-align: center; font-size: 0.8em; margin: 0.2em 0 1em 0; }
.meta, .src { font-size: 0.8em; color: #444; }
.why { font-size: 0.85em; font-style: italic; margin-top: 0.1em; }
ol.headlines { padding-left: 1.4em; }
ol.headlines li { margin-bottom: 0.9em; }
ol.headlines a { font-weight: bold; text-decoration: none; }
pre { white-space: pre-wrap; font-size: 0.8em; }
blockquote { margin: 0.6em 1.2em; font-style: italic; }
table { border-collapse: collapse; font-size: 0.85em; }
td, th { border: 1px solid #999; padding: 0.2em 0.4em; }
`)

var tplFront = mustTpl("front", `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="{{a .Language}}" lang="{{a .Language}}">
<head><meta charset="utf-8"/><title>{{x .FullTitle}}</title><link rel="stylesheet" type="text/css" href="style.css"/></head>
<body>
<h1 class="masthead">{{x .Title}}</h1>
<p class="date">{{x .LongDate}}</p>
<p class="stats">{{len .Chapters}} artículos · {{min .TotalMin}} de lectura</p>
<hr/>
<ol class="headlines">
{{- range .Chapters}}
<li><a href="{{.File}}">{{x .Title}}</a><br/>
<span class="meta">{{x .Domain}} · {{min .Minutes}}</span>
{{- if .Reason}}<br/><span class="why">{{x .Reason}}</span>{{end}}</li>
{{- end}}
</ol>
</body>
</html>
`)

var tplChapter = mustTpl("chapter", `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="{{a .Lang}}" lang="{{a .Lang}}">
<head><meta charset="utf-8"/><title>{{x .Title}}</title><link rel="stylesheet" type="text/css" href="style.css"/></head>
<body>
<h1>{{x .Title}}</h1>
<p class="meta">
{{- if .Byline}}{{x .Byline}} · {{end -}}
{{- if .SiteName}}{{x .SiteName}} · {{end -}}
{{x .Domain}} · {{min .Minutes}}</p>
{{- if .Reason}}
<p class="why">{{x .Reason}}</p>
{{- end}}
<div class="body">
{{.XHTML}}
</div>
<hr/>
<p class="src">Original: <a href="{{a (uri .URL)}}">{{x .Domain}}</a>
{{- if .Discussion}} · <a href="{{a (uri .Discussion)}}">discusión ({{x .Source}})</a>{{end}}</p>
</body>
</html>
`)
