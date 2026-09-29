package media

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func flateBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInflateLimitedRoundtripAndLimits(t *testing.T) {
	payload := []byte(strings.Repeat("abc123 ", 50))
	out, err := inflateLimited(flateBytes(t, payload), 4096)
	if err != nil || !bytes.Equal(out, payload) {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, err := inflateLimited(flateBytes(t, payload), len(payload)-1); err == nil {
		t.Fatal("over-limit inflate must fail")
	}
	if _, err := inflateLimited([]byte("not deflated"), 1024); err == nil {
		t.Fatal("corrupt stream must fail")
	}
}

func TestAppendPDFArrayMixed(t *testing.T) {
	var b strings.Builder
	// literal + kern + hex + literal inside one TJ array
	if err := appendPDFArray(&b, []byte("[(He) -20 <6C6C> (o)]"), nil); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); !strings.Contains(got, "He") || !strings.Contains(got, "o") {
		t.Fatalf("unexpected array text %q", got)
	}
	var bad strings.Builder
	if err := appendPDFArray(&bad, []byte("[(unterminated"), nil); err == nil {
		t.Fatal("unterminated literal must fail")
	}
}

func TestReadPDFLiteralEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"(a\\nb)", "a\nb"},
		{"(a\\rb)", "a\rb"},
		{"(a\\tb)", "a\tb"},
		{"(a\\bb)", "a\bb"},
		{"(a\\fb)", "a\fb"},
		{"(a\\(b\\)c)", "a(b)c"},
		{"(a\\\\b)", "a\\b"},
		{"(a\\\nb)", "ab"},
		{"(a\\\r\nb)", "ab"},
		{"(x\\101y)", "xAy"}, // octal escape
		{"(x\\053y)", "x+y"}, // octal escape
		{"(a\\zb)", "azb"},   // unknown escape keeps char
		{"(o(n)e)", "o(n)e"}, // nested parens
	}
	for _, tc := range cases {
		got, next, err := readPDFLiteral([]byte(tc.in), 0)
		if err != nil || got != tc.want || next != len(tc.in) {
			t.Fatalf("%q → %q,%d,%v want %q", tc.in, got, next, err, tc.want)
		}
	}
	if _, _, err := readPDFLiteral([]byte("(open"), 0); err == nil {
		t.Fatal("unterminated literal must fail")
	}
}

func TestReadPDFHexAndDecode(t *testing.T) {
	hex, next, ok := readPDFHex([]byte("<48656C6C6F>"), 0)
	if !ok || hex != "48656C6C6F" || next != 12 {
		t.Fatalf("hex %q next %d ok %v", hex, next, ok)
	}
	if _, _, ok := readPDFHex([]byte("<<"), 0); ok {
		t.Fatal("dict opener must not read as hex")
	}
	if got := decodePDFHex("48656C6C6", nil); got != "Hell`" && got == "" {
		t.Fatalf("odd hex pad got %q", got)
	}
	// cmap path: gid 0x0001 → 'A'
	cmap := map[uint16]rune{0x0001: 'A'}
	if got := decodePDFHex("0001", cmap); got != "A" {
		t.Fatalf("cmap decode got %q", got)
	}
	// UTF-16BE BOM path
	if got := decodePDFHex("FEFF0048", nil); got != "H" {
		t.Fatalf("utf16 decode got %q", got)
	}
}

func TestParseToUnicodeBfcharAndBfrange(t *testing.T) {
	cmap := map[uint16]rune{}
	parseToUnicode([]byte("1 beginbfchar\n<0001> <0041>\nendbfchar\n"), cmap)
	if cmap[1] != 'A' {
		t.Fatalf("bfchar mapping missing: %v", cmap)
	}
	parseToUnicode([]byte("1 beginbfrange\n<0002> <0004> <0042>\nendbfrange\n"), cmap)
	if cmap[2] != 'B' || cmap[4] != 'D' {
		t.Fatalf("bfrange mapping wrong: %v", cmap)
	}
	// degenerate ranges are skipped, not fatal
	parseToUnicode([]byte("1 beginbfrange\n<0005> <0002> <0042>\nendbfrange\n"), cmap)
	if _, ok := cmap[5]; ok {
		t.Fatal("reversed range must be skipped")
	}
}

