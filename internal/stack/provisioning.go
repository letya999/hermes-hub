package stack

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
)

// provisioningProfile is a resolved provisioning profile (SPEC-0083 R9.1):
// the per-file overlay of an extends chain — at each level the org copy is
// consulted before the platform copy, and the first occurrence of each file
// leaf→root wins. profile.yaml carries {description, extends, version}.
type provisioningProfile struct {
	Name      string
	Version   int
	Agent     []byte
	Workspace []byte
	Soul      []byte
	Grants    []byte
	Skills    []byte
	Layout    []byte
}

// resolveProvisioningProfile walks the extends chain for name. Org profiles
// (<orgDir>/profiles/<level>) override platform profiles (<hubRoot>/policies/
// profiles/<level>) per file at each chain level; the platform default then
// falls back to legacy policies/*-default.yaml and the embedded templates.
func resolveProvisioningProfile(spaceDir, orgDir, name string) (*provisioningProfile, error) {
	if !idPattern.MatchString(name) {
		return nil, fmt.Errorf("invalid provisioning profile %q", name)
	}
	root := filepath.Dir(spaceDir)
	if filepath.Base(root) == "spaces" {
		root = filepath.Dir(root)
	}
	p := &provisioningProfile{Name: name}
	dst := map[string]*[]byte{
		"agent.yaml": &p.Agent, "workspace.yaml": &p.Workspace, "SOUL.md": &p.Soul,
		"layout.yaml": &p.Layout, "grants.yaml": &p.Grants, "skills.yaml": &p.Skills,
	}
	seen := map[string]bool{}
	level, leaf := name, true
	for level != "" {
		if !idPattern.MatchString(level) || seen[level] {
			return nil, fmt.Errorf("invalid provisioning profile chain at %q", level)
		}
		seen[level] = true
		next := ""
		for _, dir := range profileLevelDirs(root, orgDir, level) {
			if next == "" {
				if b, err := os.ReadFile(filepath.Join(dir, "profile.yaml")); err == nil {
					var meta struct {
						Extends string `yaml:"extends"`
						Version int    `yaml:"version"`
					}
					if err := yaml.Unmarshal(b, &meta); err != nil {
						return nil, fmt.Errorf("profile %q: %w", level, err)
					}
					next = meta.Extends
					if leaf {
						p.Version = meta.Version
					}
				}
			}
			for file, out := range dst {
				if len(*out) == 0 {
					if b, err := os.ReadFile(filepath.Join(dir, file)); err == nil {
						*out = b
					}
				}
			}
		}
		leaf = false
		level = next
	}
	if len(p.Agent) == 0 {
		p.Agent = policyTemplate(spaceDir, "agent-default.yaml", defaultAgentYAML)
	}
	if len(p.Workspace) == 0 {
		p.Workspace = policyTemplate(spaceDir, "workspace-default.yaml", defaultWorkspaceYAML)
	}
	return p, nil
}

// profileLevelDirs lists the candidate dirs for one chain level, org first.
func profileLevelDirs(root, orgDir, level string) []string {
	dirs := []string{}
	if orgDir != "" {
		dirs = append(dirs, filepath.Join(orgDir, "profiles", level))
	}
	return append(dirs, filepath.Join(root, "policies", "profiles", level))
}

// resolveInitProfile picks the provisioning profile a new space stamps
// (SPEC-0083 R9.3): the operator flag, else the member's org assignment,
// else the org default, else "default".
func resolveInitProfile(dir, user, organization, flagProfile string) (*provisioningProfile, error) {
	orgDir := ""
	provision := flagProfile
	if organization != "" {
		orgDir = resolvedOrganizationDir(dir, organization)
		if provision == "" {
			if o, err := ReadOrganization(orgDir); err == nil {
				if m, ok := o.Members[user]; ok && m.Profile != "" {
					provision = m.Profile
				} else {
					provision = o.DefaultProfile
				}
			}
		}
	}
	if provision == "" {
		provision = "default"
	}
	return resolveProvisioningProfile(dir, orgDir, provision)
}

