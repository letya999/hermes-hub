package sshcap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/pkg/sftp"
)

type ReadResult struct {
	Alias     string `json:"alias"`
	Path      string `json:"path"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Bytes     int    `json:"bytes"`
}

// Read fetches one allowlisted remote path over SFTP with a byte cap. Binary
// files are rejected like the workspace file tools.
func (s *Service) Read(ctx context.Context, alias, remotePath string) (*ReadResult, error) {
	if err := s.alive(); err != nil {
		return nil, err
	}
	h, err := s.host(alias)
	if err != nil {
		return nil, err
	}
	if err = s.pathPolicy(h, alias, "read", h.Paths, remotePath); err != nil {
		return nil, err
	}
	release, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	client, err := s.dial(ctx, alias, h)
	if err != nil {
		s.auditEvent("ssh-file", alias, "error", "dial failed")
		return nil, err
	}
	defer client.Close()
	fs, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("%w: sftp: %v", ErrUnavailable, err)
	}
	defer fs.Close()
	f, err := fs.Open(remotePath)
	if err != nil {
		s.auditEvent("ssh-file", alias, "error", "open failed")
		return nil, fmt.Errorf("%w: open: %v", ErrUnavailable, err)
	}
	defer f.Close()
	body, truncated, err := boundedRead(f, h.readLimit())
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrUnavailable, err)
	}
	if int64(len(body)) > h.readLimit() {
		body = body[:h.readLimit()]
	}
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("%w: remote file is not UTF-8 text", ErrInvalid)
	}
	s.auditEvent("ssh-file", alias, "ok", fmt.Sprintf("read %s bytes=%d", remotePath, len(body)))
	return &ReadResult{Alias: alias, Path: remotePath, Text: string(body), Truncated: truncated, Bytes: len(body)}, nil
}

type WriteResult struct {
	Alias string `json:"alias"`
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// Write atomically replaces one allowlisted remote path (temp + rename). The
// ssh_write grant is required and the payload is bounded UTF-8 text.
func (s *Service) Write(ctx context.Context, alias, remotePath, text string) (*WriteResult, error) {
	if err := s.alive(); err != nil {
		return nil, err
	}
	if !s.grants.Write {
		return nil, fmt.Errorf("%w: file write requires the ssh_write grant", ErrDenied)
	}
	h, err := s.host(alias)
	if err != nil {
		return nil, err
	}
	if err = s.pathPolicy(h, alias, "write", h.WritePaths, remotePath); err != nil {
		return nil, err
	}
	if len(text) > writeLimit || !utf8.ValidString(text) {
		return nil, fmt.Errorf("%w: text must be UTF-8 and <=2 MiB", ErrInvalid)
	}
	release, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	client, err := s.dial(ctx, alias, h)
	if err != nil {
		s.auditEvent("ssh-file", alias, "error", "dial failed")
		return nil, err
	}
	defer client.Close()
	fs, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("%w: sftp: %v", ErrUnavailable, err)
	}
	defer fs.Close()
	raw := make([]byte, 8)
	if _, err = rand.Read(raw); err != nil {
		return nil, fmt.Errorf("%w: temp name", ErrUnavailable)
	}
	tmp := remotePath + ".hub-ssh-" + hex.EncodeToString(raw)
	f, err := fs.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		s.auditEvent("ssh-file", alias, "error", "temp create failed")
		return nil, fmt.Errorf("%w: temp create: %v", ErrUnavailable, err)
	}
	_, writeErr := f.Write([]byte(text))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = fs.Remove(tmp)
		return nil, fmt.Errorf("%w: temp write failed", ErrUnavailable)
	}
	if err = fs.Rename(tmp, remotePath); err != nil {
		_ = fs.Remove(tmp)
		s.auditEvent("ssh-file", alias, "error", "rename failed")
		return nil, fmt.Errorf("%w: rename: %v", ErrUnavailable, err)
	}
	s.auditEvent("ssh-file", alias, "ok", fmt.Sprintf("write %s bytes=%d", remotePath, len(text)))
	return &WriteResult{Alias: alias, Path: remotePath, Bytes: len(text)}, nil
}
