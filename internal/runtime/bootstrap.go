package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/diagnostics"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
)

type bootstrapRecord struct {
	Status           string   `json:"status"`
	Key              string   `json:"key"`
	Changes          []string `json:"changes"`
	ChangesAvailable bool     `json:"changes_available"`
	SessionID        string   `json:"session_id,omitempty"`
	RunID            string   `json:"run_id,omitempty"`
	Output           string   `json:"output,omitempty"`
}

var bootstrapGate = make(chan struct{}, 1)

func boundedStateText(s string, limit int) string {
	runes := []rune(s)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func bootstrapChanges(before, after map[string]string) []string {
	changes := []string{}
	for name, hash := range after {
		if before[name] != hash {
			changes = append(changes, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			changes = append(changes, name)
		}
	}
	sort.Strings(changes)
	return changes
}

func workspaceSnapshot() (map[string]string, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := map[string]string{}
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(files) >= 4096 {
			return errors.New("bootstrap diff exceeds entry bound")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			files[name] = info.Mode().Type().String()
			return nil
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		sum := sha256.New()
		n, copyErr := io.Copy(sum, io.LimitReader(file, (64<<20)-total+1))
		err = copyErr
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
		total += n
		if total > 64<<20 {
			return errors.New("bootstrap diff exceeds byte bound")
		}
		files[name] = hex.EncodeToString(sum.Sum(nil))
		return nil
	})
	return files, err
}

func (s *runtimeHTTP) ensureBootstrap(ctx context.Context, request ExecuteRequest) (runErr error) {
	goal := strings.TrimSpace(os.Getenv("HUB_BOOTSTRAP_GOAL"))
	if goal == "" {
		return nil
	}
	if os.Getenv("HUB_CAPABILITY_MODE") != "managed" {
		return errors.New("bootstrap requires the managed sandbox and its existing egress grants")
	}
	ctx, cancel := context.WithTimeout(ctx, RunBudget())
	defer cancel()
	select {
	case bootstrapGate <- struct{}{}:
		defer func() { <-bootstrapGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	digest := sha256.Sum256([]byte(goal + "\x00" + env("HUB_BOOTSTRAP_REVISION", "1")))
	key := "bootstrap-" + hex.EncodeToString(digest[:12])
	root, err := os.OpenRoot(state)
	if err != nil {
		return err
	}
	defer root.Close()
	var record bootstrapRecord
	if file, err := root.Open("bootstrap.json"); err == nil {
		err = json.NewDecoder(io.LimitReader(file, 8<<20)).Decode(&record)
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
		if record.Key == key {
			if record.Status == "ready" {
				return nil
			}
			return errors.New("bootstrap not ready; explicit retry required")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	save := func() error {
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := root.MkdirAll("bootstrap-history", 0700); err != nil {
			return err
		}
		if err := root.WriteFile("bootstrap-history/"+record.Key+".json", body, 0600); err != nil {
			return err
		}
		return root.WriteFile("bootstrap.json", body, 0600)
	}
	record = bootstrapRecord{Status: "running", Key: key, Changes: []string{}}
	if err := save(); err != nil {
		return err
	}
	var before map[string]string
	defer func() {
		if runErr != nil {
			record.Status = "failed"
			if before != nil {
				after, err := workspaceSnapshot()
				if err == nil {
					record.Changes = bootstrapChanges(before, after)
					record.ChangesAvailable = true
				}
				runErr = errors.Join(runErr, err)
			}
			record.Output = boundedStateText(diagnostics.Redact(record.Output+"\n"+runErr.Error()), 4096)
			runErr = errors.Join(runErr, save())
		}
	}()
	baseline, err := workspaceSnapshot()
	if err != nil {
		return err
	}
	before = baseline
	child := request
	child.JobID, child.IdempotencyKey, child.ConversationID = key, key, key
	child.Text, child.InboundID, child.Instructions = goal, "", ""
	record.SessionID = sessionIDFor(child)
	result, err := s.admitHermesRun(ctx, child, nil)
	record.RunID = result.RunID
	record.Output = boundedStateText(diagnostics.Redact(result.Text), 4096)
	if err != nil {
		return err
	}
	after, err := workspaceSnapshot()
	if err != nil {
		return err
	}
	record.Status, record.Changes, record.ChangesAvailable = "ready", bootstrapChanges(before, after), true
	return save()
}
