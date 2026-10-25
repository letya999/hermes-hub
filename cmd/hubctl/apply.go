package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"gopkg.in/yaml.v3"
)

// ApplyReport records the deterministic outcome of one apply.
type ApplyReport struct {
	Routines  map[string]string `json:"routines"`
	Knowledge map[string]string `json:"knowledge"`
}

type applyRoutineSpec struct {
	ID             string   `yaml:"id"`
	OrganizationID string   `yaml:"organization_id"`
	RuntimeID      string   `yaml:"runtime_id"`
	PolicyVersion  string   `yaml:"policy_version"`
	Channel        string   `yaml:"channel"`
	Kind           string   `yaml:"kind"`
	ChatID         int64    `yaml:"chat_id"`
	Timezone       string   `yaml:"timezone"`
	Expression     string   `yaml:"expression"`
	Input          string   `yaml:"input"`
	URL            string   `yaml:"url"`
	Regex          string   `yaml:"regex"`
	ProbeKind      string   `yaml:"probe_kind"`
	NotifyMode     string   `yaml:"notify_mode"`
	SourceIDs      []string `yaml:"source_ids"`
	Expect         string   `yaml:"expect"`
}

type applySpec struct {
	Routines  []applyRoutineSpec `yaml:"routines"`
	Knowledge []struct {
		Name    string `yaml:"name"`
		Source  string `yaml:"source"`
		Context string `yaml:"context"`
		State   string `yaml:"state"`
	} `yaml:"knowledge"`
}

func applyFile(spoolDir, stateDir, profile, path string) (ApplyReport, error) {
	rep := ApplyReport{Routines: map[string]string{}, Knowledge: map[string]string{}}
	body, err := readApplySource(path)
	if err != nil {
		return rep, err
	}
	var cfg applySpec
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return rep, fmt.Errorf("invalid apply file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return rep, fmt.Errorf("apply requires exactly one YAML document")
	}
	if !identity.ValidID(profile) {
		return rep, fmt.Errorf("invalid user")
	}
	unlock, err := communication.LockSpool(spoolDir)
	if err != nil {
		return rep, err
	}
	defer unlock()
	spool, err := communication.OpenSpool(spoolDir)
	if err != nil {
		return rep, err
	}
	if len(cfg.Routines) > 128 || len(cfg.Knowledge) > 128 {
		return rep, fmt.Errorf("apply exceeds resource limit (128 per kind)")
	}
	seen := map[string]bool{}
	for _, r := range cfg.Routines {
		if seen["routine:"+r.ID] {
			return rep, fmt.Errorf("duplicate routine %q", r.ID)
		}
		seen["routine:"+r.ID] = true
		if _, err := routineAction(spool, profile, r, false); err != nil {
			return rep, fmt.Errorf("routine %q: %w", r.ID, err)
		}
	}
	if len(cfg.Knowledge) > 0 && strings.TrimSpace(stateDir) == "" {
		return rep, fmt.Errorf("apply requires --state <dir> for knowledge entries")
	}
	knowledge := map[string][]byte{}
	snapshots := map[string]map[string][]byte{}
	totalBytes := 0
	for _, k := range cfg.Knowledge {
		if seen["knowledge:"+k.Name] || !knowledgeName(k.Name) {
			return rep, fmt.Errorf("invalid or duplicate knowledge name %q", k.Name)
		}
		seen["knowledge:"+k.Name] = true
		if k.Context != "" || k.State != "" {
			if !identity.ValidID(k.Context) {
				return rep, fmt.Errorf("invalid knowledge context")
			}
			if k.State != "present" && k.State != "stale" && k.State != "unavailable" && k.State != "absent" {
				return rep, fmt.Errorf("invalid knowledge state")
			}
			if k.State != "present" {
				continue
			}
			folder := k.Source
			if !filepath.IsAbs(folder) {
				folder = filepath.Join(filepath.Dir(path), folder)
			}
			files := map[string][]byte{}
			err := filepath.Walk(folder, func(name string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("knowledge source must not contain symlinks")
				}
				if info.IsDir() {
					return nil
				}
				if len(files) >= 128 {
					return fmt.Errorf("knowledge folder exceeds 128 documents")
				}
				body, err := readKnowledgeSource(name)
				if err != nil {
					return err
				}
				totalBytes += len(body)
				if totalBytes > 32<<20 {
					return fmt.Errorf("apply knowledge exceeds 32 MiB aggregate limit")
				}
				rel, err := filepath.Rel(folder, name)
				if err != nil {
					return err
				}
				files[filepath.ToSlash(rel)] = body
				return nil
			})
			if err != nil {
				return rep, fmt.Errorf("knowledge %q: %w", k.Name, err)
			}
			snapshots[k.Name] = files
			continue
		}
		source := k.Source
		if !filepath.IsAbs(source) {
			source = filepath.Join(filepath.Dir(path), source)
		}
		body, err := readKnowledgeSource(source)
		if err != nil {
			return rep, fmt.Errorf("knowledge %q: %w", k.Name, err)
		}
		totalBytes += len(body)
		if totalBytes > 32<<20 {
			return rep, fmt.Errorf("apply knowledge exceeds 32 MiB aggregate limit")
		}
		knowledge[k.Name] = body
	}
	for _, r := range cfg.Routines {
		action, err := applyRoutine(spool, profile, r)
		if err != nil {
			return rep, fmt.Errorf("routine %q: %w", r.ID, err)
		}
		rep.Routines[r.ID] = action
	}
	for _, k := range cfg.Knowledge {
		if k.Context != "" || k.State != "" {
			if _, err := agenttools.StageKnowledgeSource(stateDir, profile, k.Context, k.Name, k.State, snapshots[k.Name]); err != nil {
				return rep, err
			}
			rep.Knowledge[k.Name] = k.State
			continue
		}
		action, err := writeKnowledge(stateDir, profile, k.Name, knowledge[k.Name])
		if err != nil {
			return rep, fmt.Errorf("knowledge %q: %w", k.Name, err)
		}
		rep.Knowledge[k.Name] = action
	}
	return rep, nil
}

