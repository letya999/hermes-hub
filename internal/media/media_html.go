package media

import (
	"strings"
)

func htmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// htmlText drops well-formed script, style, noscript and comments.
// It is a text extract, not a browser.
func htmlText(s string) string {
	var b strings.Builder
	lower := strings.ToLower(s)
	i := 0
	for i < len(s) {
		if s[i] != '<' {
			next := strings.IndexByte(s[i:], '<')
			chunk := s[i:]
			if next >= 0 {
				chunk = s[i : i+next]
				i += next
			} else {
				i = len(s)
			}
			b.WriteString(decodeEntities(chunk))
			continue
		}
		if strings.HasPrefix(lower[i:], "<!--") {
			if j := strings.Index(lower[i+4:], "-->"); j >= 0 {
				i += 4 + j + 3
				continue
			}
			break
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break
		}
		raw := strings.TrimSpace(lower[i+1 : i+end])
		raw = strings.TrimSuffix(raw, "/")
		closing := strings.HasPrefix(raw, "/")
		raw = strings.TrimPrefix(raw, "/")
		name := raw
		if field, _, _ := strings.Cut(raw, " "); field != "" {
			name = field
		}
		i += end + 1
		if !closing && (name == "script" || name == "style" || name == "noscript") {
			closer := "</" + name
			j := strings.Index(lower[i:], closer)
			if j < 0 {
				break
			}
			rest := i + j + len(closer)
			k := strings.IndexByte(s[rest:], '>')
			if k < 0 {
				break
			}
			i = rest + k + 1
			continue
		}
		if name == "br" || name == "p" || name == "div" || name == "li" || name == "tr" {
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(collapseSpace(b.String()))
}

func collapseSpace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' || r == '\f' {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

func decodeEntities(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		semi := strings.IndexByte(s[i:], ';')
		if semi <= 1 || semi > 12 {
			b.WriteByte(s[i])
			i++
			continue
		}
		ent := s[i+1 : i+semi]
		i += semi + 1
		switch ent {
		case "amp":
			b.WriteByte('&')
		case "lt":
			b.WriteByte('<')
		case "gt":
			b.WriteByte('>')
		case "quot":
			b.WriteByte('"')
		case "apos":
			b.WriteByte('\'')
		case "nbsp":
			b.WriteByte(' ')
		default:
			if n, ok := numericEntity(ent); ok {
				b.WriteRune(n)
			} else {
				b.WriteByte('&')
				b.WriteString(ent)
				b.WriteByte(';')
			}
		}
	}
	return b.String()
}

func numericEntity(ent string) (rune, bool) {
	if len(ent) < 2 || ent[0] != '#' {
		return 0, false
	}
	n := 0
	if ent[1] == 'x' || ent[1] == 'X' {
		for _, r := range ent[2:] {
			n *= 16
			switch {
			case r >= '0' && r <= '9':
				n += int(r - '0')
			case r >= 'a' && r <= 'f':
				n += int(r-'a') + 10
			case r >= 'A' && r <= 'F':
				n += int(r-'A') + 10
			default:
				return 0, false
			}
		}
	} else {
		for _, r := range ent[1:] {
			if r < '0' || r > '9' {
				return 0, false
			}
			n = n*10 + int(r-'0')
		}
	}
	if n <= 0 || n > 0x10FFFF || n > 1<<21 {
		return 0, false
	}
	return rune(n), true
}
