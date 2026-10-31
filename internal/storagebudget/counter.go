// Package storagebudget accounts one data owner's existing files without a database.
package storagebudget

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrQuota       = errors.New("owned storage capacity reached")
	ErrUnsafe      = errors.New("unsafe owned storage path")
	ErrUnavailable = errors.New("owned storage I/O unavailable")
	ErrLimits      = errors.New("invalid owned storage limits")
)

type Limits struct {
	Bytes          int64
	Files          int
	NextWrite      int64
	LifecycleBytes int64
	LifecycleFiles int
}

func (l Limits) Validate() error {
	if l.Bytes <= 0 || l.Files < 1 || l.NextWrite <= 0 || l.LifecycleBytes < 0 || l.LifecycleFiles < 0 || l.LifecycleBytes >= l.Bytes || l.NextWrite > l.Bytes-l.LifecycleBytes || l.LifecycleFiles >= l.Files {
		return ErrLimits
	}
	return nil
}

type Snapshot struct {
	LogicalBytes  int64     `json:"logical_bytes"`
	Files         int       `json:"filesystem_entries"`
	ReservedBytes int64     `json:"reserved_bytes"`
	ReservedFiles int       `json:"reserved_files"`
	Complete      bool      `json:"complete"`
	ReconciledAt  time.Time `json:"reconciled_at"`
}

type Counter struct {
	mu     sync.Mutex
	root   string
	roots  []string
	limits Limits
	sizes  map[string]int64
	state  Snapshot
	dirty  bool
}

type ioError struct{ cause error }

func (e ioError) Error() string { return ErrUnavailable.Error() }
func (e ioError) Unwrap() error { return errors.Join(ErrUnavailable, e.cause) }
func safeIO(err error) error {
	if err == nil {
		return nil
	}
	return ioError{err}
}

func New(root string, limits Limits) (*Counter, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, ErrUnsafe
	}
	c := &Counter{root: filepath.Clean(absolute), limits: limits, sizes: map[string]int64{}}
	c.roots = []string{c.root}
	if _, err := c.checkedPath(c.root, true); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(c.root); err != nil {
		return nil, safeIO(err)
	} else if !info.IsDir() {
		return nil, ErrUnsafe
	}
	if err := c.reconcileLocked(); err != nil {
		return nil, err
	}
	return c, nil
}

// Paths are never exported in errors or snapshots, including filesystem errors.
func (c *Counter) checkedPath(path string, allowRoot bool) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", ErrUnsafe
	}
	absolute = filepath.Clean(absolute)
	ownerRoot := c.rootFor(absolute)
	if ownerRoot == "" || absolute == ownerRoot && !allowRoot {
		return "", ErrUnsafe
	}

	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", ErrUnsafe
			}
		} else if !os.IsNotExist(err) {
			return "", safeIO(err)
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return absolute, nil
}

func (c *Counter) reconcileLocked() error {
	sizes := map[string]int64{}
	var bytes int64
	count := 0
	err := error(nil)
	for _, root := range c.roots {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return safeIO(walkErr)
			}
			if d.Type()&os.ModeSymlink != 0 {
				return ErrUnsafe
			}
			if path == root {
				return nil
			}
			count++
			if count > c.limits.Files {
				return ErrQuota
			}
			if d.IsDir() {
				sizes[path] = 0
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return safeIO(err)
			}
			if !info.Mode().IsRegular() {
				return ErrUnsafe
			}
			if info.Size() > int64(^uint64(0)>>1)-bytes {
				return ErrQuota
			}
			bytes += info.Size()
			sizes[path] = info.Size()
			return nil
		})
		if err != nil {
			break
		}
	}
	if err != nil {
		c.dirty = true
		c.state.Complete = false
		return err
	}
	c.sizes = sizes
	c.state.LogicalBytes = bytes
	c.state.Files = count
	c.state.Complete = true
	c.state.ReconciledAt = time.Now().UTC()
	c.dirty = false
	return nil
}

func (c *Counter) refreshLocked() error {
	if c.dirty || !c.state.Complete || time.Since(c.state.ReconciledAt) >= 30*time.Second {
		return c.reconcileLocked()
	}
	return nil
}
func (c *Counter) Reconcile() error { c.mu.Lock(); defer c.mu.Unlock(); return c.reconcileLocked() }
func (c *Counter) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.state
	state.Complete = state.Complete && !c.dirty
	return state
}
func (c *Counter) Invalidate() { c.mu.Lock(); c.dirty = true; c.mu.Unlock() }

func (c *Counter) fitsLocked(bytes int64, files int, lifecycle bool) error {
	if bytes < 0 || bytes > c.limits.NextWrite || files < 0 {
		return ErrLimits
	}
	byteCap, fileCap := c.limits.Bytes, c.limits.Files
	if !lifecycle {
		byteCap -= c.limits.LifecycleBytes
		fileCap -= c.limits.LifecycleFiles
	}
	if c.state.ReservedBytes > byteCap || c.state.LogicalBytes > byteCap-c.state.ReservedBytes || bytes > byteCap-c.state.ReservedBytes-c.state.LogicalBytes || c.state.Files+c.state.ReservedFiles > fileCap-files {
		return ErrQuota
	}
	return nil
}
func (c *Counter) CheckAdmission() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.refreshLocked(); err != nil {
		return err
	}
	return c.fitsLocked(c.limits.NextWrite, 2, false)
}

