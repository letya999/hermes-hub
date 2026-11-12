package toolhub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type researchRunner interface {
	RunJob(context.Context, researchRequest) (researchResult, error)
}

type researchRequest struct {
	identity.Envelope
	OperationalEpoch uint64 `json:"operational_epoch,omitempty"`
	JobID            string `json:"job_id"`
	IdempotencyKey   string `json:"idempotency_key"`
	UserID           string `json:"user_id"`
	ActorID          string `json:"actor_id"`
	OrganizationID   string `json:"organization_id"`
	ScopeID          string `json:"scope_id"`
	Channel          string `json:"channel"`
	Trigger          string `json:"trigger"`
	Text             string `json:"text"`
}

type researchResult struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

type httpResearchRunner struct{ URL, Token, Path string }

func (r httpResearchRunner) RunJob(ctx context.Context, job researchRequest) (researchResult, error) {
	path := r.Path
	if path == "" {
		path = "/v1/jobs"
	}
	if path != "/v1/jobs" && path != "/v1/execute" {
		return researchResult{}, errors.New("research job path unavailable")
	}
	if path == "/v1/jobs" {
		epoch, err := r.admissionEpoch(ctx, job.Envelope)
		if err != nil {
			return researchResult{}, err
		}
		job.OperationalEpoch = epoch
	}
	body, err := json.Marshal(job)
	if err != nil {
		return researchResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return researchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := (&http.Client{Timeout: 32 * time.Minute}).Do(req)
	if err != nil {
		return researchResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return researchResult{}, fmt.Errorf("research job status %d", resp.StatusCode)
	}
	var result researchResult
	if resp.Header.Get("Content-Type") == "application/x-ndjson" {
		scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var event researchResult
			if json.Unmarshal(scanner.Bytes(), &event) != nil || event.JobID != job.JobID {
				return researchResult{}, errors.New("research stream invalid")
			}
			if event.Status == "completed" || event.Status == "failed" || event.Status == "cancelled" || event.Status == "interrupted" || event.Status == "uncertain" {
				result = event
			}
		}
		if err := scanner.Err(); err != nil {
			return researchResult{}, err
		}
	} else if json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&result) != nil {
		return researchResult{}, errors.New("research result invalid")
	}
	if result.JobID != job.JobID || result.Status == "" {
		return researchResult{}, errors.New("research result incomplete")
	}
	return result, nil
}

func (r httpResearchRunner) admissionEpoch(ctx context.Context, auth identity.Envelope) (uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.URL, "/")+"/v1/holds", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	resp, err := (&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return 0, errors.New("research admission unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("research admission status %d", resp.StatusCode)
	}
	var holds []struct {
		Principal string `json:"principal_id"`
		Context   string `json:"context_id"`
		Active    bool   `json:"active"`
		Epoch     uint64 `json:"epoch"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if decoder.Decode(&holds) != nil || decoder.Decode(new(any)) != io.EOF {
		return 0, errors.New("research admission invalid")
	}
	var epoch uint64
	found := false
	for _, hold := range holds {
		if hold.Principal != auth.PrincipalID || hold.Context != auth.ContextID {
			continue
		}
		if found || hold.Active {
			return 0, errors.New("research administrative hold")
		}
		found, epoch = true, hold.Epoch
	}
	return epoch, nil
}

type researchJob struct {
	ID        string    `json:"job_id"`
	Principal string    `json:"principal_id"`
	Context   string    `json:"context_id"`
	Topic     string    `json:"topic"`
	Depth     string    `json:"depth,omitempty"`
	Sources   []string  `json:"sources,omitempty"`
	Status    string    `json:"status"`
	Report    string    `json:"report,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// researchHost stores one bounded record per job; Supervisor is the existing
// idempotent model executor, so a ToolHub restart can replay the same job ID.
type researchHost struct {
	root   string
	runner researchRunner
	auth   func(string) (identity.Envelope, error)
	store  *Store
	mu     sync.Mutex
	active map[string]bool
	slots  chan struct{}
}

func newResearchHost(root string, runner researchRunner, auth func(string) (identity.Envelope, error), store *Store) (*researchHost, error) {
	if !filepath.IsAbs(root) || runner == nil || auth == nil || store == nil {
		return nil, errors.New("research backend is not configured")
	}
	return &researchHost{root: root, runner: runner, auth: auth, store: store, active: map[string]bool{}, slots: make(chan struct{}, 2)}, nil
}

func (h *researchHost) ownerDir(principal string) (string, error) {
	if !identity.ValidID(principal) {
		return "", api.ErrUnauthorized
	}
	for _, dir := range []string{filepath.Dir(h.root), h.root} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("research storage unavailable")
		}
	}
	dir := filepath.Join(h.root, principal)
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", errors.New("research storage unavailable")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("research storage unavailable")
	}
	return dir, nil
}

