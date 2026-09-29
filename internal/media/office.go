package media

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	maxZipFiles        = 64
	maxZipUncompressed = 8 << 20
	maxSheetRows       = 2000
	maxSheetCols       = 64
)

func xlsxBytes(text string) ([]byte, error) {
	rows, err := tableRows(text)
	if err != nil {
		return nil, err
	}
	if len(rows) > maxSheetRows {
		return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	var sheet strings.Builder
	sheet.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	sheet.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for r, row := range rows {
		if len(row) > maxSheetCols {
			return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
		}
		fmt.Fprintf(&sheet, `<row r="%d">`, r+1)
		for c, value := range row {
			ref := colName(c) + strconv.Itoa(r+1)
			fmt.Fprintf(&sheet, `<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`, ref, xmlText(value))
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	files := []zipPart{
		{"[Content_Types].xml", xlsxContentTypes},
		{"_rels/.rels", xlsxRootRels},
		{"xl/workbook.xml", xlsxWorkbook},
		{"xl/_rels/workbook.xml.rels", xlsxWorkbookRels},
		{"xl/worksheets/sheet1.xml", sheet.String()},
	}
	return writeZip(files)
}

func xlsxText(body []byte) (string, error) {
	zr, err := openOfficeZip(body)
	if err != nil {
		return "", err
	}
	shared, err := xlsxSharedStrings(zr)
	if err != nil {
		return "", err
	}
	sheets := xlsxSheetFiles(zr)
	if len(sheets) == 0 {
		return "", errors.New("document is invalid")
	}
	var blocks []string
	for _, sheet := range sheets {
		raw, err := readZipPart(sheet.file)
		if err != nil {
			return "", err
		}
		rows, err := xlsxRows(raw, shared)
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		for _, row := range rows {
			if err := w.Write(row); err != nil {
				return "", err
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return "", err
		}
		text := strings.TrimRight(buf.String(), "\n")
		if len(sheets) > 1 {
			text = "# " + sheet.name + "\n" + text
		}
		blocks = append(blocks, text)
	}
	return strings.Join(blocks, "\n\n"), nil
}

func pptxBytes(text string) ([]byte, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	slides := splitSlides(text)
	if len(slides) > MaxPages {
		return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	files := []zipPart{
		{"[Content_Types].xml", pptxContentTypes(len(slides))},
		{"_rels/.rels", pptxRootRels},
		{"ppt/presentation.xml", pptxPresentation(len(slides))},
		{"ppt/_rels/presentation.xml.rels", pptxPresentationRels(len(slides))},
		{"ppt/slideMasters/slideMaster1.xml", pptxMaster},
		{"ppt/slideMasters/_rels/slideMaster1.xml.rels", pptxMasterRels},
		{"ppt/slideLayouts/slideLayout1.xml", pptxLayout},
		{"ppt/slideLayouts/_rels/slideLayout1.xml.rels", pptxLayoutRels},
		{"ppt/theme/theme1.xml", pptxTheme},
		{"ppt/presProps.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:presentationPr xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`},
		{"ppt/viewProps.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:viewPr xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`},
		{"docProps/core.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Hub</dc:title></cp:coreProperties>`},
		{"docProps/app.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>hermes-hub</Application></Properties>`},
	}
	for i, slide := range slides {
		files = append(files, zipPart{fmt.Sprintf("ppt/slides/slide%d.xml", i+1), pptxSlide(slide)})
		files = append(files, zipPart{fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", i+1), pptxSlideRels})
	}
	return writeZip(files)
}

func pptxText(body []byte) (string, error) {
	zr, err := openOfficeZip(body)
	if err != nil {
		return "", err
	}
	var slides []*zip.File
	for _, f := range zr.File {
		name := path.Clean(f.Name)
		base := path.Base(name)
		if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(base, ".xml") && !strings.Contains(base, ".rels") {
			slides = append(slides, f)
		}
	}
	sort.Slice(slides, func(i, j int) bool {
		return slideNumber(slides[i].Name) < slideNumber(slides[j].Name)
	})
	if len(slides) == 0 {
		return "", errors.New("document is invalid")
	}
	if len(slides) > MaxPages {
		return "", fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	var blocks []string
	for _, slide := range slides {
		raw, err := readZipPart(slide)
		if err != nil {
			return "", err
		}
		text, err := pptxSlideText(raw)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, text)
	}
	return strings.TrimRight(strings.Join(blocks, "\n\n"), "\n"), nil
}

func splitSlides(text string) []string {
	if strings.TrimSpace(text) == "" {
		return []string{""}
	}
	parts := strings.Split(text, "\n\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, strings.Trim(part, "\n"))
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func tableRows(text string) ([][]string, error) {
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	rows, err := reader.ReadAll()
	if err != nil {
		lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
		rows = make([][]string, len(lines))
		for i, line := range lines {
			rows[i] = []string{strings.TrimSuffix(line, "\r")}
		}
	}
	if len(rows) == 0 {
		rows = [][]string{{}}
	}
	return rows, nil
}

type zipPart struct {
	name string
	body string
}

func writeZip(files []zipPart) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, file := range files {
		h := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		h.SetMode(0o600)
		entry, err := w.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err = io.WriteString(entry, file.body); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > MaxDocumentBytes {
		return nil, fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
	}
	return buf.Bytes(), nil
}

func openOfficeZip(body []byte) (*zip.Reader, error) {
	if len(body) == 0 || len(body) > MaxDocumentBytes {
		return nil, errors.New("document is invalid")
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, errors.New("document is invalid")
	}
	if len(zr.File) == 0 || len(zr.File) > maxZipFiles {
		return nil, errors.New("document is invalid")
	}
	var total uint64
	for _, f := range zr.File {
		if f.Flags&0x1 != 0 {
			return nil, errors.New("document is invalid")
		}
		name := f.Name
		if name == "" || strings.Contains(name, `\`) || strings.Contains(name, "..") || path.IsAbs(name) {
			return nil, errors.New("document is invalid")
		}
		if f.UncompressedSize64 > maxZipUncompressed || total+f.UncompressedSize64 > maxZipUncompressed {
			return nil, fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
		}
		total += f.UncompressedSize64
	}
	return zr, nil
}

func readZipPart(f *zip.File) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, errors.New("document is invalid")
	}
	defer r.Close()
	body, err := io.ReadAll(io.LimitReader(r, maxZipUncompressed+1))
	if err != nil || len(body) > maxZipUncompressed {
		return nil, errors.New("document is invalid")
	}
	return body, nil
}

type xlsxSheet struct {
	name string
	file *zip.File
}

func xlsxSheetFiles(zr *zip.Reader) []xlsxSheet {
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		byName[path.Clean(f.Name)] = f
	}
	var sheets []xlsxSheet
	if workbook, ok := byName["xl/workbook.xml"]; ok {
		if raw, err := readZipPart(workbook); err == nil {
			dec := xml.NewDecoder(bytes.NewReader(raw))
			for {
				tok, err := dec.Token()
				if err != nil {
					break
				}
				start, ok := tok.(xml.StartElement)
				if !ok || start.Name.Local != "sheet" {
					continue
				}
				name := "Sheet"
				id := ""
				for _, attr := range start.Attr {
					switch attr.Name.Local {
					case "name":
						name = attr.Value
					case "id":
						id = attr.Value
					}
				}
				target := xlsxSheetTarget(byName["xl/_rels/workbook.xml.rels"], id)
				if target == "" {
					continue
				}
				if file := byName[path.Clean(path.Join("xl", target))]; file != nil {
					sheets = append(sheets, xlsxSheet{name: name, file: file})
				}
			}
		}
	}
	if len(sheets) > 0 {
		return sheets
	}
	var names []string
	for name, file := range byName {
		if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
			names = append(names, name)
			_ = file
		}
	}
	sort.Strings(names)
	for _, name := range names {
		sheets = append(sheets, xlsxSheet{name: path.Base(name), file: byName[name]})
	}
	return sheets
}

func xlsxSheetTarget(rels *zip.File, id string) string {
	if rels == nil || id == "" {
		return ""
	}
	raw, err := readZipPart(rels)
	if err != nil {
		return ""
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		var gotID, target string
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "Id":
				gotID = attr.Value
			case "Target":
				target = attr.Value
			}
		}
		if gotID == id {
			return target
		}
	}
}

func xlsxSharedStrings(zr *zip.Reader) ([]string, error) {
	var file *zip.File
	for _, f := range zr.File {
		if path.Clean(f.Name) == "xl/sharedStrings.xml" {
			file = f
			break
		}
	}
	if file == nil {
		return nil, nil
	}
	raw, err := readZipPart(file)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var out []string
	var current strings.Builder
	inSI := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("document is invalid")
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if tok.Name.Local == "si" {
				inSI = true
				current.Reset()
			}
		case xml.EndElement:
			if tok.Name.Local == "si" && inSI {
				out = append(out, current.String())
				inSI = false
			}
		case xml.CharData:
			if inSI {
				current.Write(tok)
			}
		}
	}
	return out, nil
}

func xlsxRows(raw []byte, shared []string) ([][]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var rows [][]string
	var row []string
	var ref, kind, value string
	inCell := false
	inValue := false
	cells := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("document is invalid")
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			switch tok.Name.Local {
			case "row":
				row = nil
			case "c":
				inCell = true
				ref, kind, value = "", "", ""
				for _, attr := range tok.Attr {
					switch attr.Name.Local {
					case "r":
						ref = attr.Value
					case "t":
						kind = attr.Value
					}
				}
			case "v", "t":
				inValue = true
			}
		case xml.CharData:
			if inCell && inValue {
				value += string(tok)
			}
		case xml.EndElement:
			switch tok.Name.Local {
			case "v", "t":
				inValue = false
			case "c":
				text := xlsxCell(kind, strings.TrimSpace(value), shared)
				col, _, ok := splitCellRef(ref)
				if !ok {
					col = len(row)
				}
				if col >= maxSheetCols || col < 0 {
					return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
				}
				for len(row) <= col {
					row = append(row, "")
				}
				row[col] = text
				cells++
				if cells > maxSheetRows*maxSheetCols {
					return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
				}
				inCell = false
			case "row":
				if row != nil {
					if len(rows) >= maxSheetRows {
						return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
					}
					rows = append(rows, row)
				}
			}
		}
	}
	return rows, nil
}

func xlsxCell(kind, value string, shared []string) string {
	switch kind {
	case "s":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n >= len(shared) {
			return ""
		}
		return shared[n]
	case "inlineStr", "str":
		return value
	case "b":
		if value == "1" {
			return "true"
		}
		return "false"
	default:
		return value
	}
}

func splitCellRef(ref string) (int, int, bool) {
	if ref == "" {
		return 0, 0, false
	}
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		i++
	}
	if i == 0 || i == len(ref) {
		return 0, 0, false
	}
	col := 0
	for _, c := range ref[:i] {
		col = col*26 + int(c-'A'+1)
	}
	row, err := strconv.Atoi(ref[i:])
	if err != nil || row <= 0 {
		return 0, 0, false
	}
	return col - 1, row - 1, true
}

func colName(n int) string {
	n++
	var b [8]byte
	i := len(b)
	for n > 0 {
		n--
		i--
		b[i] = byte('A' + n%26)
		n /= 26
	}
	return string(b[i:])
}

func slideNumber(name string) int {
	base := strings.TrimSuffix(path.Base(name), ".xml")
	base = strings.TrimPrefix(base, "slide")
	n, _ := strconv.Atoi(base)
	return n
}

func pptxSlideText(raw []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var lines []string
	var current strings.Builder
	inText := false
	inPara := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("document is invalid")
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if tok.Name.Space == drawingML && tok.Name.Local == "p" {
				inPara = true
				current.Reset()
			}
			if tok.Name.Space == drawingML && tok.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inText {
				current.Write(tok)
			}
		case xml.EndElement:
			if tok.Name.Space == drawingML && tok.Name.Local == "t" {
				inText = false
			}
			if tok.Name.Space == drawingML && tok.Name.Local == "p" && inPara {
				lines = append(lines, current.String())
				inPara = false
			}
		}
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n"), nil
}

const drawingML = "http://schemas.openxmlformats.org/drawingml/2006/main"

func xmlText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == 0x9 || r == 0xA || r == 0xD || r >= 0x20 {
			_ = xml.EscapeText(&b, []byte(string(r)))
		}
	}
	return b.String()
}

func pptxSlide(text string) string {
	var paras strings.Builder
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	for _, line := range lines {
		fmt.Fprintf(&paras, `<a:p><a:r><a:rPr lang="en-US" dirty="0" sz="2400"/><a:t>%s</a:t></a:r></a:p>`, xmlText(line))
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:cSld><p:spTree>` +
		`<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>` +
		`<p:sp><p:nvSpPr><p:cNvPr id="2" name="Content"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>` +
		`<p:spPr><a:xfrm><a:off x="457200" y="457200"/><a:ext cx="8229600" cy="5943600"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>` +
		`<p:txBody><a:bodyPr wrap="square"/><a:lstStyle/>` + paras.String() + `</p:txBody>` +
		`</p:sp></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`
}

func pptxContentTypes(slides int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	b.WriteString(`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/presProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presProps+xml"/>`)
	b.WriteString(`<Override PartName="/ppt/viewProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.viewProps+xml"/>`)
	b.WriteString(`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>`)
	b.WriteString(`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	for i := 1; i <= slides; i++ {
		fmt.Fprintf(&b, `<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, i)
	}
	b.WriteString(`</Types>`)
	return b.String()
}

func pptxPresentation(slides int) string {
	var ids strings.Builder
	for i := 1; i <= slides; i++ {
		fmt.Fprintf(&ids, `<p:sldId id="%d" r:id="rId%d"/>`, 255+i, i+1)
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<p:presentation xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">` +
		`<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>` +
		`<p:sldIdLst>` + ids.String() + `</p:sldIdLst>` +
		`<p:sldSz cx="9144000" cy="6858000" type="screen4x3"/>` +
		`<p:notesSz cx="6858000" cy="9144000"/>` +
		`</p:presentation>`
}

func pptxPresentationRels(slides int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	b.WriteString(`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>`)
	for i := 1; i <= slides; i++ {
		fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide%d.xml"/>`, i+1, i)
	}
	b.WriteString(`</Relationships>`)
	return b.String()
}

const (
	xlsxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`
	xlsxRootRels     = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`
	xlsxWorkbook     = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`
	xlsxWorkbookRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`

	pptxRootRels   = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`
	pptxMasterRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/></Relationships>`
	pptxLayoutRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`
	pptxSlideRels  = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/></Relationships>`
	pptxMaster     = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sldMaster xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/></p:spTree></p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/><p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst></p:sldMaster>`
	pptxLayout     = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sldLayout xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" type="titleAndBody"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`
	pptxTheme      = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Hub"><a:themeElements><a:clrScheme name="Hub"><a:dk1><a:srgbClr val="000000"/></a:dk1><a:lt1><a:srgbClr val="FFFFFF"/></a:lt1><a:dk2><a:srgbClr val="1F4E79"/></a:dk2><a:lt2><a:srgbClr val="EEECE1"/></a:lt2><a:accent1><a:srgbClr val="5B9BD5"/></a:accent1><a:accent2><a:srgbClr val="ED7D31"/></a:accent2><a:accent3><a:srgbClr val="A9D08E"/></a:accent3><a:accent4><a:srgbClr val="FFC000"/></a:accent4><a:accent5><a:srgbClr val="4472C4"/></a:accent5><a:accent6><a:srgbClr val="70AD47"/></a:accent6><a:hlink><a:srgbClr val="0563C1"/></a:hlink><a:folHlink><a:srgbClr val="954F72"/></a:folHlink></a:clrScheme><a:fontScheme name="Hub"><a:majorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface=""/></a:majorFont><a:minorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface=""/></a:minorFont></a:fontScheme><a:fmtScheme name="Hub"><a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst><a:lnStyleLst><a:ln w="6350"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:ln></a:lnStyleLst><a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst><a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst></a:fmtScheme></a:themeElements></a:theme>`
)