// Write reserves the replacement scratch while the previous file is still live.
func (c *Counter) Write(path string, bytes int64, lifecycle bool, write func() error) error {
	return c.write(path, bytes, lifecycle, 1, write)
}

func (c *Counter) Update(path string, growth int64, lifecycle bool, update func() error) error {
	return c.write(path, growth, lifecycle, 0, update)
}

func (c *Counter) write(path string, bytes int64, lifecycle bool, scratchFiles int, write func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	absolute, err := c.checkedPath(path, false)
	if err != nil {
		return err
	}
	if err := c.refreshLocked(); err != nil {
		return err
	}
	if info, err := os.Lstat(absolute); err == nil {
		if !info.Mode().IsRegular() {
			return ErrUnsafe
		}
		if previous, ok := c.sizes[absolute]; !ok || previous != info.Size() {
			if err := c.reconcileLocked(); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return safeIO(err)
	}
	for dir := filepath.Dir(absolute); dir != c.rootFor(absolute); dir = filepath.Dir(dir) {
		if _, known := c.sizes[dir]; !known {
			info, err := os.Lstat(dir)
			if err != nil {
				return safeIO(err)
			}
			if !info.IsDir() {
				return ErrUnsafe
			}
			c.sizes[dir] = 0
			c.state.Files++
		}
	}
	extra := scratchFiles + 1
	if _, ok := c.sizes[absolute]; ok {
		extra = scratchFiles
	}
	if err := c.fitsLocked(bytes, extra, lifecycle); err != nil {
		return err
	}
	writeErr := write()
	if info, err := os.Lstat(absolute); err == nil && info.Mode().IsRegular() {
		old, known := c.sizes[absolute]
		c.state.LogicalBytes += info.Size() - old
		if !known {
			c.state.Files++
		}
		c.sizes[absolute] = info.Size()
	} else {
		c.dirty = true
	}
	if writeErr != nil {
		c.dirty = true
		return safeIO(writeErr)
	}
	return nil
}

// Reserve covers a bounded streaming or multi-file owner operation. Reconcile
// its final bytes after close; a process restart reconstructs them from disk.
func (c *Counter) Reserve(bytes int64, files int, lifecycle bool) (func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.refreshLocked(); err != nil {
		return nil, err
	}
	if err := c.fitsLocked(bytes, files, lifecycle); err != nil {
		return nil, err
	}
	c.state.ReservedBytes += bytes
	c.state.ReservedFiles += files
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.state.ReservedBytes -= bytes
			c.state.ReservedFiles -= files
			c.dirty = true
		})
	}, nil
}

func (c *Counter) Rename(from, to string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, err := c.checkedPath(from, false)
	if err != nil {
		return err
	}
	b, err := c.checkedPath(to, false)
	if err != nil {
		return err
	}
	if err := os.Rename(a, b); err != nil {
		return safeIO(err)
	}
	size, known := c.sizes[a]
	replaced, exists := c.sizes[b]
	if known {
		delete(c.sizes, a)
		c.sizes[b] = size
		c.state.LogicalBytes -= replaced
		if exists {
			c.state.Files--
		}
	} else {
		c.dirty = true
	}
	return nil
}
func (c *Counter) Remove(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	absolute, err := c.checkedPath(path, false)
	if err != nil {
		return err
	}
	if err := os.Remove(absolute); err != nil {
		return safeIO(err)
	}
	if size, known := c.sizes[absolute]; known {
		delete(c.sizes, absolute)
		c.state.LogicalBytes -= size
		c.state.Files--
	} else {
		c.dirty = true
	}
	return nil
}

func (c *Counter) MkdirAll(path string, lifecycle bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	absolute, err := c.checkedPath(path, true)
	if err != nil {
		return err
	}
	if absolute == c.rootFor(absolute) {
		return nil
	}
	if err := c.refreshLocked(); err != nil {
		return err
	}
	var added []string
	for current := absolute; current != c.rootFor(absolute); current = filepath.Dir(current) {
		if _, known := c.sizes[current]; !known {
			added = append(added, current)
		}
	}
	if err := c.fitsLocked(0, len(added), lifecycle); err != nil {
		return err
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		c.dirty = true
		return safeIO(err)
	}
	for _, dir := range added {
		c.sizes[dir] = 0
		c.state.Files++
	}
	return nil
}

func (c *Counter) rootFor(path string) string {
	for _, root := range c.roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return root
		}
	}
	return ""
}

// AddRoot is host configuration, never a path supplied by an event or tool call.
func (c *Counter) AddRoot(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return ErrUnsafe
	}
	absolute = filepath.Clean(absolute)
	if c.rootFor(absolute) != "" {
		info, err := os.Lstat(absolute)
		if err != nil {
			return safeIO(err)
		}
		if !info.IsDir() {
			return ErrUnsafe
		}
		return nil
	}
	if len(c.roots) >= 16 {
		return ErrLimits
	}
	previous := c.roots
	var roots []string
	for _, root := range previous {
		relative, err := filepath.Rel(absolute, root)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			roots = append(roots, root)
		}
	}
	c.roots = append(roots, absolute)
	if _, err := c.checkedPath(absolute, true); err != nil {
		c.roots = previous
		return err
	}
	if info, err := os.Lstat(absolute); err != nil || !info.IsDir() {
		c.roots = previous
		if err != nil {
			return safeIO(err)
		}
		return ErrUnsafe
	}
	if err := c.reconcileLocked(); err != nil {
		c.roots = previous
		c.dirty = true
		return err
	}
	return nil
}
