package media

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// Office round-trip: CSV text becomes a real .xlsx and parses back; slide
// text becomes a .pptx and reads back in slide order; hostile or malformed
// containers are rejected before extraction.
func TestOfficeRoundTripAndGuards(t *testing.T) {
	xlsx, err := xlsxBytes("name,score\nalice,10\nbob,20")
	if err != nil {
		t.Fatal(err)
	}
	text, err := xlsxText(xlsx)
	if err != nil || !strings.Contains(text, "name,score") || !strings.Contains(text, "alice,10") {
		t.Fatalf("xlsx round-trip: %q %v", text, err)
	}
	if _, err := xlsxText([]byte("not a zip")); err == nil {
		t.Fatal("garbage accepted as xlsx")
	}
	if _, err := xlsxText(nil); err == nil {
		t.Fatal("empty accepted as xlsx")
	}
	// A zip without worksheets is not a spreadsheet.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	if _, err := xlsxText(buf.Bytes()); err == nil {
		t.Fatal("sheetless zip accepted as xlsx")
	}
	// Zip-slip names are refused.
	var buf2 bytes.Buffer
	zw2 := zip.NewWriter(&buf2)
	w2, _ := zw2.Create("../escape.xml")
	_, _ = w2.Write([]byte("x"))
	_ = zw2.Close()
	if _, err := openOfficeZip(buf2.Bytes()); err == nil {
		t.Fatal("zip-slip name accepted")
	}

	// Shared strings and numeric/boolean cells decode through the shared table.
	var buf3 bytes.Buffer
	zw3 := zip.NewWriter(&buf3)
	parts := map[string]string{
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="b"><v>1</v></c><c r="C1"><v>42</v></c><c><v>tail</v></c></row></sheetData></worksheet>`,
		"xl/sharedStrings.xml":     `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>shared-value</t></si></sst>`,
	}
	for name, body := range parts {
		w, _ := zw3.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw3.Close()
	text, err = xlsxText(buf3.Bytes())
	if err != nil || !strings.Contains(text, "shared-value") || !strings.Contains(text, "true") || !strings.Contains(text, "42") {
		t.Fatalf("xlsx decode: %q %v", text, err)
	}

	// Multi-slide pptx keeps order.
	pptx, err := pptxBytes("Slide one\nline two\n\nSecond slide")
	if err != nil {
		t.Fatal(err)
	}
	text, err = pptxText(pptx)
	if err != nil || !strings.Contains(text, "Slide one") || !strings.Contains(text, "Second slide") {
		t.Fatalf("pptx round-trip: %q %v", text, err)
	}
	if strings.Index(text, "Slide one") > strings.Index(text, "Second slide") {
		t.Fatalf("slide order wrong: %q", text)
	}
	if _, err := pptxText([]byte("not a zip")); err == nil {
		t.Fatal("garbage accepted as pptx")
	}
	if _, err := pptxText(buf.Bytes()); err == nil {
		t.Fatal("slideless zip accepted as pptx")
	}

	if c := splitSlides(""); len(c) != 1 {
		t.Fatalf("empty split: %v", c)
	}
	rows, err := tableRows("a,b\n\"unclosed")
	if err != nil || len(rows) == 0 {
		t.Fatalf("csv fallback: %v %v", rows, err)
	}
	if col, row, ok := splitCellRef("B3"); !ok || col != 1 || row != 2 {
		t.Fatalf("cell ref: %d %d %v", col, row, ok)
	}
	if _, _, ok := splitCellRef("3B"); ok {
		t.Fatal("malformed cell ref accepted")
	}
	if colName(0) != "A" || colName(26) != "AA" {
		t.Fatal("column naming wrong")
	}
	if slideNumber("ppt/slides/slide12.xml") != 12 {
		t.Fatal("slide number parse wrong")
	}
}
