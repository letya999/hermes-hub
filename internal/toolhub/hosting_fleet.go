package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/hosting"
)

type hostingFleet struct {
	root string
	max  int
}

func (f hostingFleet) change(ctx context.Context, w hosting.Workload, remove bool) error {
	if !filepath.IsAbs(f.root) || f.max < 1 || f.max > 8 {
		return hosting.ErrUnavailable
	}
	lock := flock.New(filepath.Join(f.root, "budget.lock"))
	ok, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil || !ok {
		return hosting.ErrUnavailable
	}
	defer lock.Unlock()
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return hosting.ErrUnavailable
	}
	defer root.Close()
	entries := map[string]hosting.Workload{}
	file, err := root.Open("budget.json")
	if err == nil {
		decoder := json.NewDecoder(io.LimitReader(file, 2<<20))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&entries)
		extra := decoder.Decode(new(any))
		_ = file.Close()
		if err != nil || extra != io.EOF || entries == nil || len(entries) > 4096 {
			return hosting.ErrUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return hosting.ErrUnavailable
	}
	key := siteObject(w)
	if old, exists := entries[key]; exists && (old.AdmissionDigest != w.AdmissionDigest || old.Binding != w.Binding || old.Name != w.Name || old.Revision != w.Revision || old.Release != w.Release || !old.ExpiresAt.Equal(w.ExpiresAt)) {
		return hosting.ErrDenied
	}
	if remove {
		delete(entries, key)
	} else if _, exists := entries[key]; !exists {
		active, userActive := 0, 0
		for _, lease := range entries {
			// PID1 enforces the immutable hard expiry; keep the cleanup receipt after
			// capacity expires. No container/object-zero claim is inferred from a clock.
			if time.Now().Before(lease.ExpiresAt.Add(30 * time.Second)) {
				active++
				if lease.Binding.Owner == w.Binding.Owner {
					userActive++
				}
			}
		}
		if active >= f.max || userActive >= 2 || len(entries) >= 4096 {
			return hosting.ErrQuota
		}
		entries[key] = w
	}
	data, err := json.Marshal(entries)
	if err != nil || len(data) > 2<<20 {
		return hosting.ErrQuota
	}
	out, err := root.OpenFile("budget.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return hosting.ErrUnavailable
	}
	_, writeErr := out.Write(data)
	syncErr := out.Sync()
	closeErr := out.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || root.Rename("budget.tmp", "budget.json") != nil || syncHostingFleetDirectory(f.root) != nil {
		return hosting.ErrUnavailable
	}
	return nil
}
func (f hostingFleet) owned(ctx context.Context, owner, env string) ([]hosting.Workload, error) {
	// Reading through change's lock keeps shutdown from consuming a partial file.
	lock := flock.New(filepath.Join(f.root, "budget.lock"))
	ok, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil || !ok {
		return nil, hosting.ErrUnavailable
	}
	defer lock.Unlock()
	file, err := os.Open(filepath.Join(f.root, "budget.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, hosting.ErrUnavailable
	}
	defer file.Close()
	var entries map[string]hosting.Workload
	if json.NewDecoder(io.LimitReader(file, 2<<20)).Decode(&entries) != nil {
		return nil, hosting.ErrUnavailable
	}
	out := []hosting.Workload{}
	for _, w := range entries {
		if w.Binding.Owner == owner && w.Binding.Environment == env {
			out = append(out, w)
		}
	}
	return out, nil
}
func (c *genericController) releaseHostingOnShutdown(ctx context.Context) {
	c.siteMu.Lock()
	defer c.siteMu.Unlock()
	for _, p := range c.config.Hosting {
		entries, err := (hostingFleet{c.config.HostingFleetRoot, 8}).owned(ctx, p.Owner, p.Environment)
		if err != nil {
			continue
		}
		for _, w := range entries {
			_ = c.siteRemove(ctx, w, p)
		}
	}
}
