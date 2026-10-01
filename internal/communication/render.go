package communication

// Channel rendering (issue #166): parse model Markdown once into neutral
// blocks, then render per channel. Model HTML is never executed — every byte
// of model text is escaped by the channel renderer and only our tags go out.

import (
	"html"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	goldtext "github.com/yuin/goldmark/text"
)

const (
	formatMarkdown = "markdown"

	// Telegram counts message length in UTF-16 code units.
	telegramPartLimit = 4000
	slackPartLimit    = 39000
)

var markdownParser = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList),
).Parser()

type span struct {
	text   string
	link   string
	code   bool
	bold   bool
	italic bool
	strike bool
}

type listItem struct {
	depth   int
	num     int
	ordered bool
	spans   []span
}

type block struct {
	kind  string // p, h, quote, code, list, table, hr, html
	lang  string
	spans []span
	lines []string
	items []listItem
	rows  [][][]span
}

func parseBlocks(src string) []block {
	buf := []byte(src)
	doc := markdownParser.Parse(goldtext.NewReader(buf))
	var out []block
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		out = append(out, blocksFrom(buf, n)...)
	}
	return out
}

func blocksFrom(src []byte, n ast.Node) []block {
	switch v := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return []block{{kind: "p", spans: inlineSpans(src, v)}}
	case *ast.Heading:
		return []block{{kind: "h", spans: inlineSpans(src, v)}}
	case *ast.FencedCodeBlock:
		lang := ""
		if v.Info != nil {
			if fields := strings.Fields(string(v.Info.Segment.Value(src))); len(fields) > 0 {
				lang = fields[0]
			}
		}
		return []block{{kind: "code", lang: lang, lines: codeLines(src, v)}}
	case *ast.CodeBlock:
		return []block{{kind: "code", lines: codeLines(src, v)}}
	case *ast.ThematicBreak:
		return []block{{kind: "hr"}}
	case *ast.Blockquote:
		// Flatten nested structure to text lines; Telegram has no nested quotes.
		var lines []string
		for c := v.FirstChild(); c != nil; c = c.NextSibling() {
			for _, inner := range blocksFrom(src, c) {
				lines = append(lines, blockPlainText(inner)...)
			}
		}
		return []block{{kind: "quote", lines: lines}}
	case *ast.List:
		var items []listItem
		collectListItems(src, v, 1, &items)
		return []block{{kind: "list", items: items}}
	case *ast.HTMLBlock:
		return []block{{kind: "html", lines: codeLines(src, v)}}
	case *extast.Table:
		var rows [][][]span
		for r := v.FirstChild(); r != nil; r = r.NextSibling() {
			var row [][]span
			for c := r.FirstChild(); c != nil; c = c.NextSibling() {
				if cell, ok := c.(*extast.TableCell); ok {
					row = append(row, inlineSpans(src, cell))
				}
			}
			if len(row) > 0 {
				rows = append(rows, row)
			}
		}
		return []block{{kind: "table", rows: rows}}
	default:
		return []block{{kind: "p", spans: inlineSpans(src, n)}}
	}
}

func collectListItems(src []byte, list *ast.List, depth int, out *[]listItem) {
	n := 0
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		li, ok := item.(*ast.ListItem)
		if !ok {
			continue
		}
		n++
		it := listItem{depth: depth, ordered: list.IsOrdered()}
		if it.ordered {
			it.num = list.Start + n - 1
		}
		appended := false
		for c := li.FirstChild(); c != nil; c = c.NextSibling() {
			if sub, ok := c.(*ast.List); ok {
				if !appended {
					*out = append(*out, it)
					appended = true
				}
				collectListItems(src, sub, depth+1, out)
				continue
			}
			it.spans = append(it.spans, inlineSpans(src, c)...)
		}
		if !appended && len(it.spans) > 0 {
			*out = append(*out, it)
		}
	}
}

func inlineSpans(src []byte, n ast.Node) []span {
	var out []span
	var walk func(ast.Node, span)
	walk = func(node ast.Node, st span) {
		switch v := node.(type) {
		case *ast.Text:
			t := string(v.Segment.Value(src))
			if v.HardLineBreak() || v.SoftLineBreak() {
				t += "\n"
			}
			st.text = t
			out = append(out, st)
		case *ast.String:
			st.text = string(v.Value)
			out = append(out, st)
		case *ast.CodeSpan:
			st.code = true
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c, st)
			}
		case *ast.Emphasis:
			if v.Level == 2 {
				st.bold = true
			} else {
				st.italic = true
			}
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c, st)
			}
		case *extast.Strikethrough:
			st.strike = true
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c, st)
			}
		case *extast.TaskCheckBox:
			st.text = "☐ "
			if v.IsChecked {
				st.text = "☑ "
			}
			out = append(out, st)
		case *ast.Link:
			if dest := string(v.Destination); safeLink(dest) {
				st.link = dest
			}
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c, st)
			}
		case *ast.AutoLink:
			dest := plainText(src, v)
			st.text = dest
			if safeLink(dest) {
				st.link = dest
			}
			out = append(out, st)
		case *ast.Image:
			alt := plainText(src, v)
			if alt == "" {
				alt = "image"
			}
			st.text = "🖼 " + alt
			if dest := string(v.Destination); safeLink(dest) {
				st.link = dest
			}
			out = append(out, st)
		case *ast.RawHTML:
			var b strings.Builder
			for i := 0; i < v.Segments.Len(); i++ {
				seg := v.Segments.At(i)
				b.Write(seg.Value(src))
			}
			st.text = b.String()
			out = append(out, st)
		default:
			for c := node.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c, st)
			}
		}
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		walk(c, span{})
	}
	return out
}

