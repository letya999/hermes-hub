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
	"github.com/letya999/hermes-hub/internal/skillstore"
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
		return fmt.Errorf("skill requires list, show, install, drafts, approve, reject, rollback, archive or restore")
	}
	sub := args[0]
	f := flag.NewFlagSet("skill", flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	profile := f.String("user", "me", "person identifier")
	name := f.String("name", "", "skill name")
	id := f.String("id", "", "draft id")
	from := f.String("from-file", "", "SKILL.md path")
	source := f.String("source", "", "source URL")
	to := f.String("to", "", "target revision for rollback")
	consent := f.Bool("consent", false, "owner consent to publish; without it the install lands as a draft")
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
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		names, err := st.Names()
		if err != nil {
			return err
		}
		idx, err := st.Index()
		if err != nil {
			return err
		}
		type item struct {
			Name     string `json:"name"`
			Revision string `json:"revision"`
		}
		out := make([]item, 0, len(names))
		for _, n := range names {
			out = append(out, item{Name: n, Revision: idx.Skills[n].Revision})
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	case "show":
		if *name == "" {
			return fmt.Errorf("skill show requires --name")
		}
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		m, rev, err := st.Manifest(*name)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			*skillstore.Manifest
			Revision string `json:"revision"`
		}{m, rev})
	case "install":
		if *name == "" || *from == "" {
			return fmt.Errorf("skill install requires --name and --from-file")
		}
		body, err := os.ReadFile(*from)
		if err != nil {
			return err
		}
		if _, _, err := skillstore.ParseSkillMD(*name, body, false); err != nil {
			return err
		}
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		tree := skillstore.Tree{skillstore.SkillFile: skillstore.File{Data: body}}
		if !*consent {
			d, err := st.CreateDraft(*name, tree, skillstore.DraftMeta{Author: "operator"})
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(d)
		}
		rev, err := st.Publish(*name, tree, skillstore.IndexMeta{Author: "operator", Source: "import", Origin: &skillstore.Origin{URL: *source}})
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Name     string `json:"name"`
			Revision string `json:"revision"`
		}{*name, rev})
	case "archive":
		if *name == "" {
			return fmt.Errorf("skill archive requires --name")
		}
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		return st.Archive(*name, "operator archive", "", *profile)
	case "restore":
		if *name == "" {
			return fmt.Errorf("skill restore requires --name")
		}
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		return st.Restore(*name)
	case "drafts":
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		drafts, err := st.Drafts()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(drafts)
	case "approve":
		if *id == "" {
			return fmt.Errorf("skill approve requires --id")
		}
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		_, err = st.PublishDraft(*id, *profile)
		return err
	case "reject":
		if *id == "" {
			return fmt.Errorf("skill reject requires --id")
		}
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		return st.RejectDraft(*id)
	case "rollback":
		if *name == "" || *to == "" {
			return fmt.Errorf("skill rollback requires --name and --to")
		}
		st, err := skillstore.Open(filepath.Join(abs, "skills"), skillstore.ScopeUser)
		if err != nil {
			return err
		}
		_, err = st.Rollback(*name, *to)
		return err
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
	chatID := f.Int64("chat-id", 0, "explicit Telegram chat id")
	organization := f.String("organization", "", "explicit organization id")
	runtimeID := f.String("runtime", "", "explicit runtime id")
	policy := f.String("policy", "", "explicit policy version")
	input := f.String("input", "", "bounded task text")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *spoolDir == "" {
		return fmt.Errorf("routine requires --spool")
	}
	unlock, err := communication.LockSpool(*spoolDir)
	if err != nil {
		return err
	}
	defer unlock()
	spool, err := communication.OpenSpool(*spoolDir)
	if err != nil {
		return err
	}
	if sub != "list" && (*chatID <= 0 || !identity.ValidID(*organization) || !identity.ValidID(*runtimeID) || !identity.ValidID(*policy)) {
		return fmt.Errorf("routine mutation requires --chat-id --organization --runtime --policy")
	}
	caller := identity.TelegramEnvelope(*profile, *chatID, *runtimeID, *policy)
	if sub == "list" {
		caller.PrincipalID = *profile
	}
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
		item, err := spool.CreateSchedule(communication.Schedule{ScheduleID: *id, Envelope: caller, OrganizationID: *organization, UserID: *profile, ActorID: *profile, ScopeID: "user:" + *profile, Channel: "telegram_bot", ChatID: *chatID, Timezone: *tz, Expression: *expr, Input: *input}, caller, "")
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
