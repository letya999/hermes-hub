package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/contextlife"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/skills"
)

func runContext(_ context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("context requires backup, restore, export, purge, stop-runtime or delete-runtime")
	}
	sub := args[0]
	f := flag.NewFlagSet("context", flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	profile := f.String("user", "me", "person identifier")
	out := f.String("out", "", "archive destination")
	in := f.String("in", "", "archive source")
	confirm := f.String("confirm", "", "explicit purge target")
	spool := f.String("spool", "", "communication spool to include")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *profile)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	switch sub {
	case "backup", "export":
		if *out == "" {
			return fmt.Errorf("%s requires --out", sub)
		}
		return contextlife.Backup(abs, *profile, *out, *spool)
	case "restore":
		if *in == "" {
			return fmt.Errorf("restore requires --in")
		}
		return contextlife.Restore(abs, *profile, *in, *spool)
	case "purge":
		report, err := contextlife.Purge(abs, *profile, *confirm)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	case "stop-runtime":
		fmt.Println("stop-runtime: use hubctl down --user " + *profile + " to stop compute without deleting data")
		return nil
	case "delete-runtime":
		fmt.Println("delete-runtime: use hubctl down --user " + *profile + " to remove compute; homes stay until context purge")
		return nil
	default:
		return fmt.Errorf("unknown context command %q", sub)
	}
}

func runMemory(_ context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("memory requires list or delete")
	}
	sub := args[0]
	f := flag.NewFlagSet("memory", flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	profile := f.String("user", "me", "person identifier")
	name := f.String("name", "", "memory file name")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *profile)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		names, err := contextlife.MemoryFiles(abs)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(names)
	case "delete":
		if *name == "" {
			return fmt.Errorf("memory delete requires --name")
		}
		return contextlife.DeleteMemory(abs, *name)
	default:
		return fmt.Errorf("unknown memory command %q", sub)
	}
}

func runSkill(_ context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("skill requires list, install or revoke")
	}
	sub := args[0]
	f := flag.NewFlagSet("skill", flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	profile := f.String("user", "me", "person identifier")
	name := f.String("name", "", "skill name")
	from := f.String("from-file", "", "SKILL.md path")
	source := f.String("source", "", "source URL")
	consent := f.Bool("consent", false, "owner consent to activate")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *profile)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	caller := identity.Envelope{Schema: identity.Schema, PrincipalID: *profile, ExternalIdentityID: "telegram-1", ContextID: *profile, RuntimeID: *profile, ConversationID: "telegram-1", DeliveryTargetID: "telegram-1", PolicyVersion: "policy-1"}
	switch sub {
	case "list":
		items, err := skills.Advertised(abs)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(items)
	case "install":
		if *name == "" || *from == "" {
			return fmt.Errorf("skill install requires --name and --from-file")
		}
		body, err := os.ReadFile(*from)
		if err != nil {
			return err
		}
		rec, err := skills.Install(abs, *name, *source, "user", body, *consent, caller)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(rec)
	case "revoke":
		if *name == "" {
			return fmt.Errorf("skill revoke requires --name")
		}
		return skills.Revoke(abs, *name, caller)
	default:
		return fmt.Errorf("unknown skill command %q", sub)
	}
}

func runRoutine(_ context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("routine requires list, create, pause or delete")
	}
	sub := args[0]
	f := flag.NewFlagSet("routine", flag.ContinueOnError)
	spoolDir := f.String("spool", "", "communication spool")
	profile := f.String("user", "me", "person identifier")
	id := f.String("id", "", "schedule id")
	tz := f.String("tz", "UTC", "timezone")
	expr := f.String("expr", "", "once:RFC3339 or 5-field cron")
	input := f.String("input", "", "bounded task text")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *spoolDir == "" {
		return fmt.Errorf("routine requires --spool")
	}
	spool, err := communication.NewSpool(*spoolDir)
	if err != nil {
		return err
	}
	caller := identity.TelegramEnvelope(*profile, 1, *profile, "policy-1")
	switch sub {
	case "list":
		items, err := spool.ListSchedules(caller)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(items)
	case "create":
		if *id == "" || *expr == "" || strings.TrimSpace(*input) == "" {
			return fmt.Errorf("routine create requires --id --expr --input")
		}
		item, err := spool.CreateSchedule(communication.Schedule{ScheduleID: *id, Envelope: caller, OrganizationID: "personal", UserID: *profile, ActorID: *profile, ScopeID: "user:" + *profile, Channel: "telegram_bot", ChatID: 1, Timezone: *tz, Expression: *expr, Input: *input}, caller, "")
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(item)
	case "pause":
		if *id == "" {
			return fmt.Errorf("routine pause requires --id")
		}
		return spool.PauseSchedule(*id, caller)
	case "delete":
		if *id == "" {
			return fmt.Errorf("routine delete requires --id")
		}
		return spool.DeleteSchedule(*id, caller)
	default:
		return fmt.Errorf("unknown routine command %q", sub)
	}
}