func plainText(src []byte, n ast.Node) string {
	var b strings.Builder
	for _, s := range inlineSpans(src, n) {
		b.WriteString(s.text)
	}
	return b.String()
}

func blockPlainText(b block) []string {
	switch b.kind {
	case "p", "h":
		var s strings.Builder
		for _, sp := range b.spans {
			s.WriteString(sp.text)
		}
		return strings.Split(s.String(), "\n")
	case "code", "quote", "html":
		return b.lines
	case "list":
		var out []string
		for _, it := range b.items {
			var s strings.Builder
			for _, sp := range it.spans {
				s.WriteString(sp.text)
			}
			out = append(out, s.String())
		}
		return out
	case "table":
		return tableLines(b.rows, func(sp span) string { return sp.text })
	case "hr":
		return []string{"———"}
	}
	return nil
}

func codeLines(src []byte, n interface{ Lines() *goldtext.Segments }) []string {
	var out []string
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		out = append(out, string(seg.Value(src)))
	}
	return out
}

func safeLink(dest string) bool {
	lower := strings.ToLower(strings.TrimSpace(dest))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "mailto:")
}

// rblock is one rendered block: header+body+footer. Wrappers let an oversized
// block split into multiple messages while staying valid (closed) markup.
type rblock struct {
	header string
	body   string
	footer string
}

func renderTelegram(src string) (parts []string, degraded bool) {
	var rb []rblock
	for _, b := range parseBlocks(src) {
		switch b.kind {
		case "h":
			rb = append(rb, rblock{body: "<b>" + tgSpans(b.spans) + "</b>"})
		case "quote":
			rb = append(rb, rblock{header: "<blockquote>", body: html.EscapeString(strings.Join(b.lines, "\n")), footer: "</blockquote>"})
		case "code":
			header := "<pre><code>"
			if b.lang != "" {
				header = `<pre><code class="language-` + html.EscapeString(b.lang) + `">`
			}
			rb = append(rb, rblock{header: header, body: html.EscapeString(strings.Join(b.lines, "")), footer: "</code></pre>"})
		case "list":
			var s strings.Builder
			for i, it := range b.items {
				if i > 0 {
					s.WriteString("\n")
				}
				s.WriteString(strings.Repeat("  ", it.depth-1))
				if it.ordered {
					s.WriteString(strconv.Itoa(it.num) + ". ")
				} else {
					s.WriteString("• ")
				}
				s.WriteString(tgSpans(it.spans))
			}
			rb = append(rb, rblock{body: s.String()})
		case "table":
			degraded = true
			rb = append(rb, rblock{header: "<pre>", body: html.EscapeString(strings.Join(tableLines(b.rows, tgSpanPlain), "\n")), footer: "</pre>"})
		case "hr":
			rb = append(rb, rblock{body: "———"})
		case "html":
			degraded = true
			rb = append(rb, rblock{header: "<pre>", body: html.EscapeString(strings.Join(b.lines, "")), footer: "</pre>"})
		default:
			rb = append(rb, rblock{body: tgSpans(b.spans)})
		}
	}
	return packBlocks(rb, telegramPartLimit, true), degraded
}

func renderSlack(src string) []string {
	var rb []rblock
	for _, b := range parseBlocks(src) {
		switch b.kind {
		case "h":
			rb = append(rb, rblock{body: "*" + slackSpans(b.spans) + "*"})
		case "quote":
			var s strings.Builder
			for i, l := range b.lines {
				if i > 0 {
					s.WriteString("\n")
				}
				s.WriteString("> " + slackEscape(l))
			}
			rb = append(rb, rblock{body: s.String()})
		case "code":
			rb = append(rb, rblock{header: "```" + b.lang + "\n", body: strings.Join(b.lines, ""), footer: "```"})
		case "list":
			var s strings.Builder
			for i, it := range b.items {
				if i > 0 {
					s.WriteString("\n")
				}
				s.WriteString(strings.Repeat("  ", it.depth-1))
				if it.ordered {
					s.WriteString(strconv.Itoa(it.num) + ". ")
				} else {
					s.WriteString("• ")
				}
				s.WriteString(slackSpans(it.spans))
			}
			rb = append(rb, rblock{body: s.String()})
		case "table":
			rb = append(rb, rblock{header: "```\n", body: strings.Join(tableLines(b.rows, slackSpanPlain), "\n"), footer: "```"})
		case "hr":
			rb = append(rb, rblock{body: "———"})
		case "html":
			rb = append(rb, rblock{header: "```\n", body: strings.Join(b.lines, ""), footer: "```"})
		default:
			rb = append(rb, rblock{body: slackSpans(b.spans)})
		}
	}
	return packBlocks(rb, slackPartLimit, false)
}