// SyncOrganizationMembers provisions one space per org member under
// spacesRoot (SPEC-0083 R9.3): each new space inits with the member's
// resolved profile; existing spaces are reported and never overwritten.
// dryRun reports the plan without writing.
func SyncOrganizationMembers(orgDir, organization, spacesRoot, environment string, dryRun bool) (map[string]any, error) {
	o, err := ReadOrganization(orgDir)
	if err != nil {
		return nil, err
	}
	members := make([]string, 0, len(o.Members))
	for user := range o.Members {
		members = append(members, user)
	}
	slices.Sort(members)
	created, existing := []string{}, []string{}
	for _, user := range members {
		spaceDir := filepath.Join(spacesRoot, user)
		if _, err := os.Lstat(filepath.Join(spaceDir, "scope.yaml")); err == nil {
			existing = append(existing, user)
			continue
		}
		if dryRun {
			created = append(created, user)
			continue
		}
		if err := initScopeEnvironmentProfile(spaceDir, user, environment, organization, UserScope, ""); err != nil {
			return nil, err
		}
		created = append(created, user)
	}
	return map[string]any{
		"organization": organization, "dry_run": dryRun,
		"created": created, "existing": existing,
	}, nil
}

// ProfileGrantRules parses the profile's grants.yaml template into capability
// rules (SPEC-0083 R9.5). The rules are a draft only: they become policy
// defaults through the confirmed operator record path and Validate rejects
// any rule outside the organization ceiling.
func ProfileGrantRules(p *provisioningProfile) ([]api.CapabilityRule, error) {
	if len(p.Grants) == 0 {
		return nil, nil
	}
	var doc struct {
		Rules []struct {
			CapabilityID         string `yaml:"capability_id"`
			ImplementationDigest string `yaml:"implementation_digest"`
			Action               string `yaml:"action"`
			Resource             string `yaml:"resource"`
			ConnectionID         string `yaml:"connection_id"`
			PathPrefix           string `yaml:"path_prefix"`
			Limits               struct {
				OutputBytes    int `yaml:"output_bytes"`
				TimeoutSeconds int `yaml:"timeout_seconds"`
			} `yaml:"limits"`
		} `yaml:"rules"`
	}
	if err := yaml.Unmarshal(p.Grants, &doc); err != nil {
		return nil, fmt.Errorf("profile %q grants: %w", p.Name, err)
	}
	rules := make([]api.CapabilityRule, 0, len(doc.Rules))
	for _, r := range doc.Rules {
		rules = append(rules, api.CapabilityRule{
			CapabilityID: r.CapabilityID, ImplementationDigest: r.ImplementationDigest,
			Action: r.Action, Resource: r.Resource, ConnectionID: r.ConnectionID,
			PathPrefix: r.PathPrefix,
			Limits:     api.CapabilityLimits{OutputBytes: r.Limits.OutputBytes, TimeoutSeconds: r.Limits.TimeoutSeconds},
		})
	}
	return rules, nil
}

// ProfileGrantRulesForSpace resolves the named profile for a space and
// returns its grants.yaml rules (R9.5).
func ProfileGrantRulesForSpace(dir, name string) ([]api.CapabilityRule, error) {
	p, err := resolveProvisioningProfile(dir, "", name)
	if err != nil {
		return nil, err
	}
	return ProfileGrantRules(p)
}

// ProfileSkillPins parses the profile's skills.yaml into pinned skill
// references (SPEC-0083 R9.6). The render pins them to the front of the
// effective catalog; a reference that resolves to no eligible entry fails.
func ProfileSkillPins(p *provisioningProfile) ([]skillstore.PinnedRef, error) {
	if len(p.Skills) == 0 {
		return nil, nil
	}
	var doc struct {
		Skills []string `yaml:"skills"`
	}
	if err := yaml.Unmarshal(p.Skills, &doc); err != nil {
		return nil, fmt.Errorf("profile %q skills: %w", p.Name, err)
	}
	refs := make([]skillstore.PinnedRef, 0, len(doc.Skills))
	for _, raw := range doc.Skills {
		refs = append(refs, skillstore.PinnedRef{Name: raw})
	}
	return refs, nil
}

