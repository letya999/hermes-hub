package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/selfsettings"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"gopkg.in/yaml.v3"
)

// runSettingsEdit is the governed settings publisher. The supervisor calls it
// after authenticating the control request; no runtime receives a host path.
func runSettingsEdit(ctx context.Context, dir, root, environment, key string, value *string, baseRevision string) (map[string]any, error) {
	path, ok := selfsettings.PublishedPath(key)
	if !ok || baseRevision == "" {
		return nil, errors.New("settings edit requires a reviewed key and base revision")
	}
	var canonical any
	if value != nil && strings.TrimSpace(*value) != "" {
		var err error
		canonical, err = selfsettings.ValidatePath(key, *value)
		if err != nil {
			return nil, err
		}
	}
	if environment != "dev" && environment != "prod" {
		return nil, errors.New("settings edit requires dev or prod environment")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(dir, "generated", "apply.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if !locked {
		return nil, errors.New("settings edit: another apply is in progress")
	}
	if _, err := os.Stat(applyJournalPath(dir)); err == nil || !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("settings edit: interrupted apply needs recovery")
	}
	current, err := passport.Current(dir)
	if err != nil || current != baseRevision {
		return nil, errors.New("settings edit: stale base revision")
	}
	previous, err := passport.LoadEffective(dir)
	if err != nil || previous.Header.Metadata.Environment != environment || previous.Header.Metadata.Space != filepath.Base(dir) {
		return nil, errors.New("settings edit: current passport unavailable")
	}
	docs, err := catalog.Generate()
	if err != nil {
		return nil, err
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub", "governance.json"))
	issued, err := toolhub.IssuedRightCeiling(gov, docs.Rights, filepath.Base(dir), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if !slices.Contains(issued["config"], "edit_own") || !slices.Contains(previous.Grants.Allow["config"], "edit_own") {
		return nil, errors.New("settings edit: config.edit_own is not current")
	}
	before, err := os.ReadFile(filepath.Join(dir, "passports", "config.yaml")) // #nosec G304 G703 -- fixed passport path under a validated space ID
	if err != nil {
		return nil, err
	}
	authored, err := passport.ReadAuthored(dir)
	if err != nil {
		return nil, err
	}
	afterRead, err := os.ReadFile(filepath.Join(dir, "passports", "config.yaml")) // #nosec G304 G703 -- fixed passport path under a validated space ID
	if err != nil || !bytes.Equal(before, afterRead) {
		return nil, errors.New("settings edit: authored config changed during read")
	}
	now := time.Now().UTC()
	compile := func() (passport.Revision, error) {
		return passport.CompileAuthoredDocs(dir, environment, now, docs, stack.ReservedSiblingPorts(dir), issued, authored)
	}
	baseline, err := compile()
	if err != nil {
		return nil, err
	}
	baseEffective, err := baseline.Effective()
	if err != nil || len(passport.Diff(previous, baseEffective)) != 0 {
		return nil, errors.New("settings edit: unpublished authored changes require space apply")
	}
	after, edited, err := patchAuthoredConfig(before, path, canonical)
	if err != nil || len(after) > 1<<20 {
		return nil, errors.New("settings edit: authored config exceeds size limit or cannot be edited")
	}
	authored["config"] = edited
	candidate, err := compile()
	if err != nil {
		return nil, err
	}
	result, err := candidate.Effective()
	if err != nil {
		return nil, err
	}
	changes := passport.Diff(previous, result)
	for _, change := range changes {
		if !strings.HasPrefix(change.Path, "config.") {
			return nil, errors.New("settings edit: candidate changes another passport")
		}
	}
	if len(changes) == 0 {
		return map[string]any{"published_revision": current, "previous_revision": current, "changed": false}, nil
	}
	actions := reconcile.Plan(changes, result.Stack.Services)
	if _, err := applyConfirm(ctx, dir, root, environment, candidate, current, actions, applyHandlers(ctx, dir, root, environment), authoredConfigUpdate{Before: before, After: after}); err != nil {
		return nil, fmt.Errorf("settings edit: %w", err)
	}
	return map[string]any{"published_revision": candidate.ID, "previous_revision": current, "changed": true}, nil
}

func patchAuthoredConfig(before []byte, path string, value any) ([]byte, map[string]any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(before, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("invalid authored config")
	}
	parts := strings.Split(path, ".")
	node := doc.Content[0]
	find := func(section *yaml.Node, name string) int {
		for i := 0; i+1 < len(section.Content); i += 2 {
			if section.Content[i].Value == name {
				return i
			}
		}
		return -1
	}
	for _, part := range parts[:len(parts)-1] {
		i := find(node, part)
		if i < 0 {
			if value == nil {
				var unchanged map[string]any
				if err := yaml.Unmarshal(before, &unchanged); err != nil {
					return nil, nil, err
				}
				return before, unchanged, nil
			}
			child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, child)
			node = child
			continue
		}
		if node.Content[i+1].Kind != yaml.MappingNode {
			return nil, nil, errors.New("authored config setting parent is not a map")
		}
		node = node.Content[i+1]
	}
	i := find(node, parts[len(parts)-1])
	if value == nil {
		if i >= 0 {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
		}
	} else {
		leaf := &yaml.Node{}
		if err := leaf.Encode(value); err != nil {
			return nil, nil, err
		}
		if i >= 0 {
			leaf.HeadComment = node.Content[i+1].HeadComment
			leaf.LineComment = node.Content[i+1].LineComment
			leaf.FootComment = node.Content[i+1].FootComment
			node.Content[i+1] = leaf
		} else {
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: parts[len(parts)-1]}, leaf)
		}
	}
	after, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, nil, err
	}
	var edited map[string]any
	if err := yaml.Unmarshal(after, &edited); err != nil {
		return nil, nil, err
	}
	return after, edited, nil
}
