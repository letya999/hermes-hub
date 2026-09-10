package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

const selfServicesFile = "self-services.json"

type serviceState struct {
	Features []string `json:"features"`
}

func loadSelfServices() error {
	features, err := readSelfServices()
	if err != nil {
		return err
	}
	active := map[string]bool{}
	for _, name := range strings.Split(os.Getenv("HUB_FEATURES"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			active[name] = true
		}
	}
	for _, name := range features {
		active[name] = true
	}
	all := make([]string, 0, len(active))
	for name := range active {
		all = append(all, name)
	}
	slices.Sort(all)
	if err := os.Setenv("HUB_ACTIVE_FEATURES", strings.Join(all, ",")); err != nil {
		return err
	}
	if active["hh"] {
		if err := os.Setenv("HUB_HH_ENABLED", "true"); err != nil {
			return err
		}
	}
	return nil
}

func readSelfServices() ([]string, error) {
	path := filepath.Join(state, selfServicesFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("self-services must be a regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(body) > 64*1024 {
		return nil, errors.New("self-services file is too large")
	}
	var current serviceState
	if err := json.Unmarshal(body, &current); err != nil {
		return nil, fmt.Errorf("invalid self-services file: %w", err)
	}
	seen := map[string]bool{}
	for _, name := range current.Features {
		info, ok := stack.ServiceInfoByName(name)
		if !ok || !info.SelfService {
			return nil, fmt.Errorf("service %q is not self-service", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate service %q", name)
		}
		seen[name] = true
	}
	slices.Sort(current.Features)
	return current.Features, nil
}

func applySelfServices(configPath string) error {
	features, err := readSelfServices()
	if err != nil || len(features) == 0 {
		return err
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var config map[string]any
	if err := yaml.Unmarshal(body, &config); err != nil {
		return fmt.Errorf("read Hermes config: %w", err)
	}
	servers, ok := config["mcp_servers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
		config["mcp_servers"] = servers
	}
	for _, feature := range features {
		name, server, ok, err := stack.ServiceMCPConfig(feature)
		if err != nil {
			return err
		}
		if ok && name != "" {
			servers[name] = server
		}
	}
	updated, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("write Hermes config: %w", err)
	}
	return os.WriteFile(configPath, updated, 0660)
}
