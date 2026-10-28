package communication

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// Harness-side skill expansion (SPEC-0083 R6.5): `/name args` and `$name`
// resolve against the mounted read-only effective view before the text ever
// reaches the model. Activated content arrives wrapped in <skill_content>
// with $ARGUMENTS/$0..$n substituted and the re-load instruction attached.

// skillIndex loads the mounted effective index; a missing index means "no
// skills" — the caller falls back to the plain unknown-command reply.
func (g *Gateway) skillIndex() *skillstore.EffectiveIndex {
	if g.config.SkillsDir == "" {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(g.config.SkillsDir, "index.json"))
	if err != nil {
		return nil
	}
	var idx skillstore.EffectiveIndex
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil
	}
	return &idx
}

func skillEntry(idx *skillstore.EffectiveIndex, name string) *skillstore.Entry {
	if idx == nil {
		return nil
	}
	for i := range idx.Skills {
		if idx.Skills[i].Name == name {
			return &idx.Skills[i]
		}
	}
	return nil
}

// skillActivationText reads the skill body, applies $ARGUMENTS/$0..$n
// substitution and wraps it for the job envelope.
func (g *Gateway) skillActivationText(e *skillstore.Entry, args []string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(g.config.SkillsDir, e.Name, skillstore.SkillFile))
	if err != nil {
		return "", err
	}
	body := substituteSkillArgs(skillBody(raw), args)
	return fmt.Sprintf("<skill_content name=%q source=%q revision=%q>\n%s\n</skill_content>\nRe-load this skill with the skill tool if its body left context.", e.Name, e.Source, e.Revision, body), nil
}

// skillBody strips the frontmatter block from a SKILL.md.
func skillBody(raw []byte) string {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	return strings.TrimLeft(rest[end+4:], "\n")
}

// skillSessionFor returns the per-(principal, task) activation session,
// pinning the catalog revision at first touch (R6.3). Callers hold no lock;
// the helper locks internally and returns the shared record.
func (g *Gateway) skillSessionFor(principalID, taskID string, idx *skillstore.EffectiveIndex) *skillSession {
	g.skillMu.Lock()
	defer g.skillMu.Unlock()
	if g.skillActivated == nil {
		g.skillActivated = map[string]*skillSession{}
	}
	key := principalID + ":" + taskID
	session := g.skillActivated[key]
	if session == nil {
		session = &skillSession{Revision: idx.Revision}
		g.skillActivated[key] = session
	}
	return session
}

// expandSkillRefs rewrites a normal message: every `$name` token naming a
// skill appends a <skill_content> activation block, stopping at the first
// token that names no skill (R6.5). Activations dedupe per session.
func (g *Gateway) expandSkillRefs(principalID, taskID, text string) string {
	idx := g.skillIndex()
	if idx == nil {
		return text
	}
	g.skillMu.Lock()
	defer g.skillMu.Unlock()
	if g.skillActivated == nil {
		g.skillActivated = map[string]*skillSession{}
	}
	key := principalID + ":" + taskID
	session := g.skillActivated[key]
	if session == nil {
		session = &skillSession{Revision: idx.Revision}
		g.skillActivated[key] = session
	}
	activated := session.activatedSet()
	prefix := ""
	if session.Revision != idx.Revision {
		// The session pin holds the catalog byte-stable; a publish mid-session
		// arrives once as a note on the next message (R6.3).
		prefix = "<skills_catalog_updated revision=\"" + idx.Revision + "\">The skills catalog changed during this session; skills_search reflects the current set.</skills_catalog_updated>\n\n"
		session.Revision = idx.Revision
	}
	var extra strings.Builder
	expanded := 0
	for i := 0; i < len(text) && expanded < 5; i++ {
		if text[i] != '$' {
			continue
		}
		j := i + 1
		for j < len(text) && (text[j] == '-' || text[j] == '_' ||
			(text[j] >= 'a' && text[j] <= 'z') || (text[j] >= 'A' && text[j] <= 'Z') ||
			(text[j] >= '0' && text[j] <= '9')) {
			j++
		}
		if j == i+1 {
			if i+1 < len(text) && text[i+1] == '$' {
				i++ // $$ is a literal escape, not a reference
			}
			continue
		}
		e := skillEntry(idx, text[i+1:j])
		if e == nil {
			break // first non-skill token ends the scan
		}
		i = j - 1
		if e.Hidden || !e.Eligible {
			extra.WriteString("\n\n<skill_unavailable name=\"" + e.Name + "\"/>")
			continue
		}
		if activated[e.Name] {
			continue
		}
		block, err := g.skillActivationText(e, nil)
		if err != nil {
			continue
		}
		extra.WriteString("\n\n" + block)
		activated[e.Name] = true
		session.Activated = append(session.Activated, e.Name)
		expanded++
	}
	return prefix + text + extra.String()
}

// skillSession is the per-(principal, task) activation state: the pinned
// catalog revision (R6.3) plus activated skill names (R6.5 dedupe).
type skillSession struct {
	Revision  string
	Activated []string
}

func (s *skillSession) activatedSet() map[string]bool {
	out := map[string]bool{}
	for _, n := range s.Activated {
		out[n] = true
	}
	return out
}

// substituteSkillArgs fills $ARGUMENTS and $0..$n placeholders.
func substituteSkillArgs(body string, args []string) string {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] != '$' {
			b.WriteByte(body[i])
			continue
		}
		if strings.HasPrefix(body[i:], "$ARGUMENTS") {
			b.WriteString(strings.Join(args, " "))
			i += len("$ARGUMENTS") - 1
			continue
		}
		j := i + 1
		for j < len(body) && body[j] >= '0' && body[j] <= '9' {
			j++
		}
		if j == i+1 {
			b.WriteByte('$')
			continue
		}
		if n, err := strconv.Atoi(body[i+1 : j]); err == nil && n < len(args) {
			b.WriteString(args[n])
		}
		i = j - 1
	}
	return b.String()
}
