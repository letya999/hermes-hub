package communication

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// ExtractStep is one deterministic transform in a probe extract pipeline
// (SPEC-0059): each step maps the fetched bytes to new bytes, in order, and
// the last step's output is the compared value. Steps are pure functions —
// they never fetch, execute, or see the wall clock.

const extractMaxSteps = 8

var extractStepKinds = map[string]bool{
	"css": true, "html2text": true, "regex": true, "grep": true,
	"drop_regex": true, "drop_lines": true, "sort_lines": true, "strip": true,
	"json_path": true, "rss": true,
}

// validateExtractStep rejects steps that can never extract at create time.
func validateExtractStep(step ExtractStep) error {
	if !extractStepKinds[step.Kind] {
		return errors.New("invalid extract step kind")
	}
	switch step.Kind {
	case "css":
		if strings.TrimSpace(step.Selector) == "" {
			return errors.New("css step needs a selector")
		}
	case "regex":
		re, err := regexp.Compile(step.Pattern)
		if err != nil || re.NumSubexp() == 0 {
			return errors.New("regex step needs a compiling capture group")
		}
	case "json_path":
		if _, err := parseJSONPath(step.Path); err != nil {
			return err
		}
	}
	return nil
}

// extractValue reduces a fetched body to the compared watch value by running
// the probe's extract steps in order.
func extractValue(probe *WatchProbe, body []byte) (string, error) {
	if probe == nil {
		return "", errors.New("probe required")
	}
	if probe.Kind == "github_issue" {
		var issue struct {
			State string `json:"state"`
		}
		if json.Unmarshal(body, &issue) != nil || (issue.State != "open" && issue.State != "closed") {
			return "", errors.New("invalid GitHub issue response")
		}
		return issue.State, nil
	}
	if len(probe.Extract) == 0 && probe.Regex != "" {
		re, err := regexp.Compile(probe.Regex)
		if err != nil {
			return "", err
		}
		if re.NumSubexp() == 0 || re.FindSubmatch(body) == nil {
			return "", errors.New("probe did not match")
		}
		return watchValue(body, re), nil
	}
	current := body
	for i, step := range probe.Extract {
		out, err := applyExtractStep(step, current)
		if err != nil {
			return "", fmt.Errorf("extract step %d (%s): %w", i, step.Kind, err)
		}
		current = out
	}
	if len(probe.Extract) > 0 && len(current) == 0 {
		return "", errors.New("extract produced an empty value")
	}
	return string(current), nil
}

func applyExtractStep(step ExtractStep, in []byte) ([]byte, error) {
	switch step.Kind {
	case "css":
		sel, err := cascadia.Parse(step.Selector)
		if err != nil {
			return nil, err
		}
		doc, err := html.Parse(bytes.NewReader(in))
		if err != nil {
			return nil, err
		}
		var parts []string
		for _, n := range cascadia.QueryAll(doc, sel) {
			if t := strings.TrimSpace(nodeText(n)); t != "" {
				parts = append(parts, t)
			}
		}
		return []byte(strings.Join(parts, "\n")), nil
	case "html2text":
		doc, err := html.Parse(bytes.NewReader(in))
		if err != nil {
			return nil, err
		}
		return []byte(nodeText(doc)), nil
	case "regex":
		re, err := regexp.Compile(step.Pattern)
		if err != nil {
			return nil, err
		}
		if m := re.FindSubmatch(in); len(m) > 1 {
			return m[1], nil
		}
		return nil, errors.New("pattern did not match")
	case "grep":
		re, err := regexp.Compile(step.Pattern)
		if err != nil {
			return nil, err
		}
		var kept []string
		for _, line := range strings.Split(string(in), "\n") {
			if re.MatchString(line) {
				kept = append(kept, line)
			}
		}
		return []byte(strings.Join(kept, "\n")), nil
	case "drop_regex":
		re, err := regexp.Compile(step.Pattern)
		if err != nil {
			return nil, err
		}
		return re.ReplaceAll(in, nil), nil
	case "drop_lines":
		re, err := regexp.Compile(step.Pattern)
		if err != nil {
			return nil, err
		}
		var kept []string
		for _, line := range strings.Split(string(in), "\n") {
			if !re.MatchString(line) {
				kept = append(kept, line)
			}
		}
		return []byte(strings.Join(kept, "\n")), nil
	case "sort_lines":
		lines := strings.Split(string(in), "\n")
		sort.Strings(lines)
		return []byte(strings.Join(lines, "\n")), nil
	case "rss":
		return extractFeed(in)
	case "json_path":
		segs, err := parseJSONPath(step.Path)
		if err != nil {
			return nil, err
		}
		var data any
		if json.Unmarshal(in, &data) != nil {
			return nil, errors.New("body is not JSON")
		}
		return evalJSONPath(segs, data)
	case "strip":
		var kept []string
		for _, line := range strings.Split(string(in), "\n") {
			if t := strings.TrimSpace(line); t != "" {
				kept = append(kept, t)
			}
		}
		return []byte(strings.Join(kept, "\n")), nil
	}
	return nil, fmt.Errorf("unknown extract step %q", step.Kind)
}

