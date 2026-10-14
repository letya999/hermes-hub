package media

import (
	"bytes"
	"errors"
	"fmt"
	"golang.org/x/image/font/gofont/goregular"
)

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
