package media

import (
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

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
