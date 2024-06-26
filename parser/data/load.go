package parser_data

import (
	"bytes"
	"embed"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/anaskhan96/soup"
	"github.com/metal3d/go-slugify"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	html2markdown "github.com/evorts/html-to-markdown"
)

var html2md = html2markdown.NewConverter("wiki.hyprlang.org", true, &html2markdown.Options{})
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

func debug(msg string, fmtArgs ...any) {
	// fmt.Fprintf(os.Stderr, msg, fmtArgs...)
}

//go:embed sources/Variables.md
var documentationSource []byte

//go:embed sources/Master-Layout.md
var masterLayoutDocumentationSource []byte

//go:embed sources/Dwindle-Layout.md
var dwindleLayoutDocumentationSource []byte

//go:embed sources/*.md
var documentationSources embed.FS

var Sections = []SectionDefinition{}

var undocumentedGeneralSectionVariables = []VariableDefinition{
	{
		Name:        "autogenerated",
		Description: "Whether this configuration was autogenerated",
		Type:        "bool",
		Default:     "1",
	},
}

func (s SectionDefinition) VariableDefinition(name string) *VariableDefinition {
	for _, v := range s.Variables {
		if v.Name == name {
			return &v
		}
	}
	return nil
}

func init() {
	html2md.AddRules(html2markdown.Rule{
		Filter: []string{"a"},
		Replacement: func(content string, selec *goquery.Selection, options *html2markdown.Options) *string {
			href, _ := selec.Attr("href")
			if strings.HasPrefix(href, "../") {
				href = strings.Replace(href, "../", "https://wiki.hyprland.org/Configuring/", 1)
			}
			result := fmt.Sprintf("[%s](%s)", content, href)
			return html2markdown.String(result)
		},
	})

	Sections = parseDocumentationMarkdown(documentationSource, 3)
	Sections = append(Sections, parseDocumentationMarkdownWithRootSectionName(masterLayoutDocumentationSource, 2, "Master")...)
	Sections = append(Sections, parseDocumentationMarkdownWithRootSectionName(dwindleLayoutDocumentationSource, 2, "Dwindle")...)
	addVariableDefsOnSection("General", undocumentedGeneralSectionVariables)

	for i, kw := range Keywords {
		if kw.Description != "" {
			continue
		}

		content, err := documentationSources.ReadFile(filepath.Join("sources", kw.documentationFile+".md"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to read documentation file for %s: %s\n", kw.Name, err)
			continue
		}

		document := markdownToHTML(content)
		headings := make([]soup.Root, 0)
		for _, t := range []string{"h1", "h2", "h3", "h4", "h5", "h6"} {
			headings = append(headings, document.FindAll(t)...)
		}
		var heading soup.Root
		found := false
		for _, h := range headings {
			if id, ok := h.Attrs()["id"]; ok && id == kw.documentationHeadingSlug {
				heading = h
				found = true
				break
			}
			anchor := slugify.Marshal(strings.TrimSpace(h.Text()), true)
			anchor = regexp.MustCompile(`^weight-%d+-title-`).ReplaceAllString(anchor, "")
			if anchor == kw.documentationHeadingSlug {
				heading = h
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "Failed to find heading %s in %s\n", kw.documentationHeadingSlug, kw.documentationFile)
			continue
		}
		Keywords[i].Description, _ = html2md.ConvertString(htmlBetweenHeadingAndNextHeading(heading, heading))
	}
}

func addVariableDefsOnSection(sectionName string, variables []VariableDefinition) {
	for i, sec := range Sections {
		if sec.Name() != sectionName {
			continue
		}
		Sections[i].Variables = append(Sections[i].Variables, variables...)
	}
}

func parseDocumentationMarkdownWithRootSectionName(source []byte, headingRootLevel int, rootSectionName string) []SectionDefinition {
	sections := parseDocumentationMarkdown(source, headingRootLevel)
	for i := range sections {
		sections[i].Path[0] = rootSectionName
	}
	return sections
}

func markdownToHTML(source []byte) soup.Root {
	var html bytes.Buffer
	err := md.Convert(source, &html)
	if err != nil {
		panic(err)
	}

	return soup.HTMLParse(html.String())
}

func parseDocumentationMarkdown(source []byte, headingRootLevel int) (sections []SectionDefinition) {
	document := markdownToHTML(source)
	for _, table := range document.FindAll("table") {
		if !arraysEqual(tableHeaderCells(table), []string{"name", "description", "type", "default"}) {
			continue
		}

		// fmt.Printf("Processing table %s\n", table.HTML())
		section := SectionDefinition{
			Path: tablePath(table, headingRootLevel),
		}
		section.Variables = make([]VariableDefinition, 0)
		for _, row := range table.FindAll("tr")[1:] {
			cells := row.FindAll("td")
			if len(cells) != 4 {
				continue
			}

			section.Variables = append(section.Variables, VariableDefinition{
				Name:        cells[0].FullText(),
				Description: cells[1].FullText(),
				Type:        cells[2].FullText(),
				Default:     cells[3].FullText()})
		}
		sections = append(sections, section)
	}

	for i, section := range sections {
		if len(section.Path) == 1 {
			sections[i] = section.AttachSubsections(sections)
		}
	}
	return sections
}

func (s SectionDefinition) AttachSubsections(sections []SectionDefinition) SectionDefinition {
	// TODO make it work for recursively nested sections
	s.Subsections = make([]SectionDefinition, 0)
	for _, section := range sections {
		if len(section.Path) == 1 {
			continue
		}
		if section.Path[0] == s.Name() {
			debug("adding %s to %s\n", section.Name(), s.Name())
			s.Subsections = append(s.Subsections, section)
		}
	}
	return s
}

func tableHeaderCells(table soup.Root) []string {
	headerCells := table.FindAll("th")
	cells := make([]string, 0, len(headerCells))
	for _, cell := range headerCells {
		cells = append(cells, cell.FullText())
	}
	return cells
}

func tablePath(table soup.Root, headingRootLevel int) []string {
	header := backtrackToNearestHeader(table)
	level, err := strconv.Atoi(header.NodeValue[1:])
	if err != nil {
		panic(err)
	}
	if level <= headingRootLevel {
		return []string{header.FullText()}
	}
	return append(tablePath(header.FindPrevElementSibling(), headingRootLevel), header.FullText())
}

func backtrackToNearestHeader(element soup.Root) soup.Root {
	if element.NodeValue != "table" {
		debug("backtracking to nearest header from %s\n", element.HTML())
	}
	if regexp.MustCompile(`^h[1-6]$`).MatchString(element.NodeValue) {
		debug("-> returning from backtrack with %s\n", element.HTML())
		return element
	}
	prev := element.FindPrevElementSibling()
	debug("-> prev is %s\n", prev.HTML())
	return backtrackToNearestHeader(prev)
}

func htmlBetweenHeadingAndNextHeading(heading soup.Root, element soup.Root) string {
	next := element.FindNextElementSibling()
	if isHeading(next) && headingLevel(next) == headingLevel(heading) {
		return ""
	}

	defer func() {
		if crash := recover(); crash != nil {
			if os.Getenv("DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "Panic while rendering %s\n", next.HTML())
			}
		}
	}()

	rendered := next.HTML()

	return rendered + htmlBetweenHeadingAndNextHeading(heading, next)
}

func isHeading(element soup.Root) bool {
	return regexp.MustCompile(`^h[1-6]$`).MatchString(element.NodeValue)
}

func headingLevel(heading soup.Root) int {
	level, err := strconv.Atoi(heading.NodeValue[1:])
	if err != nil {
		panic(err)
	}
	return level
}

func arraysEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if strings.TrimSpace(v) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}

func toPascalCase(s string) string {
	out := ""
	for _, word := range regexp.MustCompile(`[-_\.]`).Split(s, -1) {
		out += strings.ToUpper(word[:1]) + word[1:]
	}
	return out
}
