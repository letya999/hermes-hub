package passport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/letya999/hermes-hub/internal/catalog"
)

const maxAuthoredPassportBytes = 1 << 20

// ReadAuthored reads the six operator-authored passport bodies. Publication
// metadata belongs to the compiled revision, never to these source files.
func ReadAuthored(spaceDir string) (map[string]map[string]any, error) {
	path := filepath.Join(spaceDir, "passports")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("authored passports: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("authored passports: passports must be a regular directory")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("authored passports: %w", err)
	}
	defer root.Close()

	directory, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("authored passports: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return nil, fmt.Errorf("authored passports: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("authored passports: %w", closeErr)
	}
	allowed := make(map[string]bool, len(Names))
	for _, name := range Names {
		allowed[name+".yaml"] = true
	}
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if (ext == ".yaml" || ext == ".yml" || ext == ".json" || ext == ".toml") && !allowed[entry.Name()] {
			return nil, fmt.Errorf("authored passports: unexpected configuration file %s", entry.Name())
		}
	}

	docs := make(map[string]map[string]any, len(Names))
	for _, name := range Names {
		filename := name + ".yaml"
		info, err := root.Lstat(filename)
		if err != nil {
			return nil, fmt.Errorf("authored passports: %s: %w", filename, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("authored passports: %s must be a regular file", filename)
		}
		f, err := root.Open(filename)
		if err != nil {
			return nil, fmt.Errorf("authored passports: %s: %w", filename, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(f, maxAuthoredPassportBytes+1))
		closeErr := f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("authored passports: %s: %w", filename, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("authored passports: %s: %w", filename, closeErr)
		}
		if len(body) > maxAuthoredPassportBytes {
			return nil, fmt.Errorf("authored passports: %s exceeds size limit", filename)
		}
		dec := yaml.NewDecoder(bytes.NewReader(body))
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil || len(doc) == 0 {
			return nil, fmt.Errorf("authored passports: %s is empty or invalid: %v", filename, err)
		}
		if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("authored passports: %s contains a trailing document", filename)
		}
		for _, key := range []string{"apiVersion", "kind", "metadata", "body"} {
			if _, exists := doc[key]; exists {
				return nil, fmt.Errorf("authored passports: %s contains generated field %s", filename, key)
			}
		}
		docs[name] = doc
	}
	return docs, nil
}

// CompileAuthored validates the six authored documents using the existing
// cross-file compiler. The trusted caller supplies the effective grant ceiling
// and must separately verify the publisher before publishing the result.
func CompileAuthored(spaceDir, environment string, now time.Time, catalogDocs catalog.Docs, reservedPorts map[int]string, grantCeiling map[string][]string) (Revision, error) {
	var empty Revision
	if grantCeiling == nil {
		return empty, fmt.Errorf("authored passports: trusted grant ceiling is required")
	}
	authored, err := ReadAuthored(spaceDir)
	if err != nil {
		return empty, err
	}
	return CompileAuthoredDocs(spaceDir, environment, now, catalogDocs, reservedPorts, grantCeiling, authored)
}

// CompileAuthoredDocs validates an in-memory candidate through the same six
// passport compiler. The publisher uses it before changing authored files.
func CompileAuthoredDocs(spaceDir, environment string, now time.Time, catalogDocs catalog.Docs, reservedPorts map[int]string, grantCeiling map[string][]string, authored map[string]map[string]any) (Revision, error) {
	var empty Revision
	if grantCeiling == nil {
		return empty, fmt.Errorf("authored passports: trusted grant ceiling is required")
	}
	space := filepath.Base(spaceDir)
	identity, _ := authored["stack"]["identity"].(map[string]any)
	if identity["user"] != space {
		return empty, fmt.Errorf("authored passports: stack.identity.user must equal space %q", space)
	}
	rendersInfra, ok := identity["infra"].(bool)
	if !ok {
		return empty, fmt.Errorf("authored passports: stack.identity.infra must be a boolean")
	}
	if kind, ok := identity["kind"]; ok && kind != "user" && kind != "group" {
		return empty, fmt.Errorf("authored passports: stack.identity.kind must be user or group, not %v", kind)
	}
	services, _ := authored["stack"]["services"].(map[string]any)
	memory, _ := authored["config"]["memory"].(map[string]any)
	if _, ok := memory["enabled"].(bool); !ok {
		return empty, fmt.Errorf("authored passports: config.memory.enabled must be a boolean")
	}
	if _, managed := services["hermes-runtime"]; managed {
		if _, bound := authored["tools"]["binding"]; !bound {
			return empty, fmt.Errorf("authored passports: managed space requires tools.binding")
		}
	}
	grantsDoc := authored["grants"]
	allow, ok := grantsDoc["allow"].(map[string]any)
	if !ok || len(grantsDoc) != 1 {
		return empty, fmt.Errorf("authored passports: grants.yaml must contain only an allow map")
	}
	// The {capability: [operations]} parse is shared with LayeredGrants in
	// grants.go; the bound differs — v0 checks each op against the trusted
	// grant ceiling the caller supplies, not the layer above.
	caps, grants, err := parseGrantsOps(allow, "authored passports: grants.yaml allow.")
	if err != nil {
		return empty, err
	}
	for cap, ops := range grants {
		for _, op := range ops {
			if !slices.Contains(grantCeiling[cap], op) {
				return empty, fmt.Errorf("authored passports: grants.yaml allow.%s.%s exceeds trusted ceiling", cap, op)
			}
		}
	}
	ceiling := make([]any, len(caps))
	for i, cap := range caps {
		ceiling[i] = cap
	}
	authored = maps.Clone(authored)
	authored["grants"] = map[string]any{"allow": ceiling}
	return Compile(Inputs{
		Space: space, Environment: environment, Now: now,
		User:   Layer{Name: space, Version: 1, Docs: authored},
		Grants: grants, Catalog: catalogDocs,
		RendersInfra: rendersInfra, ReservedPorts: reservedPorts,
	})
}
