// Package materialize converts typed credentials into bounded tmpfs files and
// process env for a trusted runtime adapter. It never starts containers.
package materialize

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
	"github.com/letya999/credential-broker/provider"
)

var leaseID = regexp.MustCompile(`^l[0-9a-f]{32}$`)

var ErrMaterialize = errors.New("materialization failed")

type Manager struct {
	mu     sync.Mutex
	root   string
	active map[string]bool
}

func New(root string, requireTmpfs bool) (*Manager, error) {
	if e := securefs.Dir(root); e != nil {
		return nil, e
	}
	if requireTmpfs {
		if e := securefs.Tmpfs(root); e != nil {
			return nil, e
		}
	}
	// On a fresh process, leases are deliberately not valid. Remove only generated
	// lease directories; unexpected files stop startup rather than being deleted.
	entries, e := os.ReadDir(root)
	if e != nil {
		return nil, e
	}
	for _, entry := range entries {
		if !leaseID.MatchString(entry.Name()) || entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return nil, ErrMaterialize
		}
		if e := os.RemoveAll(filepath.Join(root, entry.Name())); e != nil {
			return nil, e
		}
	}
	return &Manager{root: root, active: map[string]bool{}}, nil
}
func (m *Manager) Prepare(id string, expires time.Time, c contract.Contract, b, state provider.Bundle) (v1.Materialized, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := v1.Materialized{LeaseID: id, ExpiresAt: expires, Env: map[string]string{}}
	if !leaseID.MatchString(id) || c.Validate() != nil || c.ValidateValues(b) != nil {
		return out, ErrMaterialize
	}
	root := filepath.Join(m.root, id)
	if m.active[id] {
		return out, errors.New("lease already materialized")
	}
	if e := os.Mkdir(root, 0700); e != nil {
		return out, ErrMaterialize
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(root)
		}
	}()
	for i, d := range c.Deliveries {
		if d.Type == "env" {
			if v, exists := b[d.Field]; exists {
				out.Env[d.Target] = string(v)
			}
			continue
		}
		raw := b[d.Field]
		if d.Type == "json_file" {
			obj := map[string]string{}
			for k, v := range d.Mapping {
				obj[k] = string(b[v])
			}
			var e error
			raw, e = json.Marshal(obj)
			if e != nil {
				return out, ErrMaterialize
			}
		} else if _, exists := b[d.Field]; !exists {
			continue
		}
		name := fmt.Sprintf("file-%02d", i)
		if e := securefs.Create(root, name, raw, 0400); e != nil {
			return out, ErrMaterialize
		}
		out.Mounts = append(out.Mounts, v1.Mount{Source: filepath.Join(root, name), Target: d.Target, ReadOnly: true})
		if d.EnvName != "" {
			out.Env[d.EnvName] = d.Target
		}
	}
	if c.State != nil {
		stateDir := filepath.Join(root, "state")
		if e := os.Mkdir(stateDir, 0700); e != nil {
			return out, ErrMaterialize
		}
		for _, f := range c.State.Files {
			if value, present := state[f.Name]; present {
				if len(value) > f.MaxBytes || f.JSON && !strictjson.Valid(value) {
					return out, ErrMaterialize
				}
				if e := securefs.Create(stateDir, f.Name, value, 0600); e != nil {
					return out, ErrMaterialize
				}
			}
		}
		out.Mounts = append(out.Mounts, v1.Mount{Source: stateDir, Target: c.State.Target, ReadOnly: false})
	}
	m.active[id] = true
	ok = true
	return out, nil
}
func (m *Manager) Snapshot(id string, c contract.Contract) (provider.Bundle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !identity.ValidID(id) || !m.active[id] || c.State == nil {
		return nil, ErrMaterialize
	}
	out := provider.Bundle{}
	total := 0
	dir := filepath.Join(m.root, id, "state")
	for _, f := range c.State.Files {
		b, e := securefs.Read(dir, f.Name, int64(f.MaxBytes))
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, ErrMaterialize
		}
		if f.JSON && !strictjson.Valid(b) {
			return nil, ErrMaterialize
		}
		total += len(b)
		if total > contract.MaxBundle {
			return nil, ErrMaterialize
		}
		out[f.Name] = b
	}
	return out, nil
}
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !leaseID.MatchString(id) {
		return ErrMaterialize
	}
	if e := os.RemoveAll(filepath.Join(m.root, id)); e != nil {
		return ErrMaterialize
	}
	delete(m.active, id)
	return nil
}
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result error
	for id := range m.active {
		if e := os.RemoveAll(filepath.Join(m.root, id)); e != nil {
			result = ErrMaterialize
		} else {
			delete(m.active, id)
		}
	}
	return result
}
