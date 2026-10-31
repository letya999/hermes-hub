package hosting

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

type Routes interface {
	Ensure(context.Context, string) error
	Remove(context.Context, string) error
}
type Options struct {
	Executor Executor
	Now      func() time.Time
	Routes   Routes
}
type Service struct {
	relaySlots    chan struct{}
	userRate      rateBudget
	siteRates     map[string]*rateBudget
	storageFault  bool
	bandwidthMu   sync.Mutex
	bandwidthNext time.Time
	publishAt     time.Time
	publishTokens float64
	auditReserved int64
	auditFault    bool
	executor      Executor
	sessionKey    [32]byte
	inflight      map[uint64]activeRequest
	nextRequest   uint64
	mu            sync.Mutex
	cfg           Config
	root          *os.Root
	workspace     *os.Root
	lock          *flock.Flock
	state         diskState
	now           func() time.Time
	routes        Routes
	closed        bool
}

func Open(cfg Config, opts Options) (*Service, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Store, 0700); err != nil {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(cfg.Store)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	lock := flock.New(filepath.Join(cfg.Store, "writer.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, ErrUnavailable
	}
	if !ok {
		return nil, ErrConflict
	}
	root, err := os.OpenRoot(cfg.Store)
	if err != nil {
		_ = lock.Unlock()
		return nil, ErrUnavailable
	}
	workspace, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		_ = root.Close()
		_ = lock.Unlock()
		return nil, ErrUnavailable
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &Service{cfg: cfg, root: root, workspace: workspace, lock: lock, now: now, routes: opts.Routes, executor: opts.Executor, state: diskState{Schema: 1, Binding: Binding{cfg.Owner, cfg.Environment}, Sites: map[string]record{}, Operations: map[string]operation{}, Pending: map[string]record{}, Retired: map[string]record{}, Sequences: map[string]uint64{}}}
	if _, err = rand.Read(s.sessionKey[:]); err != nil {
		_ = s.Close()
		return nil, ErrUnavailable
	}
	s.inflight = map[uint64]activeRequest{}
	s.relaySlots = make(chan struct{}, 2)
	s.siteRates = map[string]*rateBudget{}
	s.publishTokens = 2
	s.publishAt = s.now()
	if err = s.claimStore(); err != nil {
		_ = s.Close()
		return nil, err
	}
	if err = s.recoverAudit(); err == nil {
		err = s.load()
	}
	if err == nil {
		recoverContext, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err = s.recoverLocked(recoverContext)
		cancel()
	}
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Service) Binding() Binding { return Binding{s.cfg.Owner, s.cfg.Environment} }
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for _, active := range s.inflight {
		active.cancel()
	}
	_ = s.workspace.Close()
	_ = s.root.Close()
	return s.lock.Unlock()
}
func (s *Service) load() error {
	f, err := s.root.Open("state.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 2<<20))
	d.DisallowUnknownFields()
	var st diskState
	if d.Decode(&st) != nil || d.Decode(new(any)) != io.EOF || st.Schema != 1 || st.Binding != s.Binding() || st.Sites == nil || st.Operations == nil || len(st.Sites) > 1024 || len(st.Operations) > 1024 {
		return ErrUnavailable
	}
	validationConfig := s.cfg
	validationConfig.Slots = nil
	validationConfig.Reserved = nil
	for name, r := range st.Sites {
		if !validationConfig.validName(name) || r.Name != name || r.Revision == 0 || !validRelease(r.Release) || r.Bytes < 0 || r.Files < 0 || (r.State != "ready" && r.State != "removed" && r.State != "failed") || r.Origin != s.cfg.origin(name) || (r.Access != "link" && r.Access != "public" && r.Access != "private") || (r.Mode != "static" && r.Mode != "dynamic") {
			return ErrUnavailable
		}
	}
	if st.Pending == nil {
		st.Pending = map[string]record{}
	}
	if st.Retired == nil {
		st.Retired = map[string]record{}
	}
	for key, r := range st.Pending {
		if key != r.Release || !validRelease(key) || !validationConfig.validName(r.Name) || r.Revision == 0 {
			return ErrUnavailable
		}
	}
	for key, r := range st.Retired {
		if key != r.Release || !validRelease(key) || !validationConfig.validName(r.Name) || r.Revision == 0 {
			return ErrUnavailable
		}
	}
	if st.Sequences == nil {
		st.Sequences = map[string]uint64{}
	}
	for name, n := range st.Sequences {
		if !validationConfig.validName(name) || n == 0 {
			return ErrUnavailable
		}
	}
	s.state = st
	return nil
}
func (s *Service) save() error {
	body, err := json.Marshal(s.state)
	if err != nil || len(body) > 2<<20 {
		return ErrQuota
	}
	used, files, usageErr := s.usage()
	if usageErr != nil {
		return usageErr
	}
	if used+int64(len(body)) > s.cfg.MaxBytes || files+1 > s.cfg.MaxFiles {
		return ErrQuota
	}
	f, err := s.root.OpenFile("state.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return ErrUnavailable
	}
	_, werr := f.Write(body)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		return ErrUnavailable
	}
	if s.root.Rename("state.tmp", "state.json") != nil {
		return ErrUnavailable
	}
	if syncDirectory(s.cfg.Store) != nil {
		s.storageFault = true
		return ErrUnavailable
	}
	return nil
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func randomID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(b[:]), nil
}
func (s *Service) authorized(b Binding) error {
	if s.closed || s.storageFault || s.auditFault {
		return ErrUnavailable
	}
	if b != s.Binding() {
		return ErrDenied
	}
	return nil
}
func (s *Service) replay(key string, input any) (Site, bool, error) {
	if !id.MatchString(key) {
		return Site{}, false, ErrInvalid
	}
	if op, ok := s.state.Operations[key]; ok {
		if op.Digest != digest(input) {
			return Site{}, true, ErrConflict
		}
		return op.Result, true, nil
	}

	return Site{}, false, nil
}
func (s *Service) Publish(ctx context.Context, b Binding, r PublishRequest) (resultSite Site, resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorized(b); err != nil {
		return Site{}, err
	}
	if v, ok, err := s.replay(r.RequestID, r); ok || err != nil {
		return v, err
	}
	if len(s.state.Operations) >= 1024-s.cfg.MaxSites {
		return Site{}, ErrQuota
	}
	encoded, _ := json.Marshal(s.state)
	if len(encoded) > (2<<20)-(64<<10) {
		return Site{}, ErrQuota
	}
	wireDigest := digest(r)
	elapsed := s.now().Sub(s.publishAt).Seconds()
	s.publishTokens = min(2, s.publishTokens+elapsed/10)
	s.publishAt = s.now()
	if s.publishTokens < 1 {
		return Site{}, ErrQuota
	}
	s.publishTokens--
	if !s.cfg.validName(r.Name) || !safePath(r.Source) {
		return Site{}, ErrInvalid
	}
	if r.Mode == "" {
		r.Mode = "static"
	}
	if r.Access == "" {
		r.Access = "link"
	}
	if r.LifetimeSeconds == 0 {
		r.LifetimeSeconds = 7 * 24 * 3600
	}
	if r.Mode != "static" && r.Mode != "dynamic" || r.Mode == "static" && r.Profile != "" || r.Mode == "dynamic" && (r.Profile != "go-http" || s.executor == nil) {
		return Site{}, ErrUnavailable
	}
	if r.Access != "link" && r.Access != "public" && r.Access != "private" || r.LifetimeSeconds < 1 || r.LifetimeSeconds > 30*24*3600 {
		return Site{}, ErrInvalid
	}
	if r.Mode == "dynamic" {
		if err := s.appBinary(r.Source); err != nil {
			return Site{}, err
		}
	}
	old, exists := s.state.Sites[r.Name]
	if old.Revision != r.ExpectedRevision && !(old.State == "removed" && r.ExpectedRevision == 0) {
		return Site{}, ErrConflict
	}
	if len(s.state.Pending) >= 16 || len(s.state.Retired) >= 16 {
		return Site{}, ErrQuota
	}
	active := 0
	dynamic := 0
	for _, v := range s.state.Pending {
		if v.Mode == "dynamic" {
			dynamic++
		}
	}
	for _, v := range s.state.Retired {
		if v.Mode == "dynamic" {
			dynamic++
		}
	}
	for _, v := range s.state.Sites {
		if v.State != "removed" {
			if v.Mode == "dynamic" {
				dynamic++
			}
			active++
		}
	}
	if (!exists || old.State == "removed") && active >= s.cfg.MaxSites {
		return Site{}, ErrQuota
	}
	if r.Mode == "dynamic" && dynamic >= s.cfg.MaxDynamic {
		return Site{}, ErrQuota
	}
	if len(s.state.Sites) >= 1024 && !exists {
		return Site{}, ErrQuota
	}
	sequence := max(s.state.Sequences[r.Name], old.Revision)
	if sequence == ^uint64(0) {
		return Site{}, ErrQuota
	}
	sequence++
	auditDone, err := s.beginAudit("publish", r.Name, sequence)
	if err != nil {
		return Site{}, err
	}
	success := false
	// Allocate before external effects; failed preparations never reuse a revision.
	s.state.Sequences[r.Name] = sequence
	s.state.Operations[r.RequestID] = operation{Digest: wireDigest, Result: Site{Name: r.Name, Revision: sequence, State: "pending", Mode: r.Mode, Access: r.Access, Origin: s.cfg.origin(r.Name), ExpiresAt: s.now().Add(time.Duration(r.LifetimeSeconds) * time.Second)}}
	if err = s.save(); err != nil {
		return Site{}, err
	}
	defer func() {
		if !success && !s.storageFault {
			op := s.state.Operations[r.RequestID]
			op.Result.State = "failed"
			s.state.Operations[r.RequestID] = op
			_ = s.save()
		}

		outcome := "failed"
		if success {
			outcome = "complete"
		}
		if err := auditDone(outcome, 0); err != nil {
			resultSite = Site{}
			resultErr = ErrUnavailable
		}
	}()
	release, err := randomID()
	if err != nil {
		return Site{}, err
	}
	bytes, files, err := s.snapshot(ctx, r.Source, release)
	if err != nil {
		_ = s.root.RemoveAll(release)
		return Site{}, err
	}
	if r.Mode == "dynamic" {
		// The fixed launch contract executes /work/app inside the read-only
		// release; mark the committed binary executable at admission.
		if err = s.root.Chmod(release+"/app", 0o500); err != nil {
			_ = s.root.RemoveAll(release)
			return Site{}, ErrUnavailable
		}
	}
	next := record{AdmissionDigest: s.cfg.AdmissionDigest, Site: Site{Name: r.Name, Revision: sequence, State: "ready", Mode: r.Mode, Access: r.Access, Origin: s.cfg.origin(r.Name), ExpiresAt: s.now().Add(time.Duration(r.LifetimeSeconds) * time.Second)}, Release: release, Bytes: bytes, Files: files}
	s.state.Pending[release] = next
	if err = s.save(); err != nil {
		delete(s.state.Pending, release)
		_ = s.root.RemoveAll(release)
		return Site{}, err
	}
	if next.Mode == "dynamic" {
		if err = s.executor.Ensure(ctx, s.workload(next)); err != nil {
			return Site{}, ErrUnavailable
		}
	}
	raw := ""
	if r.Access == "link" {
		raw, err = randomID()
		if err != nil {
			return Site{}, err
		}
		next.TokenHash = digest(raw)
		next.ShareExpires = minTime(next.ExpiresAt, s.now().Add(24*time.Hour))
	}
	if r.Access != "private" {
		if s.routes == nil {
			return Site{}, ErrUnavailable
		}
		if err = s.routes.Ensure(ctx, r.Name); err != nil {
			return Site{}, ErrUnavailable
		}
	}
	delete(s.state.Pending, release)
	if exists && old.State != "removed" {
		s.state.Retired[old.Release] = old
	}
	s.state.Sites[r.Name] = next
	for _, active := range s.inflight {
		if active.name == r.Name {
			active.cancel()
		}
	}
	s.state.Operations[r.RequestID] = operation{Digest: wireDigest, Result: next.Site}
	if err = s.save(); err != nil {
		if exists {
			s.state.Sites[r.Name] = old
		} else {
			delete(s.state.Sites, r.Name)
		}
		delete(s.state.Operations, r.RequestID)
		s.state.Pending[release] = next
		delete(s.state.Retired, old.Release)
		return Site{}, err
	}
	// Old resources remain charged and retryable until scoped cleanup succeeds.
	if exists && old.Release != "" {
		if s.retireLocked(ctx, old) == nil {
			delete(s.state.Retired, old.Release)
			_ = s.save()
		}
	}
	result := next.Site
	if r.Access != "private" {
		result.URL = result.Origin
		if raw != "" {
			result.URL += "/#" + raw
		}
	}
	success = true
	return result, nil
}