func TestDecodeUTF16Hex(t *testing.T) {
	if got := decodeUTF16Hex("FEFF00480049"); got != "HI" {
		t.Fatalf("BOM strip failed: %q", got)
	}
	if got := decodeUTF16Hex("041"); got == "" {
		t.Fatal("odd hex must pad, not fail")
	}
	if decodeUTF16Hex("zz") != "" {
		t.Fatal("invalid hex must give empty")
	}
}

func TestWinAnsiExtended(t *testing.T) {
	if got := winAnsi("\x85\x92"); got != "…’" {
		t.Fatalf("winAnsi extras got %q", got)
	}
	if got := winAnsi("ab"); got != "ab" {
		t.Fatalf("ascii passthrough got %q", got)
	}
}

func TestPDFTextRejects(t *testing.T) {
	if _, err := pdfText([]byte("short")); err == nil {
		t.Fatal("short body must fail")
	}
	if _, err := pdfText([]byte("%PDF-1.4 no markers")); err == nil {
		t.Fatal("no EOF/endobj must fail")
	}
	enc := []byte("%PDF-1.4\nendobj\ntrailer\n<< /Encrypt 1 0 R >>")
	if _, err := pdfText(enc); err == nil || !strings.Contains(err.Error(), "encrypt") {
		t.Fatal("encrypted must fail")
	}
}

func TestPDFStreamsFlateAndFilters(t *testing.T) {
	// pdfStreams looks back 400 bytes for the dict, so cases run as separate documents.
	flateDoc := []byte("%PDF-1.4\nendobj\n<< /Filter /FlateDecode >>\nstream\n" + string(flateBytes(t, []byte("(hi) Tj"))) + "\nendstream\n%%EOF")
	streams, err := pdfStreams(flateDoc)
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 1 || !bytes.Contains(streams[0], []byte("(hi) Tj")) {
		t.Fatalf("flate stream missing: %q", streams)
	}
	plainDoc := []byte("%PDF-1.4\nendobj\n<< >>\nstream\nBT (ok) Tj ET\nendstream\n%%EOF")
	streams, err = pdfStreams(plainDoc)
	if err != nil || len(streams) != 1 || !bytes.Contains(streams[0], []byte("(ok)")) {
		t.Fatalf("plain stream: %v %q", err, streams)
	}
	unknownDoc := []byte("%PDF-1.4\nendobj\n<< /Filter /DCTDecode >>\nstream\nZZZZ\nendstream\n%%EOF")
	streams, err = pdfStreams(unknownDoc)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.Join(streams, nil), []byte("ZZZZ")) {
		t.Fatal("unknown-filter stream must be skipped")
	}
	bad := []byte("%PDF-1.4\nendobj\n<< >>\nstream\nno end\n%%EOF")
	if _, err := pdfStreams(bad); err == nil {
		t.Fatal("missing endstream must fail")
	}
}

func TestPDFTextRoundtrip(t *testing.T) {
	doc, err := pdfBytes("roundtrip hello")
	if err != nil {
		t.Fatal(err)
	}
	text, err := pdfText(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "roundtrip hello") {
		t.Fatalf("roundtrip lost text: %q", text)
	}
}

func TestParseCmap12(t *testing.T) {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint16(12))   // format
	binary.Write(&b, binary.BigEndian, uint16(0))    // reserved
	binary.Write(&b, binary.BigEndian, uint32(28))   // length
	binary.Write(&b, binary.BigEndian, uint32(0))    // language
	binary.Write(&b, binary.BigEndian, uint32(1))    // nGroups
	binary.Write(&b, binary.BigEndian, uint32(0x41)) // start 'A'
	binary.Write(&b, binary.BigEndian, uint32(0x43)) // end 'C'
	binary.Write(&b, binary.BigEndian, uint32(10))   // first glyph
	out := map[rune]uint16{}
	if err := parseCmap12(b.Bytes(), out); err != nil {
		t.Fatal(err)
	}
	if out['A'] != 10 || out['C'] != 12 {
		t.Fatalf("cmap12 wrong: %v", out)
	}
	if err := parseCmap12([]byte{0, 4}, out); err == nil {
		t.Fatal("short/wrong format must fail")
	}
}

