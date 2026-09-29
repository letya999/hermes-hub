package media

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"golang.org/x/image/font/gofont/goregular"
)

func pdfBytes(text string) ([]byte, error) {
	font, cmap, err := pdfFont()
	if err != nil {
		return nil, err
	}
	lines := wrapRunes(text, 80)
	if len(lines) == 0 {
		lines = []string{""}
	}
	const perPage = 40
	var pages [][]string
	for i := 0; i < len(lines); i += perPage {
		j := i + perPage
		if j > len(lines) {
			j = len(lines)
		}
		pages = append(pages, lines[i:j])
	}
	if len(pages) > MaxPages {
		return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	used := map[uint16]rune{}
	glyphs := make([][][]uint16, len(pages))
	for pi, page := range pages {
		glyphs[pi] = make([][]uint16, len(page))
		for li, line := range page {
			for _, r := range line {
				gid := cmap[r]
				shown := r
				if gid == 0 {
					gid = cmap['?']
					shown = '?'
				}
				if gid == 0 {
					continue
				}
				glyphs[pi][li] = append(glyphs[pi][li], gid)
				if _, ok := used[gid]; !ok {
					used[gid] = shown
				}
			}
		}
	}
	doc := &pdfBuilder{}
	fontFile := doc.add(pdfStream(map[string]string{"/Length1": strconv.Itoa(len(font))}, font))
	toUnicode := doc.add(pdfStream(nil, []byte(toUnicodeCMap(used))))
	descriptor := doc.add(pdfDict(fmt.Sprintf("<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontBBox [-400 -300 1400 1100] /ItalicAngle 0 /Ascent 1024 /Descent -256 /CapHeight 700 /StemV 80 /FontFile2 %d 0 R >>", fontFile)))
	cid := doc.add(pdfDict(fmt.Sprintf("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /GoRegular /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /CIDToGIDMap /Identity /DW 500 >>", descriptor)))
	fontObj := doc.add(pdfDict(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /GoRegular /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", cid, toUnicode)))
	var kids []int
	for _, page := range glyphs {
		var content strings.Builder
		content.WriteString("BT\n/F1 12 Tf\n48 740 Td\n")
		for i, line := range page {
			if i > 0 {
				content.WriteString("T*\n")
			}
			content.WriteString("<")
			for _, gid := range line {
				fmt.Fprintf(&content, "%04X", gid)
			}
			content.WriteString("> Tj\n")
		}
		content.WriteString("ET\n")
		contentID := doc.add(pdfStream(nil, []byte(content.String())))
		kids = append(kids, doc.add(nil))
		pageObj := kids[len(kids)-1]
		doc.objs[pageObj-1] = pdfDict(fmt.Sprintf("<< /Type /Page /Parent PAGES 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontObj, contentID))
	}
	kidRefs := make([]string, len(kids))
	for i, id := range kids {
		kidRefs[i] = fmt.Sprintf("%d 0 R", id)
	}
	pagesID := doc.add(pdfDict(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kidRefs, " "), len(kids))))
	for _, id := range kids {
		doc.objs[id-1] = bytes.ReplaceAll(doc.objs[id-1], []byte("PAGES 0 R"), []byte(fmt.Sprintf("%d 0 R", pagesID)))
	}
	catalog := doc.add(pdfDict(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesID)))
	return doc.bytes(catalog), nil
}

func pdfText(body []byte) (string, error) {
	if len(body) < 8 || !bytes.HasPrefix(body, []byte("%PDF-")) {
		return "", errors.New("document is invalid")
	}
	if !bytes.Contains(body, []byte("%%EOF")) && !bytes.Contains(body, []byte("endobj")) {
		return "", errors.New("document is invalid")
	}
	if pdfEncrypted(body) {
		return "", errors.New("pdf is encrypted")
	}
	if n := pdfPageCount(body); n > MaxPages {
		return "", fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	streams, err := pdfStreams(body)
	if err != nil {
		return "", err
	}
	cmap := map[uint16]rune{}
	var b strings.Builder
	for _, stream := range streams {
		if bytes.Contains(stream, []byte("beginbfchar")) || bytes.Contains(stream, []byte("beginbfrange")) {
			parseToUnicode(stream, cmap)
		}
	}
	for _, stream := range streams {
		if len(stream) >= 4 {
			sig := stream[:4]
			if bytes.Equal(sig, []byte{0x00, 0x01, 0x00, 0x00}) || bytes.Equal(sig, []byte("true")) || bytes.Equal(sig, []byte("OTTO")) || bytes.Contains(stream, []byte("beginbfchar")) {
				continue
			}
		}
		if err := appendPDFText(&b, stream, cmap); err != nil {
			return "", err
		}
		if b.Len() > MaxDocumentBytes {
			return "", fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
		}
	}
	text := strings.TrimRight(b.String(), "\n")
	if pageCount(text) > MaxPages {
		return "", fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	return text, nil
}

func wrapRunes(text string, width int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		runes := []rune(line)
		if len(runes) == 0 {
			lines = append(lines, "")
			continue
		}
		for len(runes) > width {
			lines = append(lines, string(runes[:width]))
			runes = runes[width:]
		}
		lines = append(lines, string(runes))
	}
	return lines
}

func findPDFStream(body []byte, from int) int {
	for from < len(body) {
		rel := bytes.Index(body[from:], []byte("stream"))
		if rel < 0 {
			return -1
		}
		at := from + rel
		if at == 0 || pdfDelim(body[at-1]) {
			return at
		}
		from = at + 1
	}
	return -1
}

func pdfEncrypted(body []byte) bool {
	i := bytes.LastIndex(body, []byte("trailer"))
	region := body
	if i >= 0 {
		region = body[i:]
	}
	return bytes.Contains(region, []byte("/Encrypt"))
}

func pdfPageCount(body []byte) int {
	n := 0
	for i := 0; i+10 < len(body); i++ {
		if !bytes.HasPrefix(body[i:], []byte("/Type")) {
			continue
		}
		j := i + len("/Type")
		for j < len(body) && (body[j] == ' ' || body[j] == '\t' || body[j] == '\n' || body[j] == '\r') {
			j++
		}
		if bytes.HasPrefix(body[j:], []byte("/Page")) && (j+5 >= len(body) || body[j+5] != 's') {
			n++
		}
	}
	return n
}

func pdfStreams(body []byte) ([][]byte, error) {
	var out [][]byte
	var total int
	for i := 0; i < len(body); {
		at := findPDFStream(body, i)
		if at < 0 {
			break
		}
		dictStart := at - 400
		if dictStart < 0 {
			dictStart = 0
		}
		dict := body[dictStart:at]
		start := at + len("stream")
		if start < len(body) && body[start] == '\r' {
			start++
		}
		if start < len(body) && body[start] == '\n' {
			start++
		}
		length, direct := pdfLength(dict)
		var data []byte
		if direct && length >= 0 && start+length <= len(body) {
			data = body[start : start+length]
			i = start + length
		} else {
			end := bytes.Index(body[start:], []byte("endstream"))
			if end < 0 {
				return nil, errors.New("document is invalid")
			}
			data = body[start : start+end]
			if len(data) > 0 && data[len(data)-1] == '\n' {
				data = data[:len(data)-1]
				if len(data) > 0 && data[len(data)-1] == '\r' {
					data = data[:len(data)-1]
				}
			}
			i = start + end + len("endstream")
		}
		if bytes.Contains(dict, []byte("/FlateDecode")) {
			decoded, err := inflateLimited(data, maxZipUncompressed)
			if err != nil {
				return nil, errors.New("document is invalid")
			}
			data = decoded
		} else if bytes.Contains(dict, []byte("/Filter")) {
			i = start + len(data)
			continue
		}
		total += len(data)
		if total > maxZipUncompressed {
			return nil, fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
		}
		out = append(out, data)
	}
	return out, nil
}

func pdfLength(dict []byte) (int, bool) {
	rel := bytes.LastIndex(dict, []byte("/Length"))
	if rel < 0 {
		return 0, false
	}
	rest := dict[rel+len("/Length"):]
	rest = bytes.TrimLeft(rest, " \t\r\n")
	if len(rest) == 0 || rest[0] < '0' || rest[0] > '9' {
		return 0, false
	}
	n := 0
	k := 0
	for k < len(rest) && rest[k] >= '0' && rest[k] <= '9' {
		n = n*10 + int(rest[k]-'0')
		k++
		if n > maxZipUncompressed {
			return 0, false
		}
	}
	tail := bytes.TrimLeft(rest[k:], " \t\r\n")
	if bytes.HasPrefix(tail, []byte("0 R")) {
		return 0, false
	}
	return n, true
}

func inflateLimited(data []byte, limit int) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		return nil, errors.New("too large")
	}
	return out, nil
}

func appendPDFText(b *strings.Builder, data []byte, cmap map[uint16]rune) error {
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0:
			i++
		case c == '%':
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case c == '(':
			s, next, err := readPDFLiteral(data, i)
			if err != nil {
				return err
			}
			if next < len(data) {
				op, after := pdfOperator(data, next)
				if op == "Tj" || op == "'" || op == `"` {
					if op == "'" || op == `"` {
						b.WriteByte('\n')
					}
					b.WriteString(winAnsi(s))
					i = after
					continue
				}
			}
			i = next
		case c == '<':
			if i+1 < len(data) && data[i+1] == '<' {
				end := bytes.Index(data[i:], []byte(">>"))
				if end < 0 {
					return errors.New("document is invalid")
				}
				i += end + 2
				continue
			}
			hex, next, ok := readPDFHex(data, i)
			if !ok {
				i++
				continue
			}
			op, after := pdfOperator(data, next)
			if op == "Tj" || op == "'" || op == `"` {
				if op == "'" || op == `"` {
					b.WriteByte('\n')
				}
				b.WriteString(decodePDFHex(hex, cmap))
				i = after
				continue
			}
			i = next
		case c == '[':
			end := bytes.IndexByte(data[i:], ']')
			if end < 0 {
				return errors.New("document is invalid")
			}
			chunk := data[i : i+end+1]
			op, after := pdfOperator(data, i+end+1)
			if op == "TJ" {
				if err := appendPDFArray(b, chunk, cmap); err != nil {
					return err
				}
				i = after
				continue
			}
			i += end + 1
		case c == '/':
			i++
			for i < len(data) && !pdfDelim(data[i]) {
				i++
			}
		default:
			op, after := pdfOperator(data, i)
			if op == "T*" {
				b.WriteByte('\n')
			}
			if after == i {
				i++
			} else {
				i = after
			}
		}
	}
	return nil
}

