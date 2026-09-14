package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
)

const Schema = 1

type Record struct {
	Name             string    `json:"name"`
	SourceURL        string    `json:"source_url,omitempty"`
	Digest           string    `json:"digest"`
	Scope            string    `json:"scope"`
	Capabilities     []string  `json:"capabilities,omitempty"`
	Scan             string    `json:"scan"`
	Consent          bool      `json:"consent"`
	EnabledRevision  uint64    `json:"enabled_revision"`
	RollbackRevision uint64    `json:"rollback_revision,omitempty"`
	Revoked          bool      `json:"revoked,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Registry struct {
	Schema int      `json:"schema"`
	User   string   `json:"user"`
	Skills []Record `json:"skills"`
}

func Path(home string) string {
	return filepath.Join(home, "skills", "registry.json")
}

func UserSkillsDir(home string) string {
	return filepath.Join(home, "hermes", "skills")
}

func Load(home string) (Registry, error) {
	var reg Registry
	b, err := os.ReadFile(Path(home))
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Schema: Schema}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	if json.Unmarshal(b, &reg) != nil || (reg.Schema != 0 && reg.Schema != Schema) {
		return Registry{}, errors.New("invalid skill registry")
	}
	if reg.Schema == 0 {
		reg.Schema = Schema
	}
	return reg, nil
}

func Save(home string, reg Registry) error {
	if err := os.MkdirAll(filepath.Join(home, "skills"), 0700); err != nil {
		return err
	}
	reg.Schema = Schema
	if reg.User == "" {
		reg.User = filepath.Base(filepath.Clean(home))
	}
	b, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path(home) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(home))
}

func ScanSkillText(text string) error {
	if envstore.LooksLikeEnv(text) {
		return errors.New("skill text must not grant provider credentials")
	}
	lower := strings.ToLower(text)
	for _, needle := range []string{"org_actions", "telegram_bot_token", "slack_signing_secret", "hub_toolhub", "provider mutation"} {
		if strings.Contains(lower, needle) {
			return errors.New("skill text must not grant provider mutation")
		}
	}
	return nil
}

func Install(home, name, source, scope string, body []byte, consent bool, caller identity.Envelope) (Record, error) {
	if caller.PrincipalID == "" || (scope == "user" && caller.PrincipalID != registryUser(home) && registryUser(home) != "") {
		if scope == "user" && !identity.ValidID(caller.PrincipalID) {
			return Record{}, errors.New("invalid owner")
		}
	}
	if !identity.ValidID(name) || (scope != "user" && scope != "org" && scope != "global") {
		return Record{}, errors.New("invalid skill identity")
	}
	if scope != "user" {
		return Record{}, errors.New("user runtime cannot write global or organization skills")
	}
	if !consent {
		return Record{}, errors.New("consent required")
	}
	if err := ScanSkillText(string(body)); err != nil {
		return Record{}, err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	dir := filepath.Join(UserSkillsDir(home), name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Record{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), body, 0600); err != nil {
		return Record{}, err
	}
	reg, err := Load(home)
	if err != nil {
		return Record{}, err
	}
	rec := Record{Name: name, SourceURL: source, Digest: digest, Scope: scope, Scan: "pass", Consent: true, EnabledRevision: 1, UpdatedAt: time.Now().UTC()}
	replaced := false
	for i, existing := range reg.Skills {
		if existing.Name == name {
			rec.EnabledRevision = existing.EnabledRevision + 1
			rec.RollbackRevision = existing.EnabledRevision
			reg.Skills[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		reg.Skills = append(reg.Skills, rec)
	}
	if err := Save(home, reg); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func Revoke(home, name string, caller identity.Envelope) error {
	if !identity.ValidID(name) || !identity.ValidID(caller.PrincipalID) {
		return errors.New("invalid identity")
	}
	reg, err := Load(home)
	if err != nil {
		return err
	}
	found := false
	for i, rec := range reg.Skills {
		if rec.Name != name {
			continue
		}
		if rec.Scope != "user" {
			return errors.New("curated skills cannot be revoked from a user runtime")
		}
		rec.Revoked = true
		rec.UpdatedAt = time.Now().UTC()
		reg.Skills[i] = rec
		found = true
	}
	if !found {
		return errors.New("skill not found")
	}
	if err := os.RemoveAll(filepath.Join(UserSkillsDir(home), name)); err != nil {
		return err
	}
	return Save(home, reg)
}

func Advertised(home string) ([]Record, error) {
	reg, err := Load(home)
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for _, rec := range reg.Skills {
		if rec.Revoked {
			continue
		}
		if rec.Scope == "user" {
			if _, err := os.Stat(filepath.Join(UserSkillsDir(home), rec.Name, "SKILL.md")); err != nil {
				continue
			}
		}
		out = append(out, rec)
	}
	return out, nil
}

func registryUser(home string) string {
	reg, err := Load(home)
	if err != nil {
		return ""
	}
	return reg.User
}

func CopyLimited(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, 256<<10))
}
