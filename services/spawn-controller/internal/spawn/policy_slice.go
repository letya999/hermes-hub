package spawn

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/shared/store"
)

// bundlePassports parses the stored canonical policy bundle into its six
// passport documents.
func bundlePassports(canonical []byte) (map[string]json.RawMessage, error) {
	var bundle struct {
		Passports map[string]json.RawMessage `json:"passports"`
	}
	if err := json.Unmarshal(canonical, &bundle); err != nil {
		return nil, err
	}
	return bundle.Passports, nil
}

// projectSlice returns the subset of the effective passports the consumers
// registry attributes to service: every document keeps its envelope while
// only the service's attributed leaf subtrees remain. An unknown consumer
// name — or one the spec attributes nothing to — is not_found.
func projectSlice(docs map[string]json.RawMessage, service string) (map[string]json.RawMessage, error) {
	if !knownConsumer(service) {
		return nil, fmt.Errorf("%w: unknown service %q", store.ErrNotFound, service)
	}
	out := map[string]json.RawMessage{}
	for _, doc := range passport.Names {
		raw, ok := docs[doc]
		if !ok {
			continue
		}
		var full map[string]any
		if err := json.Unmarshal(raw, &full); err != nil {
			return nil, err
		}
		envelope := map[string]any{}
		body := map[string]any{}
		for k, v := range full {
			if k == "apiVersion" || k == "kind" || k == "metadata" {
				envelope[k] = v
				continue
			}
			body[k] = v
		}
		flat := map[string]any{}
		flattenJSON(body, "", flat)
		kept := map[string]any{}
		for p, v := range flat {
			if consumerOf(doc, p) == service {
				setPathJSON(kept, p, v)
			}
		}
		if len(kept) == 0 {
			continue
		}
		for k, v := range envelope {
			kept[k] = v
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			return nil, err
		}
		out[doc] = encoded
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: unknown service %q", store.ErrNotFound, service)
	}
	return out, nil
}

func knownConsumer(name string) bool {
	for _, c := range passport.Consumers {
		if c.Name == name {
			return true
		}
	}
	return false
}

// consumerOf resolves which consumer a leaf belongs to: exact key, then
// the most specific segment-wise wildcard match, then the longest ancestor
// prefix (a map leaf's key covers its subtree — "config.ports" owns
// "config.ports.browser-vnc").
func consumerOf(doc, path string) string {
	key := doc + "." + path
	if c, ok := passport.Consumers[key]; ok {
		return c.Name
	}
	segs := strings.Split(key, ".")
	best := ""
	bestScore := -1
	for k, c := range passport.Consumers {
		if !strings.HasPrefix(k, doc+".") {
			continue
		}
		pat := strings.Split(k, ".")
		if len(pat) != len(segs) {
			continue
		}
		score := 0
		match := true
		for i, s := range pat {
			if s == "*" {
				continue
			}
			if s != segs[i] {
				match = false
				break
			}
			score++
		}
		if match && score > bestScore {
			bestScore = score
			best = c.Name
		}
	}
	if best != "" {
		return best
	}
	for prefix := key; ; {
		i := strings.LastIndexByte(prefix, '.')
		if i <= 0 {
			return ""
		}
		prefix = prefix[:i]
		if c, ok := passport.Consumers[prefix]; ok {
			return c.Name
		}
	}
}

// flattenJSON / setPathJSON mirror the compiler's tree helpers for the
// projection — kept local so the contract surface needs no passport
// internals.
func flattenJSON(tree map[string]any, prefix string, out map[string]any) {
	for k, v := range tree {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if m, ok := v.(map[string]any); ok {
			flattenJSON(m, p, out)
			continue
		}
		out[p] = v
	}
}

func setPathJSON(body map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	m := body
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = value
}
