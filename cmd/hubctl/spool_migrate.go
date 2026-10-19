package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
)

// SpoolMigrationReport is the deterministic outcome of `hubctl spool
// migrate`: what an offline run found under <spool>/knowledge and what it
// staged into <state>/knowledge. The command copies, never moves — the old
// image keeps reading the spool path until the operator removes it after the
// release is proven.
type SpoolMigrationReport struct {
	DryRun     bool                   `json:"dry_run"`
	Spool      string                 `json:"spool"`
	State      string                 `json:"state"`
	Principals []SpoolPrincipalReport `json:"principals"`
	Notes      []string               `json:"notes,omitempty"`
}

// SpoolPrincipalReport records one principal's knowledge tree.
type SpoolPrincipalReport struct {
	ID        string   `json:"id"`
	Copied    []string `json:"copied,omitempty"`
	Unchanged []string `json:"unchanged,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
	Skipped   []string `json:"skipped,omitempty"`
}

// spoolMigrate relocates knowledge sources staged under
// <spool>/knowledge/<principal>[/context] into the runtime-state home
// <state>/knowledge/<principal>[/context]. It runs under LockSpool — the
// gateway must be stopped. Without apply it reports only.
func spoolMigrate(spoolDir, stateDir string, apply bool) (SpoolMigrationReport, error) {
	rep := SpoolMigrationReport{DryRun: !apply, Spool: spoolDir, State: stateDir}
	unlock, err := communication.LockSpool(spoolDir)
	if err != nil {
		return rep, fmt.Errorf("spool locked (stop the gateway first): %w", err)
	}
	defer unlock()

	src, err := os.OpenRoot(filepath.Join(spoolDir, "knowledge"))
	if err != nil {
		if os.IsNotExist(err) {
			rep.Notes = append(rep.Notes, "no legacy spool/knowledge directory; nothing to migrate")
			return rep, nil
		}
		return rep, err
	}
	defer src.Close()

	entries, err := readRootDir(src, ".")
	if err != nil {
		return rep, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		info, _ := entry.Info()
		if !entry.IsDir() || info.Mode()&os.ModeSymlink != 0 || !identity.ValidID(name) {
			rep.Notes = append(rep.Notes, "skipped spool/knowledge entry: "+name)
			continue
		}
		rep.Principals = append(rep.Principals, migratePrincipal(src, stateDir, name, apply))
	}
	return rep, nil
}

// migratePrincipal copies one principal tree: flat files land under
// knowledge/<principal>/, valid subdirectories are context scopes.
func migratePrincipal(src *os.Root, stateDir, principal string, apply bool) SpoolPrincipalReport {
	rep := SpoolPrincipalReport{ID: principal}
	sub, err := src.OpenRoot(principal)
	if err != nil {
		rep.Skipped = append(rep.Skipped, ".: "+err.Error())
		return rep
	}
	defer sub.Close()
	entries, err := readRootDir(sub, ".")
	if err != nil {
		rep.Skipped = append(rep.Skipped, ".: "+err.Error())
		return rep
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		info, ierr := entry.Info()
		if ierr != nil || info.Mode()&os.ModeSymlink != 0 {
			rep.Skipped = append(rep.Skipped, name+": not a regular file")
			continue
		}
		if info.IsDir() {
			if !identity.ValidID(name) {
				rep.Skipped = append(rep.Skipped, name+"/: invalid context directory")
				continue
			}
			rep = migrateContext(sub, stateDir, principal, name, rep, apply)
			continue
		}
		rep = migrateFile(sub, stateDir, principal+"/"+name, rep, apply, info)
	}
	return rep
}

func migrateContext(principalRoot *os.Root, stateDir, principal, contextID string, rep SpoolPrincipalReport, apply bool) SpoolPrincipalReport {
	sub, err := principalRoot.OpenRoot(contextID)
	if err != nil {
		rep.Skipped = append(rep.Skipped, contextID+"/: "+err.Error())
		return rep
	}
	defer sub.Close()
	entries, err := readRootDir(sub, ".")
	if err != nil {
		rep.Skipped = append(rep.Skipped, contextID+"/: "+err.Error())
		return rep
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		info, ierr := entry.Info()
		rel := principal + "/" + contextID + "/" + name
		if ierr != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			rep.Skipped = append(rep.Skipped, rel+": not a regular file")
			continue
		}
		rep = migrateFile(sub, stateDir, rel, rep, apply, info)
	}
	return rep
}

// migrateFile validates the leaf name, then stages a copy.
func migrateFile(dir *os.Root, stateDir, rel string, rep SpoolPrincipalReport, apply bool, info os.FileInfo) SpoolPrincipalReport {
	name := filepath.Base(rel)
	if !knowledgeName(name) {
		rep.Skipped = append(rep.Skipped, rel+": invalid source name")
		return rep
	}
	f, err := dir.Open(name)
	if err != nil {
		rep.Skipped = append(rep.Skipped, rel+": "+err.Error())
		return rep
	}
	body, rerr := io.ReadAll(io.LimitReader(f, (9<<20)+1))
	cerr := f.Close()
	if rerr != nil || cerr != nil || len(body) > 9<<20 {
		rep.Skipped = append(rep.Skipped, rel+": unreadable or over limit")
		return rep
	}
	outcome, werr := stageKnowledgeCopy(stateDir, rel, body, apply)
	switch {
	case werr != nil:
		rep.Skipped = append(rep.Skipped, rel+": "+werr.Error())
	case outcome == "conflict":
		rep.Conflicts = append(rep.Conflicts, rel)
	case outcome == "unchanged":
		rep.Unchanged = append(rep.Unchanged, rel)
	default:
		rep.Copied = append(rep.Copied, rel)
	}
	return rep
}

// stageKnowledgeCopy writes body to <state>/knowledge/<destRel> atomically.
// Existing targets are never overwritten: identical content reports
// "unchanged", different content reports "conflict". Dry runs report
// "copied" for the plan without writing.
func stageKnowledgeCopy(stateDir, destRel string, body []byte, apply bool) (string, error) {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", err
	}
	base, err := os.OpenRoot(stateDir)
	if err != nil {
		return "", err
	}
	defer base.Close()
	dir, name := filepath.Split(filepath.ToSlash(destRel))
	dir = filepath.Clean(filepath.FromSlash(dir))
	if err := base.MkdirAll(filepath.Join("knowledge", dir), 0700); err != nil {
		return "", err
	}
	rel := filepath.ToSlash(filepath.Join("knowledge", dir, name))
	if prev, err := base.ReadFile(rel); err == nil {
		if bytes.Equal(prev, body) {
			return "unchanged", nil
		}
		return "conflict", nil
	}
	if !apply {
		return "copied", nil
	}
	tmpPath := filepath.ToSlash(filepath.Join("knowledge", dir, fmt.Sprintf(".migrate-%d", time.Now().UnixNano())))
	f, err := base.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer base.Remove(tmpPath)
	_, werr := f.Write(body)
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr != nil {
		return "", werr
	}
	if cerr != nil {
		return "", cerr
	}
	if err := base.Rename(tmpPath, rel); err != nil {
		return "", err
	}
	return "copied", nil
}

// readRootDir lists one directory inside a confined root.
func readRootDir(root *os.Root, name string) ([]os.DirEntry, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}
