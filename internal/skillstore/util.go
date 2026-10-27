package skillstore

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
)

// Revision is the content revision id of a tree: sha256 over a canonical
// path+bytes listing. Covers every file — references, scripts, assets —
// not just SKILL.md.
func Revision(tree Tree) string {
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	h := sha256.New()
	for _, path := range paths {
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(tree[path].Data)
		if tree[path].Exec {
			h.Write([]byte{1})
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func shortHash(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func randSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "x"
	}
	return hex.EncodeToString(b[:])
}

func stampNow() string { return time.Now().UTC().Format("20060102T150405.000000000Z") }

func mustJSON(v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return body
}

func jsonUnmarshal(body []byte, v any) error { return json.Unmarshal(body, v) }