func tgSpans(spans []span) string {
	var s strings.Builder
	for _, sp := range spans {
		text := html.EscapeString(sp.text)
		if sp.code {
			text = "<code>" + text + "</code>"
		} else {
			if sp.italic {
				text = "<i>" + text + "</i>"
			}
			if sp.bold {
				text = "<b>" + text + "</b>"
			}
			if sp.strike {
				text = "<s>" + text + "</s>"
			}
		}
		if sp.link != "" {
			text = `<a href="` + html.EscapeString(sp.link) + `">` + text + "</a>"
		}
		s.WriteString(text)
	}
	return s.String()
}

func tgSpanPlain(sp span) string { return sp.text }

func slackSpans(spans []span) string {
	var s strings.Builder
	for _, sp := range spans {
		var text string
		if sp.code {
			text = "`" + strings.ReplaceAll(sp.text, "`", "'") + "`"
		} else {
			text = slackEscape(sp.text)
			if sp.italic {
				text = "_" + text + "_"
			}
			if sp.bold {
				text = "*" + text + "*"
			}
			if sp.strike {
				text = "~" + text + "~"
			}
		}
		if sp.link != "" {
			text = "<" + strings.TrimSpace(sp.link) + "|" + text + ">"
		}
		s.WriteString(text)
	}
	return s.String()
}

func slackSpanPlain(sp span) string { return sp.text }

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// tableLines renders a table as a padded monospace grid for <pre>/``` blocks.
func tableLines(rows [][][]span, cellText func(span) string) []string {
	if len(rows) == 0 {
		return nil
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	widths := make([]int, cols)
	texts := make([][]string, len(rows))
	for i, r := range rows {
		texts[i] = make([]string, cols)
		for c := 0; c < cols; c++ {
			if c < len(r) {
				var s strings.Builder
				for _, sp := range r[c] {
					s.WriteString(cellText(sp))
				}
				texts[i][c] = strings.TrimSpace(s.String())
			}
			if w := utf8.RuneCountInString(texts[i][c]); w > widths[c] {
				widths[c] = w
			}
		}
	}
	out := make([]string, 0, len(rows)+1)
	for i, r := range texts {
		var s strings.Builder
		for c, t := range r {
			if c > 0 {
				s.WriteString(" | ")
			}
			s.WriteString(t)
			s.WriteString(strings.Repeat(" ", widths[c]-utf8.RuneCountInString(t)))
		}
		out = append(out, s.String())
		if i == 0 {
			var sep strings.Builder
			for c := range widths {
				if c > 0 {
					sep.WriteString("-+-")
				}
				sep.WriteString(strings.Repeat("-", widths[c]))
			}
			out = append(out, sep.String())
		}
	}
	return out
}

// packBlocks groups rendered blocks into messages under limit, splitting
// oversized blocks at line/space boundaries and re-wrapping each piece.
func packBlocks(rb []rblock, limit int, utf16Mode bool) []string {
	measure := func(s string) int {
		if utf16Mode {
			return len(utf16.Encode([]rune(s)))
		}
		return utf8.RuneCountInString(s)
	}
	var parts []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
			curLen = 0
		}
	}
	for _, b := range rb {
		total := measure(b.header) + measure(b.body) + measure(b.footer)
		if total > limit {
			flush()
			avail := limit - measure(b.header) - measure(b.footer)
			for _, chunk := range splitBody(b.body, avail, measure) {
				parts = append(parts, b.header+chunk+b.footer)
			}
			continue
		}
		need := total
		if curLen > 0 {
			need += measure("\n\n")
		}
		if curLen+need > limit {
			flush()
		}
		if curLen > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(b.header + b.body + b.footer)
		curLen += need
	}
	flush()
	if len(parts) == 0 {
		parts = []string{""}
	}
	return parts
}

func splitBody(body string, avail int, measure func(string) int) []string {
	var out []string
	for measure(body) > avail {
		limit := safeByteCut(body, avail, measure)
		cut := limit
		if idx := strings.LastIndex(body[:limit], "\n"); idx > 0 {
			cut = idx + 1
		} else if idx := strings.LastIndex(body[:limit], " "); idx > 0 {
			cut = idx + 1
		}
		if cut == 0 {
			_, cut = utf8.DecodeRuneInString(body)
		}
		out = append(out, body[:cut])
		body = body[cut:]
	}
	if strings.TrimSpace(body) != "" {
		out = append(out, body)
	}
	return out
}

// safeByteCut returns the largest byte index whose measured length fits avail,
// never splitting inside a rune.
func safeByteCut(s string, avail int, measure func(string) int) int {
	cut := 0
	for i := range s {
		if measure(s[:i]) > avail {
			break
		}
		cut = i
	}
	return cut
}