// createProfileLayout materializes the profile's layout.yaml dirs inside the
// space (R9.3).
func createProfileLayout(dir string, p *provisioningProfile) error {
	if len(p.Layout) == 0 {
		return nil
	}
	var layout struct {
		Dirs []string `yaml:"dirs"`
	}
	if err := yaml.Unmarshal(p.Layout, &layout); err != nil {
		return fmt.Errorf("profile %q layout: %w", p.Name, err)
	}
	for _, rel := range layout.Dirs {
		if strings.Contains(rel, "..") {
			return fmt.Errorf("profile %q layout dir %q rejected", p.Name, rel)
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0700); err != nil {
			return err
		}
	}
	return nil
}

// UpdateProvisioningProfile performs the R9.7 three-way profile update: each
// stamped file is compared against the profile.lock baseline and the newly
// resolved profile content — files the owner never touched take the new
// content, customized files stay untouched and are reported. apply=false
// reports the plan without writing; apply=true commits and rewrites
// profile.lock to the new profile baseline.
func UpdateProvisioningProfile(dir string, apply bool) (map[string]any, error) {
	lock, err := readProvisioningLock(dir)
	if err != nil {
		return nil, err
	}
	orgDir := ""
	if scope, err := ReadScope(dir); err == nil && scope.Organization != "" {
		orgDir = resolvedOrganizationDir(dir, scope.Organization)
	}
	p, err := resolveProvisioningProfile(dir, orgDir, lock.Profile)
	if err != nil {
		return nil, err
	}
	stamped := map[string][]byte{"agent.yaml": p.Agent, "workspace.yaml": p.Workspace}
	updates, customized := []string{}, []string{}
	for file, body := range stamped {
		cur, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			return nil, err
		}
		if fmt.Sprintf("sha256:%x", sha256.Sum256(cur)) != lock.Files[file] {
			customized = append(customized, file)
			continue
		}
		if string(cur) != string(body) {
			updates = append(updates, file)
		}
	}
	slices.Sort(updates)
	slices.Sort(customized)
	if apply {
		for _, file := range updates {
			if err := atomic(filepath.Join(dir, file), stamped[file]); err != nil {
				return nil, err
			}
		}
		lock.Version = p.Version
		for file, body := range stamped {
			lock.Files[file] = fmt.Sprintf("sha256:%x", sha256.Sum256(body))
		}
		raw, err := yaml.Marshal(lock)
		if err != nil {
			return nil, err
		}
		if err := atomic(filepath.Join(dir, "profile.lock"), raw); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"profile": p.Name, "version": p.Version, "apply": apply,
		"updates": updates, "customized": customized,
	}, nil
}

// provisioningLock records what init stamped so `profile apply` can tell a
// user-customized file from an untouched one (SPEC-0083 R9.7).
type provisioningLock struct {
	Profile string            `yaml:"profile"`
	Version int               `yaml:"version"`
	Files   map[string]string `yaml:"files"`
}

func readProvisioningLock(dir string) (provisioningLock, error) {
	var lock provisioningLock
	b, err := os.ReadFile(filepath.Join(dir, "profile.lock"))
	if err != nil {
		return lock, err
	}
	return lock, yaml.Unmarshal(b, &lock)
}

func writeProvisioningLock(dir string, p *provisioningProfile, stamped map[string][]byte) error {
	lock := provisioningLock{Profile: p.Name, Version: p.Version, Files: map[string]string{}}
	for name, body := range stamped {
		lock.Files[name] = fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	}
	b, err := yaml.Marshal(lock)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "profile.lock"), b, 0600)
}