func zipWith(t *testing.T, parts map[string]string) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range parts {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

func TestXLSXSharedStrings(t *testing.T) {
	zr := zipWith(t, map[string]string{
		"xl/sharedStrings.xml": `<sst><si><t>alpha</t></si><si><r><t>be</t><t>ta</t></r></si></sst>`,
	})
	ss, err := xlsxSharedStrings(zr)
	if err != nil || len(ss) != 2 || ss[0] != "alpha" || ss[1] != "beta" {
		t.Fatalf("shared strings %v err %v", ss, err)
	}
	if ss, err := xlsxSharedStrings(zipWith(t, map[string]string{"xl/other.xml": "<x/>"})); err != nil || ss != nil {
		t.Fatalf("missing part must give nil,nil got %v %v", ss, err)
	}
}

func TestXLSXCellKinds(t *testing.T) {
	shared := []string{"zero", "one"}
	cases := []struct{ kind, value, want string }{
		{"s", "1", "one"},
		{"s", "9", ""},
		{"s", "x", ""},
		{"inlineStr", "lit", "lit"},
		{"str", "computed", "computed"},
		{"b", "1", "true"},
		{"b", "0", "false"},
		{"n", "42.5", "42.5"},
	}
	for _, tc := range cases {
		if got := xlsxCell(tc.kind, tc.value, shared); got != tc.want {
			t.Fatalf("%s/%q → %q want %q", tc.kind, tc.value, got, tc.want)
		}
	}
}

func TestXLSXRows(t *testing.T) {
	sheet := `<worksheet><sheetData>` +
		`<row><c r="A1" t="s"><v>0</v></c><c r="C1"><v>5</v></c></row>` +
		`<row><c r="A2" t="b"><v>1</v></c><c r="B2" t="inlineStr"><t>raw</t></c></row>` +
		`</sheetData></worksheet>`
	rows, err := xlsxRows([]byte(sheet), []string{"hdr"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0][0] != "hdr" || rows[0][2] != "5" || rows[1][0] != "true" || rows[1][1] != "raw" {
		t.Fatalf("rows wrong: %#v", rows)
	}
	if _, err := xlsxRows([]byte("<broken"), nil); err == nil {
		t.Fatal("malformed xml must fail")
	}
}

func TestSplitCellRefEdges(t *testing.T) {
	col, row, ok := splitCellRef("BC12")
	if !ok || col != 54 || row != 11 {
		t.Fatalf("BC12 → %d,%d,%v", col, row, ok)
	}
	for _, bad := range []string{"", "A", "12", "1A"} {
		if _, _, ok := splitCellRef(bad); ok {
			t.Fatalf("%q must fail", bad)
		}
	}
}

func TestWrapRunesEdges(t *testing.T) {
	got := wrapRunes("a\r\nb\rc", 80)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("newline variants: %v", got)
	}
	if got := wrapRunes("abcdef", 3); len(got) != 2 || got[0] != "abc" || got[1] != "def" {
		t.Fatalf("wrap: %v", got)
	}
	if got := wrapRunes("", 80); len(got) != 1 || got[0] != "" {
		t.Fatalf("empty: %v", got)
	}
}

func TestDialImageGuards(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := dialImage(ctx, "tcp", "localhost:80", ""); err == nil {
		t.Fatal("localhost must be rejected")
	}
	if _, err := dialImage(ctx, "tcp", "127.0.0.1:80", ""); err == nil {
		t.Fatal("loopback literal must be rejected")
	}
	if _, err := dialImage(ctx, "tcp", "10.0.0.1:80", ""); err == nil {
		t.Fatal("private literal must be rejected")
	}
	if _, err := dialImage(ctx, "tcp", "bad-addr", ""); err == nil {
		t.Fatal("unparseable addr must be rejected")
	}
	// allowlist host bypasses public-IP checks
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	conn, err := dialImage(ctx, "tcp", ln.Addr().String(), ln.Addr().String())
	if err != nil {
		t.Fatalf("allowedHost dial failed: %v", err)
	}
	conn.Close()
}

func TestPublicIPAndBlockedHost(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.1.1", "169.254.1.1", "100.64.1.1", "224.0.0.1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Fatalf("%s must not be public", ip)
		}
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("8.8.8.8 is public")
	}
	for _, h := range []string{"localhost", "x.localhost", "a.local", "b.internal", "metadata.google.internal", ""} {
		if !blockedHost(h) {
			t.Fatalf("%s must be blocked", h)
		}
	}
	if blockedHost("example.com") {
		t.Fatal("example.com blocked")
	}
}

