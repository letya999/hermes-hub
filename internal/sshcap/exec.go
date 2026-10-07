package sshcap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type ExecResult struct {
	Alias     string `json:"alias"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr,omitempty"`
	Truncated bool   `json:"truncated"`
	Write     bool   `json:"write"`
	Millis    int64  `json:"duration_ms"`
}

// Exec runs one allowlisted command over a fresh connection. Cancellation and
// the per-host timeout both terminate the remote process.
func (s *Service) Exec(ctx context.Context, alias, command string) (*ExecResult, error) {
	if err := s.alive(); err != nil {
		return nil, err
	}
	h, err := s.host(alias)
	if err != nil {
		return nil, err
	}
	effective, write, err := s.commandPolicy(h, alias, command)
	if err != nil {
		return nil, err
	}
	release, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, h.timeout())
	defer cancel()
	client, err := s.dial(ctx, alias, h)
	if err != nil {
		s.auditEvent("ssh-exec", alias, "error", "dial failed")
		return nil, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		s.auditEvent("ssh-exec", alias, "error", "session failed")
		return nil, fmt.Errorf("%w: session: %v", ErrUnavailable, err)
	}
	defer session.Close()
	if h.Sudo == "passwordless" && effective != command {
		effective = "sudo -n " + effective
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdout: %v", ErrUnavailable, err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stderr: %v", ErrUnavailable, err)
	}
	if err = session.Start(effective); err != nil {
		s.auditEvent("ssh-exec", alias, "error", "start failed")
		return nil, fmt.Errorf("%w: start: %v", ErrUnavailable, err)
	}
	limit := h.outputLimit()
	var outBuf, errBuf bytes.Buffer
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		_, _ = io.Copy(&outBuf, io.LimitReader(stdout, limit+1))
	}()
	go func() {
		defer readers.Done()
		_, _ = io.Copy(&errBuf, io.LimitReader(stderr, limit+1))
	}()
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		_ = session.Close()
		_ = client.Close()
		<-done
		waitErr = ctx.Err()
	}
	readers.Wait()
	if ctx.Err() != nil && waitErr == nil {
		waitErr = ctx.Err()
	}
	truncated := outBuf.Len() > int(limit) || errBuf.Len() > int(limit)
	result := &ExecResult{Alias: alias, Stdout: outBuf.String(), Stderr: errBuf.String(), Truncated: truncated, Write: write, Millis: time.Since(start).Milliseconds()}
	if int64(len(result.Stdout)) > limit {
		result.Stdout = result.Stdout[:limit]
	}
	if int64(len(result.Stderr)) > limit {
		result.Stderr = result.Stderr[:limit]
	}
	var exitErr *ssh.ExitError
	switch {
	case waitErr == nil:
		result.ExitCode = 0
	case errors.As(waitErr, &exitErr):
		result.ExitCode = exitErr.ExitStatus()
	case errors.Is(waitErr, context.DeadlineExceeded), errors.Is(waitErr, context.Canceled):
		s.auditEvent("ssh-exec", alias, "error", "timeout or cancelled")
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, waitErr)
	default:
		result.ExitCode = -1
	}
	digest := sha256.Sum256([]byte(command))
	s.auditEvent("ssh-exec", alias, "ok", fmt.Sprintf("exit=%d bytes=%d ms=%d sha256:%s", result.ExitCode, outBuf.Len()+errBuf.Len(), result.Millis, hex.EncodeToString(digest[:])[:16]))
	return result, nil
}

// boundedRead drains r up to limit+1 and reports truncation.
func boundedRead(r io.Reader, limit int64) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	return body, int64(len(body)) > limit, nil
}