func appendPDFArray(b *strings.Builder, chunk []byte, cmap map[uint16]rune) error {
	for i := 0; i < len(chunk); {
		switch chunk[i] {
		case '(':
			s, next, err := readPDFLiteral(chunk, i)
			if err != nil {
				return err
			}
			b.WriteString(winAnsi(s))
			i = next
		case '<':
			hex, next, ok := readPDFHex(chunk, i)
			if !ok {
				i++
				continue
			}
			b.WriteString(decodePDFHex(hex, cmap))
			i = next
		default:
			i++
		}
	}
	return nil
}

func pdfOperator(data []byte, i int) (string, int) {
	for i < len(data) && (data[i] == ' ' || data[i] == '\t' || data[i] == '\r' || data[i] == '\n') {
		i++
	}
	start := i
	for i < len(data) && !pdfDelim(data[i]) {
		i++
	}
	return string(data[start:i]), i
}

func pdfDelim(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '\f', '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	default:
		return false
	}
}

func readPDFLiteral(data []byte, i int) (string, int, error) {
	var b strings.Builder
	depth := 1
	i++
	for i < len(data) && depth > 0 {
		c := data[i]
		if c == '\\' && i+1 < len(data) {
			n := data[i+1]
			switch n {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case '(', ')', '\\':
				b.WriteByte(n)
			case '\n':
			case '\r':
				if i+2 < len(data) && data[i+2] == '\n' {
					i++
				}
			default:
				if n >= '0' && n <= '7' {
					oct := []byte{n}
					for k := 0; k < 2 && i+2+k < len(data) && data[i+2+k] >= '0' && data[i+2+k] <= '7'; k++ {
						oct = append(oct, data[i+2+k])
					}
					v := 0
					for _, d := range oct {
						v = v*8 + int(d-'0')
					}
					b.WriteByte(byte(v))
					i += 1 + len(oct)
					continue
				}
				b.WriteByte(n)
			}
			i += 2
			continue
		}
		if c == '(' {
			depth++
		}
		if c == ')' {
			depth--
			if depth == 0 {
				return b.String(), i + 1, nil
			}
		}
		b.WriteByte(c)
		i++
	}
	return "", i, errors.New("document is invalid")
}

