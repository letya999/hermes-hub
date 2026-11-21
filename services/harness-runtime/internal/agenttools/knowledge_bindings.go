package agenttools

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type knowledgeBinding struct {
	State   string            `json:"state"`
	Files   map[string][]byte `json:"files,omitempty"`
	Updated time.Time         `json:"updated"`
}

var knowledgeStageMu sync.Mutex

func readKnowledgeBindings(root *os.Root) (map[string]knowledgeBinding, error) {
	bindings := map[string]knowledgeBinding{}
	f, err := root.Open(".bindings.json")
	if os.IsNotExist(err) {
		return bindings, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 48<<20)).Decode(&bindings)
	if err == nil && bindings == nil {
		err = fmt.Errorf("invalid knowledge metadata")
	}
	return bindings, err
}

func StageKnowledgeSource(stateDir, owner, scope, name, state string, files map[string][]byte) (string, error) {
	if !identity.ValidID(owner) || !identity.ValidID(scope) || !identity.ValidID(name) {
		return "", fmt.Errorf("invalid knowledge binding identity")
	}
	if state != "present" && state != "stale" && state != "unavailable" && state != "absent" {
		return "", fmt.Errorf("invalid knowledge state")
	}
	total := 0
	for path, body := range files {
		if len(body) > knowledgeLimit {
			return "", fmt.Errorf("knowledge document exceeds limit")
		}
		total += len(body)
		for _, part := range strings.Split(path, "/") {
			if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || strings.ContainsAny(part, "\\:\x00") {
				return "", fmt.Errorf("invalid knowledge document path")
			}
		}
	}
	if len(files) > 128 || total > 32<<20 {
		return "", fmt.Errorf("knowledge snapshot exceeds limit")
	}
	knowledgeStageMu.Lock()
	defer knowledgeStageMu.Unlock()
	dir := filepath.Join(stateDir, "knowledge", owner)
	if scope != owner {
		dir = filepath.Join(dir, scope)
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", err
	}
	base, err := os.OpenRoot(stateDir)
	if err != nil {
		return "", err
	}
	defer base.Close()
	rel := "knowledge/" + owner
	if scope != owner {
		rel += "/" + scope
	}
	for _, part := range []string{"knowledge", "knowledge/" + owner, rel} {
		if info, err := base.Lstat(part); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return "", fmt.Errorf("knowledge directory must not be an alias")
		}
	}
	if err := base.MkdirAll(rel, 0700); err != nil {
		return "", err
	}
	root, err := base.OpenRoot(rel)
	if err != nil {
		return "", err
	}
	defer root.Close()
	bindings, err := readKnowledgeBindings(root)
	if err != nil {
		return "", err
	}
	binding := bindings[name]
	binding.State = state
	binding.Updated = time.Now().UTC()
	if state == "present" {
		binding.Files = files
	}
	if state == "absent" {
		binding.Files = nil
	}
	bindings[name] = binding
	total = 0
	for _, source := range bindings {
		for _, body := range source.Files {
			total += len(body)
		}
	}
	if total > 32<<20 {
		return "", fmt.Errorf("knowledge context exceeds 32 MiB")
	}
	body, err := json.Marshal(bindings)
	if err != nil {
		return "", err
	}
	f, err := root.OpenFile(".bindings.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
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
	if err = root.Rename(".bindings.tmp", ".bindings.json"); err != nil {
		return "", err
	}
	return dir, nil
}

func boundKnowledge(root *os.Root, name string) (map[string]any, bool, error) {
	source, document, ok := strings.Cut(name, "/")
	if !ok {
		return nil, false, nil
	}
	bindings, err := readKnowledgeBindings(root)
	if err != nil {
		return nil, true, err
	}
	binding, ok := bindings[source]
	if !ok {
		return nil, true, fmt.Errorf("knowledge source unavailable")
	}
	if binding.State != "present" && binding.State != "stale" {
		return nil, true, fmt.Errorf("knowledge source %s: %s", source, binding.State)
	}
	body, ok := binding.Files[document]
	if !ok {
		return nil, true, fmt.Errorf("knowledge document unavailable")
	}
	return map[string]any{"name": name, "content": string(body), "sha256": knowledgeHash(body), "modified_at": binding.Updated.Format(time.RFC3339), "context_id": runtimeEnvelope().ContextID, "freshness": "snapshot", "stale": binding.State == "stale"}, true, nil
}
