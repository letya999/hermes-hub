package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/locale"
)

const maxMCPBodyBytes = 4 << 20

// Hermes stamps each tool call with the run ID held in its per-turn context.
// The runtime resolves that ID to its admitted job before ToolHub verifies it.
func (s *runtimeHTTP) proxyMCP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	s.proxyOnce.Do(func() {
		endpoint, err := url.Parse(os.Getenv("HUB_TOOLHUB_ENDPOINT"))
		if err != nil || endpoint.Scheme != "http" || endpoint.Host == "" || endpoint.Path != "/mcp" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
			s.proxyErr = api.ErrInvalid
			return
		}
		s.proxy, s.proxyErr = toolHubRelayHandler(endpoint.Scheme + "://" + endpoint.Host)
	})
	if s.proxyErr != nil {
		writeRuntimeError(w, http.StatusServiceUnavailable, "ToolHub route unavailable")
		return
	}
	r = r.Clone(r.Context())
	r.URL.Path = "/mcp-run"
	r.Header.Del(api.HeaderJobID)
	r.Header.Del(api.HeaderRunID)
	r.Header.Del(contract.RunTokenHeader)
	r.Header.Del(contract.ModelServiceTokenHeader)
	r.Header.Del(contract.MemoryRunTokenHeader)
	r.Header.Del(contract.CommunicationRunTokenHeader)
	r.Header.Del(contract.EgressRunTokenHeader)
	r.Header.Del(RunDeliveryHeader)
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBodyBytes))
		if err != nil {
			writeRuntimeError(w, http.StatusRequestEntityTooLarge, "MCP request too large")
			return
		}
		var call struct {
			Method string `json:"method"`
			Params struct {
				Meta struct {
					RunID string `json:"x-hermes-hub-run-id"`
				} `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &call) != nil {
			writeRuntimeError(w, http.StatusBadRequest, "invalid MCP request")
			return
		}
		if call.Method == "tools/call" {
			if !validHermesRunID(call.Params.Meta.RunID) {
				writeRuntimeError(w, http.StatusUnauthorized, "run identity missing")
				return
			}
			jobID, err := s.jobForRun(r.Context(), call.Params.Meta.RunID)
			if err != nil {
				writeRuntimeError(w, http.StatusUnauthorized, "run identity unavailable")
				return
			}
			token, err := s.runTokenFor(r.Context(), call.Params.Meta.RunID, contract.ToolHubAudience)
			if err != nil {
				lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
				writeRuntimeError(w, http.StatusUnauthorized, locale.T(lang, "run.authorization-denied"))
				return
			}
			if token != "" {
				r.Header.Set(contract.RunTokenHeader, token)
				for header, audience := range map[string]string{
					contract.MemoryRunTokenHeader:        contract.MemoryAudience,
					contract.CommunicationRunTokenHeader: contract.CommunicationAudience,
					contract.EgressRunTokenHeader:        contract.EgressAudience,
				} {
					delegated, err := s.runTokenFor(r.Context(), call.Params.Meta.RunID, audience)
					if err == nil && delegated != "" {
						r.Header.Set(header, delegated)
					}
				}
			}
			r.Header.Set(api.HeaderJobID, jobID)
			r.Header.Set(api.HeaderRunID, call.Params.Meta.RunID)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	s.metrics().mcpDelta(1)
	defer s.metrics().mcpDelta(-1)
	s.proxy.ServeHTTP(w, r)
}

func (s *runtimeHTTP) bindRun(jobID, runID string) error {
	return s.bindAuthorizedRun(jobID, runID, "", "")
}

func (s *runtimeHTTP) bindAuthorizedRun(jobID, runID, proof, session string) error {
	if jobID == "" || !validHermesRunID(runID) {
		return errors.New("invalid run binding")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.runJobs == nil {
		s.runJobs = map[string]string{}
		s.jobRuns = map[string]string{}
	}
	if existing := s.runJobs[runID]; existing != "" && existing != jobID {
		return errors.New("hermes run ownership changed during readmission")
	}
	if existing := s.jobRuns[jobID]; existing != "" && existing != runID {
		return errors.New("hermes run changed during readmission")
	}
	access := s.runAccess[runID]
	if session != "" {
		if access.session != "" && access.session != session {
			return errors.New("run session changed during readmission")
		}
		access.session = session
		// A held admission slot hands itself to the bound run here, inside
		// the same critical section — releaseRun frees it at terminal.
		if s.sessionRuns != nil {
			if _, held := s.sessionRuns[session]; held {
				s.sessionRuns[session] = runID
			}
		}
	}
	if proof != "" {
		if access.proof != "" && access.proof != proof {
			return errors.New("run delivery changed during readmission")
		}
		access.proof = proof
	}
	if s.runAccess == nil {
		s.runAccess = make(map[string]runAccess)
	}
	s.runAccess[runID] = access
	s.runJobs[runID], s.jobRuns[jobID] = jobID, runID
	s.signalRunChangeLocked()
	return nil
}

// resolveBoundRun admits /v1/observe and /v1/control only when the presented
// (job, run, session) triple is a live row of this runtime's own table —
// caller material can name an admission, never create one. A proof the same
// row has not recorded yet upgrades in place; a conflicting one refuses.
func (s *runtimeHTTP) resolveBoundRun(ctx context.Context, jobID, runID, session, proof string) error {
	if jobID == "" || !validHermesRunID(runID) {
		return errors.New("invalid run reference")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		s.runMu.Lock()
		access := s.runAccess[runID]
		switch {
		case s.runJobs[runID] != jobID || s.jobRuns[jobID] != runID || access.session != session:
			if s.pending == 0 {
				s.runMu.Unlock()
				return errors.New("run is not bound to this runtime")
			}
		default:
			if proof != "" {
				if access.proof != "" && access.proof != proof {
					s.runMu.Unlock()
					return errors.New("run delivery changed during readmission")
				}
				access.proof = proof
				s.runAccess[runID] = access
			}
			s.runMu.Unlock()
			return nil
		}
		if s.runChange == nil {
			s.runChange = make(chan struct{})
		}
		changed := s.runChange
		s.runMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("run is not bound to this runtime")
		case <-changed:
		}
	}
}

// acquireSessionSlot enforces the runtime's session bound: one active run
// per session (per-session FIFO) and at most sessionSlots distinct sessions
// runtime-wide. The caller owns the slot until it runs release, or hands it
// to a bound run — releaseRun frees it when that run goes terminal.
func (s *runtimeHTTP) acquireSessionSlot(ctx context.Context, session string) (func(), error) {
	for {
		s.runMu.Lock()
		if s.sessionRuns == nil {
			s.sessionRuns = map[string]string{}
		}
		if _, held := s.sessionRuns[session]; !held && len(s.sessionRuns) < sessionSlots() {
			s.sessionRuns[session] = ""
			s.runMu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					s.runMu.Lock()
					delete(s.sessionRuns, session)
					s.signalRunChangeLocked()
					s.runMu.Unlock()
				})
			}, nil
		}
		if s.runChange == nil {
			s.runChange = make(chan struct{})
		}
		changed := s.runChange
		s.runMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// sessionSlots bounds concurrent sessions in this runtime (issue #602 S2):
// HUB_SESSION_SLOTS, default 5.
func sessionSlots() int {
	if value := strings.TrimSpace(os.Getenv("HUB_SESSION_SLOTS")); value != "" {
		if n, err := strconv.Atoi(value); err == nil && n >= 1 && n <= 64 {
			return n
		}
	}
	return 5
}

func (s *runtimeHTTP) beginAdmission() {
	s.runMu.Lock()
	s.pending++
	s.runMu.Unlock()
}

func (s *runtimeHTTP) finishAdmission() {
	s.runMu.Lock()
	s.pending--
	s.signalRunChangeLocked()
	s.runMu.Unlock()
}

func (s *runtimeHTTP) jobForRun(ctx context.Context, runID string) (string, error) {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		s.runMu.Lock()
		jobID := s.runJobs[runID]
		if jobID != "" {
			s.runMu.Unlock()
			return jobID, nil
		}
		if s.pending == 0 {
			s.runMu.Unlock()
			return "", errors.New("run admission unavailable")
		}
		if s.runChange == nil {
			s.runChange = make(chan struct{})
		}
		changed := s.runChange
		s.runMu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errors.New("run admission unavailable")
		case <-changed:
		}
	}
}

func (s *runtimeHTTP) releaseRun(jobID, runID, status string) {
	switch status {
	case "completed", "failed", "cancelled", "interrupted":
	default:
		return
	}
	s.runMu.Lock()
	if s.runJobs[runID] == jobID && s.jobRuns[jobID] == runID {
		if session := s.runAccess[runID].session; session != "" && s.sessionRuns[session] == runID {
			delete(s.sessionRuns, session)
		}
		delete(s.runJobs, runID)
		delete(s.jobRuns, jobID)
		delete(s.runAccess, runID)
		s.signalRunChangeLocked()
	}
	s.runMu.Unlock()
}

func (s *runtimeHTTP) signalRunChangeLocked() {
	if s.runChange != nil {
		close(s.runChange)
		s.runChange = nil
	}
}
