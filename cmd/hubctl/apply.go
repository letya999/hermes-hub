package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	ID         string `yaml:"id"`
	Kind       string `yaml:"kind"`
	ChatID     int64  `yaml:"chat_id"`
	Timezone   string `yaml:"timezone"`
	Expression string `yaml:"expression"`
	Input      string `yaml:"input"`
	URL        string `yaml:"url"`
	Regex      string `yaml:"regex"`
	Expect     string `yaml:"expect"`
}

type applySpec struct {
	Routines  []applyRoutineSpec `yaml:"routines"`
	Knowledge []struct {
		Name   string `yaml:"name"`
		Source string `yaml:"source"`
	} `yaml:"knowledge"`
}

func applyFile(spoolDir, profile, path string) (ApplyReport, error) {
	rep := ApplyReport{Routines: map[string]string{}, Knowledge: map[string]string{}}
	body, err := os.ReadFile(path)
	if err != nil {
		return rep, err
	}
	var cfg applySpec
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return rep, fmt.Errorf("invalid apply file: %w", err)
	}
	spool, err := communication.NewSpool(spoolDir)
	if err != nil {
		return rep, err
	}
	for _, r := range cfg.Routines {
		action, err := applyRoutine(spool, profile, r)
		if err != nil {
			return rep, fmt.Errorf("routine %q: %w", r.ID, err)
		}
		rep.Routines[r.ID] = action
	}
	for _, k := range cfg.Knowledge {
		action, err := applyKnowledge(spoolDir, profile, k.Name, k.Source)
		if err != nil {
			return rep, fmt.Errorf("knowledge %q: %w", k.Name, err)
		}
		rep.Knowledge[k.Name] = action
	}
	return rep, nil
}

func applyRoutine(spool *communication.Spool, profile string, r applyRoutineSpec) (string, error) {
	if r.Kind == "check" {
		// A check needs a live creation-time probe to anchor its baseline;
		// the offline apply path has none — use POST /v1/routines instead.
		return "", fmt.Errorf("kind=check requires the hub API (baseline probe)")
	}
	chatID := r.ChatID
	if chatID == 0 {
		chatID = 1
	}
	caller := identity.TelegramEnvelope(profile, chatID, profile, "policy-1")
	tz := r.Timezone
	if tz == "" {
		tz = "UTC"
	}
	var probe *communication.WatchProbe
	if r.URL != "" || r.Regex != "" {
		probe = &communication.WatchProbe{URL: r.URL, Regex: r.Regex}
	}
	in := communication.Schedule{
		ScheduleID: r.ID, Envelope: caller, OrganizationID: "personal",
		UserID: profile, ActorID: profile, ScopeID: "user:" + profile,
		Channel: "telegram_bot", ChatID: chatID, Timezone: tz,
		Expression: r.Expression, JobKind: r.Kind, Probe: probe, Expect: r.Expect,
		Input: r.Input,
	}
	if _, err := spool.CreateSchedule(in, caller, ""); err == nil {
		return "created", nil
	} else if err.Error() != "schedule already exists" {
		return "", err
	}
	items, err := spool.ListSchedules(caller)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.ScheduleID == in.ScheduleID && sameRoutine(item, in) {
			return "unchanged", nil
		}
	}
	return "", fmt.Errorf("schedule %q exists with a different spec; delete it first", in.ScheduleID)
}

// sameRoutine compares the operator-visible spec — durable bookkeeping
// (revision, next_due, timestamps) never counts as drift.
func sameRoutine(a, b communication.Schedule) bool {
	probeOf := func(s communication.Schedule) string {
		if s.Probe == nil {
			return ""
		}
		return s.Probe.URL + "\x00" + s.Probe.Regex
	}
	if probeOf(a) != probeOf(b) {
		return false
	}
	norm := func(s communication.Schedule) communication.Schedule {
		s.Revision, s.NextDue, s.CreatedAt, s.UpdatedAt = 0, time.Time{}, time.Time{}, time.Time{}
		s.Enabled, s.Paused, s.CatchUpWindow = false, false, 0
		s.Probe = nil
		return s
	}
	return fmt.Sprintf("%+v", norm(a)) == fmt.Sprintf("%+v", norm(b))
}

func applyKnowledge(spoolDir, profile, name, source string) (string, error) {
	body, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(spoolDir, "knowledge", profile, name)
	if prev, err := os.ReadFile(dst); err == nil && bytes.Equal(prev, body) {
		return "unchanged", nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		return "", err
	}
	return "created", nil
}
