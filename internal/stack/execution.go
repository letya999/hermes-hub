package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// ExecutionSelection is host-owned rollout state, independent of Hermes' home.
// An absent file keeps the previous release's routing behavior.
type ExecutionSelection struct {
	Schema               int    `json:"schema"`
	User                 string `json:"user"`
	Environment          string `json:"environment"`
	Mode                 string `json:"mode"`
	SupervisorURL        string `json:"supervisor_url,omitempty"`
	NativeCron           string `json:"native_cron"`
	CompatibilityRelease string `json:"compatibility_release"`
}

func (s ExecutionSelection) Validate() error {
	if s.Schema != 1 || !idPattern.MatchString(s.User) || (s.Environment != "prod" && s.Environment != "dev") || (s.Mode != "static" && s.Mode != "supervisor") || s.CompatibilityRelease == "" {
		return errors.New("invalid execution selection identity, mode or compatibility release")
	}
	if s.NativeCron != "disabled" && s.NativeCron != "migrated" && s.NativeCron != "unmigrated" {
		return errors.New("explicit native cron disposition required")
	}
	if s.Mode == "supervisor" {
		if s.NativeCron == "unmigrated" {
			return errors.New("unmigrated native cron requires static pinned-on deployment")
		}
		u, err := url.Parse(s.SupervisorURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("supervisor URL must be an HTTP(S) origin without credentials")
		}
	} else if s.SupervisorURL != "" {
		return errors.New("static selection cannot route to supervisor")
	}
	return nil
}

func ExecutionPath(environment string) string { return "execution." + environment + ".json" }

func ReadExecution(dir, environment, user string) (ExecutionSelection, bool, error) {
	var selection ExecutionSelection
	if (environment != "prod" && environment != "dev") || !idPattern.MatchString(user) {
		return selection, false, errors.New("invalid execution selection target")
	}
	path := filepath.Join(dir, ExecutionPath(environment))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return selection, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16*1024 {
		return selection, false, errors.New("execution selection is not a bounded regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return selection, false, err
	}
	if err = json.Unmarshal(body, &selection); err != nil {
		return selection, false, err
	}
	// Compatibility records select topology, never the retired one-shot executor.
	if selection.Mode == "legacy" {
		selection.Mode = "static"
	}
	if err = selection.Validate(); err != nil {
		return selection, false, err
	}
	if selection.User != user || selection.Environment != environment {
		return selection, false, fmt.Errorf("execution selection belongs to another user/environment")
	}
	return selection, true, nil
}