func applyRoutine(spool *communication.Spool, profile string, r applyRoutineSpec) (string, error) {
	return routineAction(spool, profile, r, true)
}

func routineAction(spool *communication.Spool, profile string, r applyRoutineSpec, write bool) (string, error) {
	if r.Kind == "check" {
		// A check needs a live creation-time probe to anchor its baseline;
		// the offline apply path has none — use POST /v1/routines instead.
		return "", fmt.Errorf("kind=check requires the hub API (baseline probe)")
	}
	chatID := r.ChatID
	if chatID <= 0 || r.Channel != "telegram_bot" || !identity.ValidID(r.OrganizationID) || !identity.ValidID(r.RuntimeID) || !identity.ValidID(r.PolicyVersion) {
		return "", fmt.Errorf("explicit telegram channel, chat_id, organization_id, runtime_id and policy_version required")
	}
	caller := identity.TelegramEnvelope(profile, chatID, r.RuntimeID, r.PolicyVersion)
	tz := r.Timezone
	if tz == "" {
		tz = "UTC"
	}
	var probe *communication.WatchProbe
	if r.URL != "" || r.Regex != "" {
		probe = &communication.WatchProbe{Kind: r.ProbeKind, URL: r.URL, Regex: r.Regex}
	}
	in := communication.Schedule{
		ScheduleID: r.ID, Envelope: caller, OrganizationID: r.OrganizationID,
		UserID: profile, ActorID: profile, ScopeID: "user:" + profile,
		Channel: "telegram_bot", ChatID: chatID, Timezone: tz,
		Expression: strings.TrimSpace(r.Expression), JobKind: r.Kind, Probe: probe, Expect: r.Expect,
		Input: r.Input, NotifyMode: r.NotifyMode, SourceIDs: r.SourceIDs,
	}
	if in.JobKind == "" {
		in.JobKind = "agent"
	}
	items, err := spool.ListSchedules(caller)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.ScheduleID == in.ScheduleID {
			if sameRoutine(item, in) {
				return "unchanged", nil
			}
			return "", fmt.Errorf("schedule %q has a different spec", in.ScheduleID)
		}
	}
	if err := communication.ValidateSchedule(in, caller, time.Now().UTC()); err != nil {
		return "", err
	}
	if !write {
		return "created", nil
	}
	_, err = spool.CreateSchedule(in, caller, "")
	return "created", err
}