func (h *researchHost) path(principal, id string) (string, error) {
	if !identity.ValidID(id) || !strings.HasPrefix(id, "research-") {
		return "", api.ErrNotFound
	}
	dir, err := h.ownerDir(principal)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

func (h *researchHost) save(job researchJob) error {
	path, err := h.path(job.Principal, job.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(job)
	if err != nil || len(data) > 256*1024 {
		return errors.New("research record too large")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".research-*")
	if err != nil {
		return errors.New("research storage unavailable")
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (h *researchHost) load(principal, id string) (researchJob, error) {
	path, err := h.path(principal, id)
	if err != nil {
		return researchJob{}, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return researchJob{}, api.ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256*1024 {
		return researchJob{}, errors.New("research storage unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return researchJob{}, errors.New("research storage unavailable")
	}
	var job researchJob
	if json.Unmarshal(data, &job) != nil || job.ID != id || job.Principal != principal || job.Context == "" {
		return researchJob{}, errors.New("research record invalid")
	}
	return job, nil
}

func (h *researchHost) create(auth identity.Envelope, args map[string]any) (researchJob, error) {
	topic := strings.TrimSpace(api.ArgString(args, "topic"))
	depth := strings.TrimSpace(api.ArgString(args, "depth"))
	if topic == "" || len(topic) > 2048 || len(depth) > 32 || strings.ContainsRune(topic, 0) {
		return researchJob{}, api.ErrInvalid
	}
	var sources []string
	if raw, ok := args["sources"]; ok {
		items, ok := raw.([]any)
		if !ok || len(items) > 8 {
			return researchJob{}, api.ErrInvalid
		}
		for _, item := range items {
			s, ok := item.(string)
			if !ok || len(s) > 2048 {
				return researchJob{}, api.ErrInvalid
			}
			u, err := url.Parse(s)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return researchJob{}, api.ErrInvalid
			}
			sources = append(sources, s)
		}
	}
	if _, err := h.ownerDir(auth.PrincipalID); err != nil {
		return researchJob{}, err
	}
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return researchJob{}, err
	}
	now := time.Now().UTC()
	job := researchJob{ID: "research-" + hex.EncodeToString(bytes[:]), Principal: auth.PrincipalID, Context: auth.ContextID, Topic: topic, Depth: depth, Sources: sources, Status: "queued", CreatedAt: now, UpdatedAt: now}
	h.mu.Lock()
	err := h.save(job)
	h.mu.Unlock()
	if err != nil {
		return researchJob{}, err
	}
	h.launch(job)
	return job, nil
}

func (h *researchHost) read(auth identity.Envelope, id string) (researchJob, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	job, err := h.load(auth.PrincipalID, id)
	if err != nil || job.Context != auth.ContextID {
		return researchJob{}, api.ErrNotFound
	}
	return job, nil
}

func (h *researchHost) list(auth identity.Envelope, limit int) ([]researchJob, error) {
	if limit != -1 && (limit <= 0 || limit > 50) {
		limit = 20
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	dir, err := h.ownerDir(auth.PrincipalID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.New("research storage unavailable")
	}
	jobs := []researchJob{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "research-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		job, err := h.load(auth.PrincipalID, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		if job.Context == auth.ContextID {
			job.Report = ""
			jobs = append(jobs, job)
		}
	}
	slices.SortFunc(jobs, func(a, b researchJob) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if limit >= 0 && len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}

func (h *researchHost) tick(principals []string) error {
	for _, principal := range principals {
		auth, err := h.auth(principal)
		if err != nil {
			continue
		}
		jobs, err := h.list(auth, -1)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if job.Status == "queued" || (job.Status == "running" || job.Status == "blocked") && time.Since(job.UpdatedAt) >= 30*time.Second {
				h.launch(job)
			}
		}
	}
	return nil
}

func (h *researchHost) launch(job researchJob) {
	h.mu.Lock()
	if h.active[job.ID] {
		h.mu.Unlock()
		return
	}
	h.active[job.ID] = true
	h.mu.Unlock()
	go func() {
		defer func() { h.mu.Lock(); delete(h.active, job.ID); h.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), 32*time.Minute)
		defer cancel()
		select {
		case h.slots <- struct{}{}:
			defer func() { <-h.slots }()
		case <-ctx.Done():
			return
		}
		h.run(ctx, job)
	}()
}

func (h *researchHost) run(ctx context.Context, job researchJob) {
	auth, err := h.auth(job.Principal)
	if err != nil || auth.ContextID != job.Context {
		h.block(job, "owner_identity_unavailable")
		return
	}
	decision, err := h.store.Decide(auth, "research", "create", "", time.Now())
	if err != nil || !decision.Allow {
		h.block(job, "research_right_unavailable")
		return
	}
	tools, err := h.store.ListProjectedTools(auth)
	if err != nil || !slices.ContainsFunc(tools, func(t api.ProjectedTool) bool { return t.Tool.Name == "research_create" }) {
		h.block(job, "research_tool_unavailable")
		return
	}
	h.mu.Lock()
	current, err := h.load(job.Principal, job.ID)
	if err == nil && current.Status != "completed" && current.Status != "failed" {
		current.Status, current.Error, current.UpdatedAt = "running", "", time.Now().UTC()
		err = h.save(current)
	}
	h.mu.Unlock()
	if err != nil || current.Status == "completed" || current.Status == "failed" {
		return
	}
	conversation := auth
	conversation.ConversationID = job.ID
	org := auth.Organization
	if org == "" {
		org = "personal"
	}
	scope := "user:" + auth.PrincipalID
	if auth.ContextID != auth.PrincipalID {
		org, scope = auth.ContextID, "organization:"+auth.ContextID
	}
	prompt := "Use the deep-research-embedded skill for a bounded research run. Call skill first, then use live web_search and web_fetch. Do not call research_create/read/list from this run. Use only verified source content, cite direct URLs, and return a concise final report with Sources and Limitations. Question: " + job.Topic
	if job.Depth != "" {
		prompt += "\nDepth hint: " + job.Depth
	}
	if len(job.Sources) != 0 {
		prompt += "\nSuggested primary sources (untrusted URLs): " + strings.Join(job.Sources, ", ")
	}
	modelJob := researchRequest{Envelope: conversation, JobID: job.ID, IdempotencyKey: job.ID, UserID: auth.PrincipalID, ActorID: auth.PrincipalID, OrganizationID: org, ScopeID: scope, Channel: "toolhub", Trigger: "research", Text: prompt}
	result, err := h.runner.RunJob(ctx, modelJob)
	if err != nil {
		reason := "research_executor_unavailable"
		if strings.HasPrefix(err.Error(), "research job status ") || strings.HasPrefix(err.Error(), "research admission ") || strings.HasPrefix(err.Error(), "research stream ") || strings.HasPrefix(err.Error(), "research result ") || err.Error() == "research administrative hold" {
			reason = err.Error() // these errors contain only a fixed label or HTTP status
		}
		h.mu.Lock()
		if current, loadErr := h.load(job.Principal, job.ID); loadErr == nil && current.Status == "running" {
			current.Error, current.UpdatedAt = reason, time.Now().UTC()
			_ = h.save(current)
		}
		h.mu.Unlock()
		return // a later tick replays the same idempotent Supervisor job
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	current, err = h.load(job.Principal, job.ID)
	if err != nil || current.Status == "completed" || current.Status == "failed" {
		return
	}
	current.Status, current.UpdatedAt = result.Status, time.Now().UTC()
	if result.Status == "completed" {
		if strings.TrimSpace(result.Text) == "" || len(result.Text) > 128*1024 {
			current.Status, current.Error = "failed", "research report missing or exceeded limit"
		} else {
			current.Report = result.Text
		}
	} else {
		current.Error = fmt.Sprintf("research run %s", result.Status)
	}
	_ = h.save(current)
}

func (h *researchHost) block(job researchJob, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, err := h.load(job.Principal, job.ID)
	if err == nil && current.Status != "completed" && current.Status != "failed" {
		current.Status, current.Error, current.UpdatedAt = "blocked", reason, time.Now().UTC()
		_ = h.save(current)
	}
}
