package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

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
