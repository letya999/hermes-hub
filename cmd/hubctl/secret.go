package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/secrets"
)

func runSecret(_ context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("secret requires set, list, delete, expose, activity, backup, restore or rotate-key")
	}
	sub := args[0]
	f := flag.NewFlagSet("secret", flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	profile := f.String("user", "me", "person identifier")
	name := f.String("name", "", "credential name")
	fromFile := f.String("from-file", "", "read secret value from file instead of argv")
	out := f.String("out", "", "backup destination")
	in := f.String("in", "", "restore source")
	keyFile := f.String("key-file", os.Getenv("HUB_CREDENTIAL_KEY_FILE"), "encryption key file outside the store")
	storePath := f.String("store", os.Getenv("HUB_CREDENTIAL_STORE"), "ciphertext store path")
	terminal := f.Bool("terminal", false, "enable personal-terminal exposure")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *profile)
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if *storePath == "" {
		*storePath = filepath.Join(absDir, "runtime", "credentials", "store.enc")
	}
	if !filepath.IsAbs(*storePath) {
		*storePath, err = filepath.Abs(*storePath)
		if err != nil {
			return err
		}
	}
	if *keyFile == "" {
		*keyFile = defaultCredentialKeyPath()
	}
	if !filepath.IsAbs(*keyFile) {
		*keyFile, err = filepath.Abs(*keyFile)
		if err != nil {
			return err
		}
	}
	if err := ensureCredentialKey(*keyFile); err != nil {
		return err
	}
	svc, err := secrets.Open(*storePath, *keyFile, nil, "")
	if err != nil {
		return err
	}
	auditPath := filepath.Join(filepath.Dir(*storePath), "audit.jsonl")
	ledger, err := audit.Open(auditPath)
	if err != nil {
		return err
	}
	svc.Audit = ledger
	owner := *profile
	switch sub {
	case "set":
		raw, err := readSecretInput(*fromFile)
		if err != nil {
			return err
		}
		values := map[string]string{}
		if *name != "" {
			values[*name] = raw
		} else {
			parsed, err := parseAssignments(raw)
			if err != nil {
				return err
			}
			values = parsed
		}
		infos, err := svc.Set(owner, values)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(infos))
		for _, info := range infos {
			names = append(names, info.Name)
		}
		fmt.Println(secrets.FormatStatus(names, "active"))
		return nil
	case "list":
		infos, err := svc.List(owner)
		if err != nil {
			return err
		}
		out := make([]map[string]any, 0, len(infos))
		for _, info := range infos {
			out = append(out, map[string]any{"name": info.Name, "status": info.Status, "revision": info.Revision, "terminal_exposure": info.TerminalExposure})
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	case "delete":
		if *name == "" {
			return fmt.Errorf("secret delete requires --name")
		}
		if err := svc.Delete(owner, *name); err != nil {
			return err
		}
		fmt.Println(secrets.FormatStatus([]string{*name}, "removed"))
		return nil
	case "expose":
		if *name == "" {
			return fmt.Errorf("secret expose requires --name")
		}
		if err := svc.SetTerminalExposure(owner, *name, *terminal); err != nil {
			return err
		}
		status := "isolated"
		if *terminal {
			status = "terminal"
		}
		fmt.Println(secrets.FormatStatus([]string{*name}, status))
		return nil
	case "activity":
		events, err := ledger.List(owner)
		if err != nil {
			return err
		}
		safe := make([]map[string]any, 0, len(events))
		for _, event := range events {
			safe = append(safe, map[string]any{
				"event_id": event.EventID, "kind": event.Kind, "outcome": event.Outcome,
				"connection_id": event.ConnectionID, "policy_revision": event.PolicyRevision,
				"credential_revision": event.CredentialRevision, "job_id": event.JobID,
				"hermes_run_id": event.HermesRunID, "tool_call_id": event.ToolCallID,
				"name": event.Name,
			})
		}
		return json.NewEncoder(os.Stdout).Encode(safe)
	case "backup":
		if *out == "" {
			return fmt.Errorf("secret backup requires --out")
		}
		file, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600) // #nosec G304 -- operator-selected backup path.
		if err != nil {
			return err
		}
		defer file.Close()
		return svc.Backend.Backup(file)
	case "restore":
		if *in == "" {
			return fmt.Errorf("secret restore requires --in")
		}
		file, err := os.Open(*in) // #nosec G304 -- operator-selected backup path.
		if err != nil {
			return err
		}
		defer file.Close()
		return svc.Backend.Restore(file)
	case "rotate-key":
		if *fromFile == "" {
			return fmt.Errorf("secret rotate-key requires --from-file with the new key")
		}
		body, err := os.ReadFile(*fromFile) // #nosec G304 -- operator-selected key path.
		if err != nil {
			return err
		}
		key, err := decodeHexKey(strings.TrimSpace(string(body)))
		if err != nil {
			return err
		}
		if err := svc.Backend.RotateKey(key); err != nil {
			return err
		}
		fmt.Println("Статус: key-rotated")
		return nil
	default:
		return fmt.Errorf("unknown secret command %q", sub)
	}
}

func defaultCredentialKeyPath() string {
	if app := os.Getenv("APPDATA"); app != "" {
		return filepath.Join(app, "hermes-hub", "credential.key")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "credential.key")
	}
	return filepath.Join(home, ".config", "hermes-hub", "credential.key")
}

func ensureCredentialKey(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	key, err := credstore.GenerateKey()
	if err != nil {
		return err
	}
	return credstore.WriteKeyFile(path, key)
}

func readSecretInput(fromFile string) (string, error) {
	if fromFile != "" {
		body, err := os.ReadFile(fromFile) // #nosec G304 -- operator-selected secret file.
		if err != nil {
			return "", err
		}
		if len(body) > credstore.MaxValueBytes {
			return "", fmt.Errorf("secret value is too large")
		}
		return strings.TrimRight(string(body), "\r\n"), nil
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", fmt.Errorf("secret value must be provided on stdin or --from-file")
	}
	body, err := io.ReadAll(io.LimitReader(os.Stdin, credstore.MaxValueBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > credstore.MaxValueBytes {
		return "", fmt.Errorf("secret value is too large")
	}
	return strings.TrimRight(string(body), "\r\n"), nil
}

func parseAssignments(raw string) (map[string]string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("expected KEY=value")
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("expected KEY=value")
	}
	return values, nil
}

func decodeHexKey(raw string) ([]byte, error) {
	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("new key must be 32-byte hex")
	}
	if len(key) != credstore.KeySize {
		return nil, fmt.Errorf("new key must be 32-byte hex")
	}
	return key, nil
}
