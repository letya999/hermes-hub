package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/supervisor"
)

func mustWriteRolloutPins(t *testing.T, dir string, pins supervisor.RolloutPins) {
	t.Helper()
	if err := supervisor.WriteRolloutPins(dir, pins); err != nil {
		t.Fatal(err)
	}
}

var smokeOK = func(context.Context, string) error { return nil }
var smokeFail = func(context.Context, string) error { return errors.New("smoke failed") }

func TestPromoteRequiresSmoke(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {Current: supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()}},
	}})
	var out bytes.Buffer
	err := runImage(t.Context(), &out, dir, "prod", smokeFail, []string{"promote", "--image", "img/next:rc"})
	if err == nil {
		t.Fatal("promote must fail when smoke fails")
	}
	pins, _ := supervisor.ReadRolloutPins(dir)
	if pins.Envs["prod"].Current.Image != "img/stable:1" {
		t.Fatalf("pin moved without smoke pass: %+v", pins.Envs["prod"])
	}
}

func TestImageDispatch(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {Current: supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()}},
	}})
	if err := run(t.Context(), []string{"image", "status", "--dir", dir, "--env", "prod"}); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"image", "status", "--dir", dir, "--env", "bogus"}); err == nil {
		t.Fatal("bogus --env must fail")
	}
}

func TestImageArgValidation(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	for _, args := range [][]string{
		{"bogus"},
		{"promote"},
		{},
	} {
		if err := runImage(t.Context(), &out, dir, "prod", smokeOK, args); err == nil {
			t.Fatalf("args %v must fail", args)
		}
	}
}

func TestImageStatus(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {
			Current:  supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()},
			Previous: &supervisor.RolloutPin{Image: "img/old:0", SmokedAt: time.Now()},
			Canary:   &supervisor.CanaryPin{Image: "img/next:rc", ContextID: "ctx-9", SetAt: time.Now()},
		},
	}})
	var out bytes.Buffer
	if err := runImage(t.Context(), &out, dir, "prod", smokeOK, []string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"img/stable:1", "img/old:0", "img/next:rc", "ctx-9"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status output missing %q: %s", want, out.String())
		}
	}
}

func TestCanarySetAndClear(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {Current: supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()}},
	}})
	var out bytes.Buffer
	if err := runImage(t.Context(), &out, dir, "prod", smokeOK, []string{"canary", "--image", "img/next:rc", "--context", "ctx-9"}); err != nil {
		t.Fatal(err)
	}
	pins, err := supervisor.ReadRolloutPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := pins.Envs["prod"].Canary
	if c == nil || c.Image != "img/next:rc" || c.ContextID != "ctx-9" {
		t.Fatalf("canary = %+v", c)
	}
	if pins.Envs["prod"].Current.Image != "img/stable:1" {
		t.Fatalf("canary moved the verified pin: %+v", pins.Envs["prod"].Current)
	}
	if err := runImage(t.Context(), &out, dir, "prod", smokeOK, []string{"canary", "--clear"}); err != nil {
		t.Fatal(err)
	}
	pins, _ = supervisor.ReadRolloutPins(dir)
	if pins.Envs["prod"].Canary != nil {
		t.Fatalf("canary not cleared: %+v", pins.Envs["prod"].Canary)
	}
}

func TestCanaryRequiresImageAndContext(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {Current: supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()}},
	}})
	var out bytes.Buffer
	for _, args := range [][]string{
		{"canary", "--context", "ctx-9"},
		{"canary", "--image", "img/next:rc"},
	} {
		if err := runImage(t.Context(), &out, dir, "prod", smokeOK, args); err == nil {
			t.Fatalf("args %v must fail", args)
		}
	}
	pins, _ := supervisor.ReadRolloutPins(dir)
	if pins.Envs["prod"].Canary != nil {
		t.Fatal("canary written without required flags")
	}
}

