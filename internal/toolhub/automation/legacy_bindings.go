package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/letya999/hermes-hub/internal/commops"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// legacyBindings reads communication-owned task and interactive-job bindings
// in place. Automation records remain exclusively in the ToolHub owner tree.
type legacyBindings struct{ commops.LegacySpoolReader }

func (b legacyBindings) directory(parts ...string) (string, error) {
	if info, err := os.Lstat(b.Root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("automation: binding directory unavailable")
	}
	db, err := b.Database()
	if err != nil {
		return "", err
	}
	if db != nil {
		_ = db.Close()
		return filepath.Join(append([]string{b.Root}, parts...)...), nil
	}
	for depth := 0; depth <= len(parts); depth++ {
		path := filepath.Join(append([]string{b.Root}, parts[:depth]...)...)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("automation: binding directory unavailable")
		}
	}
	return filepath.Join(append([]string{b.Root}, parts...)...), nil
}

func (b legacyBindings) taskPath(principal, taskID string) string {
	hash := sha256.Sum256([]byte(principal + "\x00" + taskID))
	return filepath.Join(b.Root, "tasks", hex.EncodeToString(hash[:])+".json")
}

func (b legacyBindings) topicPath(principal string, chatID, topicID int64) string {
	hash := sha256.Sum256([]byte(principal + "\x00" + strconv.FormatInt(chatID, 10) + "\x00" + strconv.FormatInt(topicID, 10)))
	return filepath.Join(b.Root, "tasks", "topic", hex.EncodeToString(hash[:])+".json")
}

func (b legacyBindings) loadTask(principal, taskID string) (commodel.Task, error) {
	if !identity.ValidID(principal) || taskID == "" {
		return commodel.Task{}, errUnauthorized
	}
	if _, err := b.directory("tasks"); err != nil {
		return commodel.Task{}, err
	}
	var task commodel.Task
	if err := b.ReadJSON(b.taskPath(principal, taskID), &task); err != nil {
		return commodel.Task{}, err
	}
	if task.PrincipalID != principal || task.TaskID != taskID {
		return commodel.Task{}, errors.New("automation: task binding mismatch")
	}
	return task, nil
}

func (b legacyBindings) listTasks(principal string) ([]commodel.Task, error) {
	if !identity.ValidID(principal) {
		return nil, errUnauthorized
	}
	dir, err := b.directory("tasks")
	if err != nil {
		return nil, err
	}
	entries, err := b.Entries(dir)
	if err != nil {
		return nil, err
	}
	tasks := []commodel.Task{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry, ".json") {
			continue
		}
		var task commodel.Task
		if err := b.ReadJSON(filepath.Join(dir, entry), &task); err != nil {
			return nil, err
		}
		if task.PrincipalID != principal {
			continue
		}
		if task.TaskID == "" || b.taskPath(principal, task.TaskID) != filepath.Join(dir, entry) {
			return nil, errors.New("automation: invalid task binding")
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
	return tasks, nil
}

func (b legacyBindings) topicTask(principal string, chatID, topicID int64) (string, error) {
	if !identity.ValidID(principal) || chatID <= 0 || topicID <= 0 {
		return "", errUnauthorized
	}
	if _, err := b.directory("tasks", "topic"); err != nil {
		return "", err
	}
	var pointer struct {
		TaskID string `json:"task_id"`
	}
	if err := b.ReadJSON(b.topicPath(principal, chatID, topicID), &pointer); err != nil {
		return "", err
	}
	if pointer.TaskID == "" {
		return "", errors.New("automation: invalid topic binding")
	}
	return pointer.TaskID, nil
}

func (b legacyBindings) loadMapping(id string) (commodel.JobMapping, error) {
	if id == "" || len(id) > 256 {
		return commodel.JobMapping{}, errUnauthorized
	}
	if _, err := b.directory("mappings"); err != nil {
		return commodel.JobMapping{}, err
	}
	path := filepath.Join(b.Root, "mappings", commodel.FileID(id)+".json")
	var mapping commodel.JobMapping
	if err := b.ReadJSON(path, &mapping); err != nil {
		return commodel.JobMapping{}, err
	}
	if mapping.JobID != id {
		return commodel.JobMapping{}, errors.New("automation: invalid origin mapping")
	}
	return mapping, nil
}