func readPDFHex(data []byte, i int) (string, int, bool) {
	if i+1 < len(data) && data[i+1] == '<' {
		return "", i, false
	}
	j := i + 1
	var b strings.Builder
	for j < len(data) && data[j] != '>' {
		if isHex(data[j]) {
			b.WriteByte(data[j])
		}
		j++
	}
	if j >= len(data) {
		return b.String(), j, true
	}
	return b.String(), j + 1, true
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func decodePDFHex(hex string, cmap map[uint16]rune) string {
	if len(hex)%2 == 1 {
		hex += "0"
	}
	raw := make([]byte, 0, len(hex)/2)
	for i := 0; i+1 < len(hex); i += 2 {
		v, err := strconv.ParseUint(hex[i:i+2], 16, 8)
		if err != nil {
			continue
		}
		raw = append(raw, byte(v))
	}
	if len(cmap) > 0 && len(raw) >= 2 {
		var b strings.Builder
		for i := 0; i+1 < len(raw); i += 2 {
			gid := uint16(raw[i])<<8 | uint16(raw[i+1])
			if r, ok := cmap[gid]; ok {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		u := make([]uint16, 0, (len(raw)-2)/2)
		for i := 2; i+1 < len(raw); i += 2 {
			u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
		}
		return string(utf16.Decode(u))
	}
	return winAnsi(string(raw))
}

func parseToUnicode(stream []byte, cmap map[uint16]rune) {
	text := string(stream)
	for _, block := range []string{"beginbfchar", "beginbfrange"} {
		rest := text
		for {
			at := strings.Index(rest, block)
			if at < 0 {
				break
			}
			rest = rest[at+len(block):]
			endMark := "endbfchar"
			if block == "beginbfrange" {
				endMark = "endbfrange"
			}
			end := strings.Index(rest, endMark)
			if end < 0 {
				break
			}
			section := rest[:end]
			fields := pdfHexFields(section)
			if block == "beginbfchar" {
				for i := 0; i+1 < len(fields); i += 2 {
					src, srcOK := parseHexGID(fields[i])
					dst := decodeUTF16Hex(fields[i+1])
					if srcOK && dst != "" {
						cmap[src] = []rune(dst)[0]
					}
				}
			} else {
				for i := 0; i+2 < len(fields); i += 3 {
					start, startOK := parseHexGID(fields[i])
					finish, finishOK := parseHexGID(fields[i+1])
					dst := decodeUTF16Hex(fields[i+2])
					if !startOK || !finishOK || dst == "" || finish < start || int(finish-start) > 2048 {
						continue
					}
					base := []rune(dst)[0]
					for g := start; g <= finish; g++ {
						cmap[g] = base + rune(g-start)
					}
				}
			}
			rest = rest[end:]
		}
	}
}

func pdfHexFields(s string) []string {
	var out []string
	for {
		start := strings.IndexByte(s, '<')
		if start < 0 {
			return out
		}
		s = s[start+1:]
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return out
		}
		out = append(out, s[:end])
		s = s[end+1:]
	}
}

func parseHexGID(hex string) (uint16, bool) {
	hex = strings.TrimSpace(hex)
	if len(hex) == 0 || len(hex) > 4 {
		return 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 16)
	if err != nil {
		return 0, false
	}
	return uint16(v), true
}

func decodeUTF16Hex(hex string) string {
	hex = strings.TrimSpace(hex)
	if len(hex)%2 == 1 {
		hex += "0"
	}
	raw := make([]byte, 0, len(hex)/2)
	for i := 0; i+1 < len(hex); i += 2 {
		v, err := strconv.ParseUint(hex[i:i+2], 16, 8)
		if err != nil {
			return ""
		}
		raw = append(raw, byte(v))
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		raw = raw[2:]
	}
	if len(raw)%2 == 1 {
		return ""
	}
	u := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
	}
	return string(utf16.Decode(u))
}

func winAnsi(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x80 || c >= 0xA0 {
			b.WriteByte(c)
			continue
		}
		if r, ok := winAnsiExtra[c]; ok {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var winAnsiExtra = map[byte]rune{
	0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„', 0x85: '…', 0x86: '†', 0x87: '‡',
	0x88: 'ˆ', 0x89: '‰', 0x8A: 'Š', 0x8B: '‹', 0x8C: 'Œ', 0x8E: 'Ž', 0x91: '‘',
	0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—', 0x98: '˜',
	0x99: '™', 0x9A: 'š', 0x9B: '›', 0x9C: 'œ', 0x9E: 'ž', 0x9F: 'Ÿ',
}

func toUnicodeCMap(used map[uint16]rune) string {
	var b strings.Builder
	b.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n")
	b.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	b.WriteString("/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n")
	b.WriteString("1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
	ids := make([]uint16, 0, len(used))
	for gid := range used {
		ids = append(ids, gid)
	}
	// Stable output keeps the PDF bytes deterministic for tests.
	for i := 1; i < len(ids); i++ {
		j := i
		for j > 0 && ids[j] < ids[j-1] {
			ids[j], ids[j-1] = ids[j-1], ids[j]
			j--
		}
	}
	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		fmt.Fprintf(&b, "%d beginbfchar\n", end-start)
		for _, gid := range ids[start:end] {
			r := used[gid]
			fmt.Fprintf(&b, "<%04X> <%04X>\n", gid, uint16(r))
		}
		b.WriteString("endbfchar\n")
	}
	b.WriteString("endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n")
	return b.String()
}

type pdfBuilder struct {
	objs [][]byte
}

func (d *pdfBuilder) add(obj []byte) int {
	d.objs = append(d.objs, obj)
	return len(d.objs)
}

func (d *pdfBuilder) bytes(root int) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xFF\xFF\xFF\xFF\n")
	offsets := make([]int, len(d.objs)+1)
	for i, obj := range d.objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n", i+1)
		buf.Write(obj)
		buf.WriteString("\nendobj\n")
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(d.objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(d.objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(d.objs)+1, root, xref)
	return buf.Bytes()
}

func pdfDict(s string) []byte { return []byte(s) }

func pdfStream(extra map[string]string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString("<< ")
	for k, v := range extra {
		b.WriteString(k)
		b.WriteByte(' ')
		b.WriteString(v)
		b.WriteByte(' ')
	}
	fmt.Fprintf(&b, "/Length %d >>\nstream\n", len(data))
	b.Write(data)
	b.WriteString("\nendstream")
	return b.Bytes()
}

var (
	fontOnce sync.Once
	fontCmap map[rune]uint16
	fontErr  error
	fontTTF  []byte
)

func pdfFont() ([]byte, map[rune]uint16, error) {
	fontOnce.Do(func() {
		fontTTF = goregular.TTF
		fontCmap, fontErr = ttfCmap(fontTTF)
	})
	if fontErr != nil {
		return nil, nil, errors.New("pdf font is unavailable")
	}
	return fontTTF, fontCmap, nil
}

func ttfCmap(font []byte) (map[rune]uint16, error) {
	if len(font) < 12 {
		return nil, errors.New("short font")
	}
	numTables := int(binary.BigEndian.Uint16(font[4:6]))
	var cmapOff, cmapLen uint32
	found := false
	rec := 12
	for i := 0; i < numTables; i++ {
		if rec+16 > len(font) {
			return nil, errors.New("short font")
		}
		tag := string(font[rec : rec+4])
		off := binary.BigEndian.Uint32(font[rec+8 : rec+12])
		length := binary.BigEndian.Uint32(font[rec+12 : rec+16])
		if tag == "cmap" {
			cmapOff, cmapLen, found = off, length, true
			break
		}
		rec += 16
	}
	if !found || cmapLen < 4 || int(cmapOff+cmapLen) > len(font) {
		return nil, errors.New("font has no cmap")
	}
	table := font[cmapOff : cmapOff+cmapLen]
	nsub := int(binary.BigEndian.Uint16(table[2:4]))
	out := map[rune]uint16{}
	type subtable struct {
		off    int
		format int
	}
	var subs []subtable
	for i := 0; i < nsub; i++ {
		base := 4 + i*8
		if base+8 > len(table) {
			break
		}
		off := int(binary.BigEndian.Uint32(table[base+4 : base+8]))
		if off < 0 || off+2 > len(table) {
			continue
		}
		subs = append(subs, subtable{off, int(binary.BigEndian.Uint16(table[off : off+2]))})
	}
	parsed := false
	for _, format := range []int{4, 12} {
		for _, sub := range subs {
			if sub.format != format {
				continue
			}
			var err error
			if format == 4 {
				err = parseCmap4(table[sub.off:], out)
			} else {
				err = parseCmap12(table[sub.off:], out)
			}
			if err == nil {
				parsed = true
			}
		}
	}
	if !parsed || out['A'] == 0 {
		return nil, errors.New("font cmap is unusable")
	}
	return out, nil
}

func parseCmap4(b []byte, out map[rune]uint16) error {
	if len(b) < 14 || binary.BigEndian.Uint16(b[0:2]) != 4 {
		return errors.New("bad cmap")
	}
	segCount := int(binary.BigEndian.Uint16(b[6:8])) / 2
	if segCount <= 0 {
		return errors.New("bad cmap")
	}
	endCount := 14
	startCount := endCount + 2*segCount + 2
	idDelta := startCount + 2*segCount
	idRange := idDelta + 2*segCount
	if idRange+2*segCount > len(b) {
		return errors.New("bad cmap")
	}
	for i := 0; i < segCount; i++ {
		end := uint32(binary.BigEndian.Uint16(b[endCount+2*i : endCount+2*i+2]))
		start := uint32(binary.BigEndian.Uint16(b[startCount+2*i : startCount+2*i+2]))
		delta := int32(int16(binary.BigEndian.Uint16(b[idDelta+2*i : idDelta+2*i+2])))
		rangeOff := int(binary.BigEndian.Uint16(b[idRange+2*i : idRange+2*i+2]))
		if start == 0xFFFF {
			continue
		}
		for c := start; c <= end; c++ {
			var gid uint16
			if rangeOff == 0 {
				gid = uint16(int32(c) + delta)
			} else {
				pos := idRange + 2*i + rangeOff + 2*int(c-start)
				if pos+2 > len(b) {
					continue
				}
				gid = binary.BigEndian.Uint16(b[pos : pos+2])
				if gid != 0 {
					gid = uint16(int32(gid) + delta)
				}
			}
			if gid != 0 {
				out[rune(c)] = gid
			}
		}
	}
	return nil
}

func parseCmap12(b []byte, out map[rune]uint16) error {
	if len(b) < 16 || binary.BigEndian.Uint16(b[0:2]) != 12 {
		return errors.New("bad cmap")
	}
	n := binary.BigEndian.Uint32(b[12:16])
	if n > 20000 {
		return errors.New("bad cmap")
	}
	pos := 16
	for i := uint32(0); i < n; i++ {
		if pos+12 > len(b) {
			return errors.New("bad cmap")
		}
		start := binary.BigEndian.Uint32(b[pos : pos+4])
		end := binary.BigEndian.Uint32(b[pos+4 : pos+8])
		glyph := binary.BigEndian.Uint32(b[pos+8 : pos+12])
		pos += 12
		if end < start || end-start > 0xFFFF {
			continue
		}
		for c := start; c <= end; c++ {
			g := glyph + (c - start)
			if g > 0 && g <= 0xFFFF {
				out[rune(c)] = uint16(g)
			}
		}
	}
	return nil
}