// jsonPathSeg is one hop of a $.a.b[0].c path: a key name, an index, or a
// wildcard that fans out over array elements (or object values, sorted by key
// for determinism).
type jsonPathSeg struct {
	key     string
	index   int
	isIndex bool
	isWild  bool
}

func parseJSONPath(path string) ([]jsonPathSeg, error) {
	p := strings.TrimSpace(path)
	if !strings.HasPrefix(p, "$") || len(p) > 512 {
		return nil, errors.New("json_path must start with $")
	}
	p = strings.TrimPrefix(p, "$")
	var segs []jsonPathSeg
	for p != "" {
		if strings.HasPrefix(p, ".") {
			p = p[1:]
			end := strings.IndexAny(p, ".[")
			if end < 0 {
				end = len(p)
			}
			key := p[:end]
			if key == "" || len(key) > 128 || strings.ContainsAny(key, `"'[]`) {
				return nil, errors.New("invalid json_path key")
			}
			segs = append(segs, jsonPathSeg{key: key})
			p = p[end:]
			continue
		}
		if strings.HasPrefix(p, "[") {
			end := strings.Index(p, "]")
			if end < 0 {
				return nil, errors.New("unclosed json_path bracket")
			}
			inner := p[1:end]
			if inner == "*" {
				segs = append(segs, jsonPathSeg{isWild: true})
			} else {
				i, err := strconv.Atoi(inner)
				if err != nil || i < 0 || i > 100000 {
					return nil, errors.New("invalid json_path index")
				}
				segs = append(segs, jsonPathSeg{index: i, isIndex: true})
			}
			p = p[end+1:]
			continue
		}
		return nil, errors.New("invalid json_path syntax")
	}
	return segs, nil
}

// evalJSONPath resolves segments; a wildcard flattens matching nodes. Scalars
// are rendered in JSON form, objects/arrays re-encoded compactly.
func evalJSONPath(segs []jsonPathSeg, data any) ([]byte, error) {
	nodes := []any{data}
	for _, seg := range segs {
		var next []any
		for _, node := range nodes {
			switch {
			case seg.isWild:
				switch v := node.(type) {
				case []any:
					next = append(next, v...)
				case map[string]any:
					keys := make([]string, 0, len(v))
					for k := range v {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						next = append(next, v[k])
					}
				}
			case seg.isIndex:
				arr, ok := node.([]any)
				if !ok || seg.index >= len(arr) {
					return nil, errors.New("json_path index out of range")
				}
				next = append(next, arr[seg.index])
			default:
				obj, ok := node.(map[string]any)
				if !ok {
					return nil, errors.New("json_path hit a non-object")
				}
				v, ok := obj[seg.key]
				if !ok {
					return nil, errors.New("json_path key missing")
				}
				next = append(next, v)
			}
		}
		if len(next) == 0 {
			return nil, errors.New("json_path matched nothing")
		}
		nodes = next
	}
	var parts []string
	for _, node := range nodes {
		if s, ok := node.(string); ok {
			parts = append(parts, s)
			continue
		}
		b, err := json.Marshal(node)
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(b))
	}
	return []byte(strings.Join(parts, "\n")), nil
}

// extractFeed canonicalizes an RSS 2.0 or Atom feed into "title|link|date"
// lines, one per entry in document order (SPEC-0059).
func extractFeed(in []byte) ([]byte, error) {
	type rssItem struct {
		Title   string `xml:"title"`
		Link    string `xml:"link"`
		PubDate string `xml:"pubDate"`
	}
	var rss struct {
		Items []rssItem `xml:"channel>item"`
	}
	if err := xml.Unmarshal(in, &rss); err == nil && len(rss.Items) > 0 {
		var b strings.Builder
		for i, item := range rss.Items {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "%s|%s|%s", strings.TrimSpace(item.Title), strings.TrimSpace(item.Link), strings.TrimSpace(item.PubDate))
		}
		return []byte(b.String()), nil
	}
	type atomLink struct {
		Href string `xml:"href,attr"`
	}
	type atomEntry struct {
		Title   string     `xml:"title"`
		Links   []atomLink `xml:"link"`
		Updated string     `xml:"updated"`
	}
	var atom struct {
		Entries []atomEntry `xml:"entry"`
	}
	if err := xml.Unmarshal(in, &atom); err != nil {
		return nil, err
	}
	if len(atom.Entries) == 0 {
		return nil, errors.New("no feed entries")
	}
	var b strings.Builder
	for i, entry := range atom.Entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		link := ""
		if len(entry.Links) > 0 {
			link = entry.Links[0].Href
		}
		fmt.Fprintf(&b, "%s|%s|%s", strings.TrimSpace(entry.Title), link, strings.TrimSpace(entry.Updated))
	}
	return []byte(b.String()), nil
}

// nodeText is the visible-text view of an HTML subtree: script content is
// dropped.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