// sameRoutine compares the operator-visible spec — durable bookkeeping
// (revision, next_due, timestamps) never counts as drift.
func sameRoutine(a, b communication.Schedule) bool {
	probeOf := func(s communication.Schedule) string {
		if s.Probe == nil {
			return ""
		}
		return s.Probe.Kind + "\x00" + s.Probe.URL + "\x00" + s.Probe.Regex
	}
	if probeOf(a) != probeOf(b) {
		return false
	}
	norm := func(s communication.Schedule) communication.Schedule {
		s.Revision, s.NextDue, s.CreatedAt, s.UpdatedAt = 0, time.Time{}, time.Time{}, time.Time{}
		s.IntervalAnchor = time.Time{}
		s.Enabled, s.Paused, s.CatchUpWindow = false, false, 0
		s.Creation, s.Probe = nil, nil
		s.PriorEnvelopes = nil
		return s
	}
	return fmt.Sprintf("%+v", norm(a)) == fmt.Sprintf("%+v", norm(b))
}

func knowledgeName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 128 && !strings.ContainsAny(name, `/\:`+"\x00") && !strings.HasPrefix(name, ".")
}

func readKnowledgeSource(source string) ([]byte, error) {
	f, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 9<<20 {
		return nil, fmt.Errorf("knowledge source must be a regular file of at most 9 MiB")
	}
	body, err := io.ReadAll(io.LimitReader(f, (9<<20)+1))
	if len(body) > 9<<20 {
		return nil, fmt.Errorf("knowledge source exceeds limit")
	}
	return body, err
}

func applyKnowledge(stateDir, profile, name, source string) (string, error) {
	if !knowledgeName(name) || !identity.ValidID(profile) {
		return "", fmt.Errorf("invalid knowledge destination")
	}
	body, err := readKnowledgeSource(source)
	if err != nil {
		return "", err
	}
	return writeKnowledge(stateDir, profile, name, body)
}

func writeKnowledge(stateDir, profile, name string, body []byte) (string, error) {
	if !knowledgeName(name) || !identity.ValidID(profile) {
		return "", fmt.Errorf("invalid knowledge destination")
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", err
	}
	spool, err := os.OpenRoot(stateDir)
	if err != nil {
		return "", err
	}
	defer spool.Close()
	if err := spool.MkdirAll("knowledge/"+profile, 0700); err != nil {
		return "", err
	}
	if info, err := spool.Lstat("knowledge/" + profile); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("knowledge owner directory must not be a symlink")
	}
	root, err := spool.OpenRoot("knowledge/" + profile)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if prev, err := root.ReadFile(name); err == nil && bytes.Equal(prev, body) {
		return "unchanged", nil
	}
	temp := fmt.Sprintf(".apply-%d", time.Now().UnixNano())
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer root.Remove(temp)
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := root.Rename(temp, name); err != nil {
		return "", err
	}
	return "created", nil
}

func readApplySource(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, fmt.Errorf("apply file must be regular and at most 1 MiB")
	}
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if len(body) > 1<<20 {
		return nil, fmt.Errorf("apply file exceeds limit")
	}
	return body, err
}
