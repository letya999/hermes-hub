package media

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
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
		content.WriteString("BT\n/F1 12 Tf\n14 TL\n48 740 Td\n")
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

var (
	fontOnce sync.Once
	fontCmap map[rune]uint16
	fontErr  error
	fontTTF  []byte
)