func TestRevertWithoutPreviousFails(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {Current: supervisor.RolloutPin{Image: "img/only:1", SmokedAt: time.Now()}},
	}})
	var out bytes.Buffer
	err := runImage(t.Context(), &out, dir, "prod", smokeOK, []string{"revert"})
	if err == nil {
		t.Fatal("revert without a previous pin must fail")
	}
	pins, _ := supervisor.ReadRolloutPins(dir)
	if pins.Envs["prod"].Current.Image != "img/only:1" {
		t.Fatalf("pin moved on failed revert: %+v", pins.Envs["prod"])
	}
}

func TestRevertRestoresPrevious(t *testing.T) {
	dir := t.TempDir()
	var smoked []string
	smoke := func(_ context.Context, image string) error {
		smoked = append(smoked, image)
		return nil
	}
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {
			Current:  supervisor.RolloutPin{Image: "img/bad:2", SmokedAt: time.Now()},
			Previous: &supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()},
		},
	}})
	var out bytes.Buffer
	if err := runImage(t.Context(), &out, dir, "prod", smoke, []string{"revert"}); err != nil {
		t.Fatal(err)
	}
	pins, err := supervisor.ReadRolloutPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pins.Envs["prod"].Current.Image != "img/stable:1" {
		t.Fatalf("current after revert = %+v", pins.Envs["prod"].Current)
	}
	if len(smoked) != 1 || smoked[0] != "img/stable:1" {
		t.Fatalf("post-revert smoke ran on %v", smoked)
	}
}

func TestPromoteSerializesOnLock(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {Current: supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()}},
	}})
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	held := flock.New(filepath.Join(dir, "generated", ".image-pins.lock"))
	locked, err := held.TryLock()
	if err != nil || !locked {
		t.Fatalf("test could not take pins lock: %v locked=%v", err, locked)
	}
	defer held.Close()
	var out bytes.Buffer
	err = runImage(t.Context(), &out, dir, "prod", smokeOK, []string{"promote", "--image", "img/next:rc"})
	if err == nil {
		t.Fatal("promote must fail while another writer holds the pins lock")
	}
	pins, _ := supervisor.ReadRolloutPins(dir)
	if pins.Envs["prod"].Current.Image != "img/stable:1" {
		t.Fatal("pin moved under a held lock")
	}
}

func TestMutationsSerializeOnLock(t *testing.T) {
	for _, args := range [][]string{
		{"revert"},
		{"canary", "--image", "img/next:rc", "--context", "ctx-9"},
	} {
		dir := t.TempDir()
		mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
			"prod": {
				Current:  supervisor.RolloutPin{Image: "img/bad:2", SmokedAt: time.Now()},
				Previous: &supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()},
			},
		}})
		held := flock.New(filepath.Join(dir, "generated", ".image-pins.lock"))
		locked, err := held.TryLock()
		if err != nil || !locked {
			t.Fatalf("%v: could not take pins lock: %v", args, err)
		}
		var out bytes.Buffer
		err = runImage(t.Context(), &out, dir, "prod", smokeOK, args)
		held.Close()
		if err == nil {
			t.Fatalf("%v must fail while another writer holds the pins lock", args)
		}
		pins, _ := supervisor.ReadRolloutPins(dir)
		if pins.Envs["prod"].Current.Image != "img/bad:2" || pins.Envs["prod"].Canary != nil {
			t.Fatalf("%v moved pins under a held lock: %+v", args, pins.Envs["prod"])
		}
	}
}

func TestPromoteShiftsAndWrites(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{
		"prod": {Current: supervisor.RolloutPin{Image: "img/stable:1", SmokedAt: time.Now()}},
	}})
	var out bytes.Buffer
	if err := runImage(t.Context(), &out, dir, "prod", smokeOK, []string{"promote", "--image", "img/next:rc"}); err != nil {
		t.Fatal(err)
	}
	pins, err := supervisor.ReadRolloutPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := pins.Envs["prod"]
	if got.Current.Image != "img/next:rc" || got.Current.SmokedAt.IsZero() {
		t.Fatalf("current = %+v", got.Current)
	}
	if got.Previous == nil || got.Previous.Image != "img/stable:1" {
		t.Fatalf("previous = %+v", got.Previous)
	}
}
