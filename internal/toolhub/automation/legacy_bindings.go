package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
type legacyBindings struct{ root string }

func (b legacyBindings) directory(parts ...string) (string, error) {
	for depth := 0; depth <= len(parts); depth++ {
		path := filepath.Join(append([]string{b.root}, parts[:depth]...)...)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("automation: binding directory unavailable")
		}
	}
	return filepath.Join(append([]string{b.root}, parts...)...), nil
}

func (b legacyBindings) taskPath(principal, taskID string) string {
	hash := sha256.Sum256([]byte(principal + "\x00" + taskID))
	return filepath.Join(b.root, "tasks", hex.EncodeToString(hash[:])+".json")
}

func (b legacyBindings) topicPath(principal string, chatID, topicID int64) string {
	hash := sha256.Sum256([]byte(principal + "\x00" + strconv.FormatInt(chatID, 10) + "\x00" + strconv.FormatInt(topicID, 10)))
	return filepath.Join(b.root, "tasks", "topic", hex.EncodeToString(hash[:])+".json")
}

func (b legacyBindings) loadTask(principal, taskID string) (commodel.Task, error) {
	if !identity.ValidID(principal) || taskID == "" {
		return commodel.Task{}, errUnauthorized
	}
	if _, err := b.directory("tasks"); err != nil {
		return commodel.Task{}, err
	}
	var task commodel.Task
	if err := readRoutingJSON(b.taskPath(principal, taskID), &task); err != nil {
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	tasks := []commodel.Task{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var task commodel.Task
		if err := readRoutingJSON(filepath.Join(dir, entry.Name()), &task); err != nil {
			return nil, err
		}
		if task.PrincipalID != principal {
			continue
		}
		if task.TaskID == "" || b.taskPath(principal, task.TaskID) != filepath.Join(dir, entry.Name()) {
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
	if err := readRoutingJSON(b.topicPath(principal, chatID, topicID), &pointer); err != nil {
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
	path := filepath.Join(b.root, "mappings", commodel.FileID(id)+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return commodel.JobMapping{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return commodel.JobMapping{}, errors.New("automation: invalid origin mapping")
	}
	body, err := os.ReadFile(path) // #nosec G703 -- bounded local read-only mapping
	if err != nil {
		return commodel.JobMapping{}, err
	}
	var mapping commodel.JobMapping
	if json.Unmarshal(body, &mapping) != nil || mapping.JobID != id {
		return commodel.JobMapping{}, errors.New("automation: invalid origin mapping")
	}
	return mapping, nil
}