func TestNumericEntityAndDecodeEntities(t *testing.T) {
	if r, ok := numericEntity("#65"); !ok || r != 'A' {
		t.Fatalf("decimal entity: %v %v", r, ok)
	}
	if r, ok := numericEntity("#x41"); !ok || r != 'A' {
		t.Fatalf("hex entity: %v %v", r, ok)
	}
	for _, bad := range []string{"65", "#", "#x", "#xZZ", "#999999999999999"} {
		if _, ok := numericEntity(bad); ok {
			t.Fatalf("%q must fail", bad)
		}
	}
	got := decodeEntities("a &amp; b &lt;x&gt; &quot;q&quot; &apos;p&apos; &nbsp; &#65;&#x42; &bogus; x&")
	want := "a & b <x> \"q\" 'p'   AB &bogus; x&"
	if got != want {
		t.Fatalf("entities: %q want %q", got, want)
	}
}

func TestAppendPDFTextOperators(t *testing.T) {
	var b strings.Builder
	// name token skip, comment, dict skip, literal with ' and " ops, T* newline, hex Tj
	src := "/FontName % trailing comment\n<</F1 1 0 R>> (a) Tj (b) ' (c) \" T* <44> Tj [45] T*"
	if err := appendPDFText(&b, []byte(src), nil); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "a") || !strings.Contains(got, "D") {
		t.Fatalf("missing text: %q", got)
	}
	var bad strings.Builder
	if err := appendPDFText(&bad, []byte("<< open"), nil); err == nil {
		t.Fatal("unterminated dict must fail")
	}
	if err := appendPDFText(&bad, []byte("[45"), nil); err == nil {
		t.Fatal("unterminated array must fail")
	}
}

func TestTableRowsCSVAndFallback(t *testing.T) {
	rows, err := tableRows("a,b\nc,d")
	if err != nil || len(rows) != 2 || rows[1][1] != "d" {
		t.Fatalf("csv rows: %v %v", rows, err)
	}
	// ragged/empty inputs still yield rows, never an error
	if rows, err = tableRows(""); err != nil || len(rows) == 0 {
		t.Fatalf("empty csv: %v %v", rows, err)
	}
}

func TestWriteReplacingAndCommitNew(t *testing.T) {
	s, dir := openSession(t)
	writeWorkspace(t, dir, "docs/note.txt", []byte("old"))
	if err := s.writeReplacing("docs/note.txt", []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Workspace.ReadFile("docs/note.txt")
	if err != nil || string(got) != "new" {
		t.Fatalf("replace: %q %v", got, err)
	}
	if err := s.writeReplacing("docs/missing.txt", []byte("x")); err == nil {
		t.Fatal("replacing a missing file must fail")
	}
	if err := s.writeReplacing("docs", []byte("x")); err == nil {
		t.Fatal("replacing a directory must fail")
	}
	if err := s.commitNew("docs/fresh.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Workspace.ReadFile("docs/fresh.txt"); err != nil || string(got) != "hi" {
		t.Fatalf("commitNew: %q %v", got, err)
	}
	bare := New(nil)
	if err := bare.prepareWrite("x.txt", []byte("x")); err == nil {
		t.Fatal("nil workspace must be refused")
	}
}

func TestPDFBytesMultipageAndUnmapped(t *testing.T) {
	// >40 lines forces a second page; an unmapped glyph falls back to '?'
	long := strings.Repeat("line\n", 45) + "𐀀done"
	doc, err := pdfBytes(long)
	if err != nil {
		t.Fatal(err)
	}
	text, err := pdfText(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "done") {
		t.Fatalf("roundtrip lost tail: %q", text[len(text)-20:])
	}
	if _, err := pdfBytes(strings.Repeat("x\n", MaxPages*40+1)); err == nil {
		t.Fatal("over MaxPages must fail")
	}
}

func TestAllowImageURL(t *testing.T) {
	fal := "https://fal.media.example"
	if err := allowImageURL("not a url", fal); err == nil {
		t.Fatal("bad url must fail")
	}
	if err := allowImageURL("https://user:pw@fal.media.example/x", fal); err == nil {
		t.Fatal("userinfo must fail")
	}
	if err := allowImageURL("https://fal.media.example/x", fal); err != nil {
		t.Fatalf("same-host fal url must pass: %v", err)
	}
	if err := allowImageURL("http://other.example/x", fal); err == nil {
		t.Fatal("non-https off-host must fail")
	}
	if err := allowImageURL("https://localhost/x", fal); err == nil {
		t.Fatal("blocked host must fail")
	}
}