// appBinary enforces the go-http launch contract before any snapshot work: the
// source root must hold a static Linux ELF at the fixed name `app`. Anything
// else is a caller error, never a retryable failure.
func (s *Service) appBinary(source string) error {
	f, err := s.workspace.Open(path.Join(source, "app"))
	if err != nil {
		return ErrInvalid
	}
	defer f.Close()
	var magic [4]byte
	if _, err = io.ReadFull(f, magic[:]); err != nil || magic != [4]byte{0x7f, 'E', 'L', 'F'} {
		return ErrInvalid
	}
	return nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (s *Service) Unpublish(ctx context.Context, b Binding, r UnpublishRequest) (resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorized(b); err != nil {
		return err
	}
	if _, ok, err := s.replay(r.RequestID, r); ok || err != nil {
		return err
	}
	old, ok := s.state.Sites[r.Name]
	if !ok {
		return ErrNotFound
	}
	if old.Revision != r.ExpectedRevision {
		return ErrConflict
	}
	if old.State == "removed" {
		return s.cleanupLocked(ctx, old)
	}
	if len(s.state.Operations) >= 1024 {
		return ErrQuota
	}
	auditDone, err := s.beginAudit("unpublish", r.Name, old.Revision)
	if err != nil {
		return err
	}
	defer func() {
		outcome := "complete"
		if resultErr != nil {
			outcome = "failed"
		}
		if auditDone(outcome, 0) != nil {
			resultErr = ErrUnavailable
		}
	}()
	next := old
	next.State = "removed"
	next.TokenHash = ""
	s.state.Sites[r.Name] = next
	for _, active := range s.inflight {
		if active.name == r.Name {
			active.cancel()
		}
	}
	s.state.Operations[r.RequestID] = operation{Digest: digest(r), Result: next.Site}
	if err := s.save(); err != nil {
		s.state.Sites[r.Name] = old
		delete(s.state.Operations, r.RequestID)
		return err
	}
	return s.cleanupLocked(ctx, next)
}
func (s *Service) cleanupLocked(ctx context.Context, r record) error {
	if r.Mode == "dynamic" {
		if s.executor == nil || s.executor.Remove(ctx, s.workload(r)) != nil {
			return ErrUnavailable
		}
	}
	if s.routes != nil {
		if err := s.routes.Remove(ctx, r.Name); err != nil {
			return ErrUnavailable
		}
	}
	if err := s.root.RemoveAll(r.Release); err != nil {
		return ErrUnavailable
	}
	return nil
}
func (s *Service) recoverLocked(ctx context.Context) error {
	for key, op := range s.state.Operations {
		if op.Result.State == "pending" {
			op.Result.State = "failed"
			s.state.Operations[key] = op
		}
	}
	if err := s.expireAudit(); err != nil {
		return err
	}
	for key, r := range s.state.Pending {
		if s.retireLocked(ctx, r) == nil {
			delete(s.state.Pending, key)
		}
	}
	for key, r := range s.state.Retired {
		if s.retireLocked(ctx, r) == nil {
			delete(s.state.Retired, key)
		}
	}
	if err := s.save(); err != nil {
		return err
	}

	for name, r := range s.state.Sites {
		if r.State != "removed" && (!s.now().Before(r.ExpiresAt) || !s.cfg.validName(name) || r.Mode == "dynamic" && r.AdmissionDigest != s.cfg.AdmissionDigest) {
			finish, err := s.beginAudit("expire", name, r.Revision)
			if err != nil {
				return err
			}
			old := r
			r.State = "removed"
			r.TokenHash = ""
			s.state.Sites[name] = r
			if err := s.save(); err != nil {
				s.state.Sites[name] = old
				_ = finish("failed", 0)
				return err
			}
			for _, active := range s.inflight {
				if active.name == name {
					active.cancel()
				}
			}
			if err := finish("complete", 0); err != nil {
				return err
			}
		}
	}

	entries, err := fs.ReadDir(s.root.FS(), ".")
	if err != nil {
		return ErrUnavailable
	}
	keep := map[string]bool{}
	for key := range s.state.Pending {
		keep[key] = true
	}
	for key := range s.state.Retired {
		keep[key] = true
	}
	for _, r := range s.state.Sites {
		keep[r.Release] = true
	}
	for _, e := range entries {
		if e.IsDir() && !keep[e.Name()] {
			if !validRelease(e.Name()) {
				return ErrUnavailable
			}
			if err := s.root.RemoveAll(e.Name()); err != nil {
				return ErrUnavailable
			}
		}
	}
	for name, r := range s.state.Sites {
		if r.State == "ready" && r.Mode == "dynamic" {
			if s.executor == nil {
				return ErrUnavailable
			}
			if err := s.executor.Check(ctx, s.workload(r)); err != nil && !s.now().Before(r.NextRestart) {
				if s.cfg.Restart == "never" || r.Restarts >= 3 {
					r.State = "failed"
					r.TokenHash = ""
				} else {
					r.Restarts++
					r.NextRestart = s.now().Add(time.Duration(1<<r.Restarts) * time.Second)
				}
				s.state.Sites[name] = r
				if err := s.save(); err != nil {
					return err
				}
				if r.State == "ready" {
					finish, err := s.beginAudit("restart", name, r.Revision)
					if err != nil {
						return err
					}
					outcome := "complete"
					if s.executor.Ensure(ctx, s.workload(r)) != nil {
						outcome = "failed"
					}
					if err = finish(outcome, 0); err != nil {
						return err
					}
				}
			}
		}
		if r.State == "removed" || r.State == "failed" {
			_ = s.cleanupLocked(ctx, r)
		} else if r.Access == "private" && s.routes != nil {
			_ = s.routes.Remove(ctx, r.Name)
		} else if r.Access != "private" && s.routes != nil {
			_ = s.routes.Ensure(ctx, r.Name)
		}
	}
	return nil
}
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.storageFault || s.auditFault {
		return ErrUnavailable
	}
	return s.recoverLocked(ctx)
}
func (s *Service) List() []Site {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Site{}
	for _, r := range s.state.Sites {
		if r.State == "ready" && s.now().Before(r.ExpiresAt) {
			out = append(out, r.Site)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (s *Service) Status(name string) (Site, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.state.Sites[name]
	if !ok || r.State == "removed" || !s.now().Before(r.ExpiresAt) {
		return Site{}, ErrNotFound
	}
	return r.Site, nil
}

func (s *Service) retireLocked(ctx context.Context, r record) error {
	if r.Mode == "dynamic" {
		if s.executor == nil || s.executor.Remove(ctx, s.workload(r)) != nil {
			return ErrUnavailable
		}
	}
	if s.root.RemoveAll(r.Release) != nil {
		return ErrUnavailable
	}
	return nil
}
