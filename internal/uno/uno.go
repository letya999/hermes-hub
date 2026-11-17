// Package uno is a typed client for the Uno-compatible Firecracker
// control-plane API (mini-Uno and external Uno deployments share it).
package uno

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Box is a Firecracker microVM managed by the control plane.
type Box struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Status    string            `json:"status"` // provisioning, running, failed, deleted
	Address   string            `json:"address,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Error     string            `json:"error,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// LaunchRequest asks the control plane to boot a box.
type LaunchRequest struct {
	Name     string            `json:"name"`
	Image    string            `json:"image"`
	RAMMB    int               `json:"ram_mb"`
	VCPU     int               `json:"vcpu"`
	Shares   []Share           `json:"shares,omitempty"`
	Forwards []Forward         `json:"forwards,omitempty"`
	Proxies  []Proxy           `json:"proxies,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

// Share maps a host directory into the guest via virtio-fs.
type Share struct {
	Tag      string `json:"tag"`
	HostPath string `json:"host_path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// Forward exposes a guest TCP port on a host port. Bind selects the host
// listen address (default 127.0.0.1); HostPort 0 allocates an ephemeral port.
type Forward struct {
	GuestPort int    `json:"guest_port"`
	HostPort  int    `json:"host_port,omitempty"`
	Bind      string `json:"bind,omitempty"`
}

// Proxy binds a guest loopback TCP port that forwards to an allowlisted
// host endpoint through vsock.
type Proxy struct {
	GuestPort int    `json:"guest_port"`
	Target    string `json:"target"` // host:port on the control-plane host
}

// LaunchResponse carries the created box and its host forward address.
type LaunchResponse struct {
	Box Box `json:"box"`
}

// RunRequest executes a command inside the box.
type RunRequest struct {
	Cmd            string            `json:"cmd"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_s,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	WaitSeconds    int               `json:"wait_s,omitempty"`
}

// Run is a recorded command execution inside a box.
type Run struct {
	ID             string `json:"id"`
	Status         string `json:"status"` // running, succeeded, failed
	ExitCode       int    `json:"exit_code"`
	Stdout         string `json:"stdout,omitempty"`
	Stderr         string `json:"stderr,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Error classifies control-plane failures.
type Error struct {
	Op        string
	Status    int
	Message   string
	Ambiguous bool // submit may have landed; safe to replay by idempotency key
}

func (e *Error) Error() string { return fmt.Sprintf("uno %s: %s (status %d)", e.Op, e.Message, e.Status) }

// Retryable reports whether the caller may retry after a short delay.
func (e *Error) Retryable() bool { return e.Ambiguous || e.Status == 0 || e.Status >= 500 || e.Status == 429 }

var ErrNotFound = errors.New("uno: not found")

// Client talks to the control plane over HTTP.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient returns a client for base URL (e.g. http://127.0.0.1:8320).
func NewClient(base, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: hc}
}

func (c *Client) call(ctx context.Context, op, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("uno %s: encode: %w", op, err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return fmt.Errorf("uno %s: %w", op, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ne net.Error
		return &Error{Op: op, Message: err.Error(), Ambiguous: errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return &Error{Op: op, Message: err.Error(), Ambiguous: true}
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		return &Error{Op: op, Status: resp.StatusCode, Message: strings.TrimSpace(string(data)), Ambiguous: resp.StatusCode >= 500}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("uno %s: decode: %w", op, err)
		}
	}
	return nil
}

// ListBoxes returns all boxes.
func (c *Client) ListBoxes(ctx context.Context) ([]Box, error) {
	var out struct {
		Boxes []Box `json:"boxes"`
	}
	if err := c.call(ctx, "list", http.MethodGet, "/v1/boxes", nil, &out); err != nil {
		return nil, err
	}
	return out.Boxes, nil
}

// GetBox returns one box by id.
func (c *Client) GetBox(ctx context.Context, id string) (Box, error) {
	var out struct {
		Box Box `json:"box"`
	}
	if err := c.call(ctx, "get", http.MethodGet, "/v1/boxes/"+id, nil, &out); err != nil {
		return Box{}, err
	}
	return out.Box, nil
}

// LaunchBox creates and starts a box.
func (c *Client) LaunchBox(ctx context.Context, req LaunchRequest) (Box, error) {
	var out LaunchResponse
	if err := c.call(ctx, "launch", http.MethodPost, "/v1/boxes", req, &out); err != nil {
		return Box{}, err
	}
	return out.Box, nil
}

// DeleteBox stops and removes the box. NotFound is treated as success.
func (c *Client) DeleteBox(ctx context.Context, id string) error {
	err := c.call(ctx, "delete", http.MethodDelete, "/v1/boxes/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Run submits a command for execution; idempotent by IdempotencyKey.
func (c *Client) Run(ctx context.Context, boxID string, req RunRequest) (Run, error) {
	var out struct {
		Run Run `json:"run"`
	}
	err := c.call(ctx, "run", http.MethodPost, "/v1/boxes/"+boxID+"/runs", req, &out)
	if err != nil {
		return Run{}, err
	}
	return out.Run, nil
}

// GetRun polls a run; wait_s>0 long-polls for completion.
func (c *Client) GetRun(ctx context.Context, boxID, runID string, waitS int) (Run, error) {
	var out struct {
		Run Run `json:"run"`
	}
	path := fmt.Sprintf("/v1/boxes/%s/runs/%s?wait_s=%d", boxID, runID, waitS)
	if err := c.call(ctx, "run-status", http.MethodGet, path, nil, &out); err != nil {
		return Run{}, err
	}
	return out.Run, nil
}

// FindRunByKey returns the run recorded for an idempotency key, if any.
func (c *Client) FindRunByKey(ctx context.Context, boxID, key string) (Run, bool, error) {
	var out struct {
		Runs []Run `json:"runs"`
	}
	if err := c.call(ctx, "runs", http.MethodGet, "/v1/boxes/"+boxID+"/runs", nil, &out); err != nil {
		return Run{}, false, err
	}
	for _, r := range out.Runs {
		if r.IdempotencyKey == key {
			return r, true, nil
		}
	}
	return Run{}, false, nil
}
