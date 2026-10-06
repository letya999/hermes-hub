package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

// exec-scratch is the private workload entrypoint for sandboxed tool calls.
// It runs inside a disposable container: extracts the authorized input tar
// into private scratch, runs the bounded script with a sanitized
// environment, then emits one JSON result carrying the bounded output and a
// tar of whatever the script left under the export directory. The container
// owns the network, mount and credential isolation; this process owns the
// byte budgets.
func runExecScratch(ctx context.Context) error {
	var request toolhub.ScratchExecRequest
	if err := json.NewDecoder(io.LimitReader(os.Stdin, toolsExecMaxRequest)).Decode(&request); err != nil {
		return fmt.Errorf("exec-scratch request: %w", err)
	}
	scratch := os.Getenv("HUB_SCRATCH_DIR")
	if scratch == "" {
		scratch = "/scratch"
	}
	outputs := os.Getenv("HUB_SCRATCH_OUTPUTS")
	if outputs == "" {
		outputs = "/outputs"
	}
	inputs := filepath.Join(scratch, "inputs")
	for _, dir := range []string{scratch, outputs, inputs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("exec-scratch dirs: %w", err)
		}
	}
	result := toolhub.ScratchExecResult{Stopped: true}
	emit := func() error {
		result.ExportTar = packOutputs(outputs)
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if request.Command == "" || len(request.Command) > 8192 {
		result.Stderr = "invalid command"
		return emit()
	}
	if err := unpackInputs(request.InputsTar, inputs); err != nil {
		result.Stderr = "input unpack: " + err.Error()
		return emit()
	}
	timeout := request.TimeoutSeconds
	if timeout <= 0 || timeout > 600 {
		timeout = 300
	}
	lease, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(lease, "sh", "-c", request.Command)
	// The lease kills the whole process group, not only the direct sh; the
	// post-run check confirms no children survived before we report stopped.
	cmd.SysProcAttr = scratchProcAttr()
	cmd.Cancel = func() error { return scratchCancel(cmd) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = scratch
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + scratch, "TMPDIR=/tmp",
		"INPUTS=" + inputs, "OUTPUTS=" + outputs, "USER=sandbox",
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &boundedWriter{w: &stdout, max: 256 << 10}
	cmd.Stderr = &boundedWriter{w: &stderr, max: 256 << 10}
	runErr := cmd.Run()
	result.TreeStopped = cmd.Process == nil || scratchTreeGone(cmd.Process.Pid)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.ExitCode = 0
	if runErr != nil {
		result.ExitCode = 1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.Stderr += "\n" + runErr.Error()
		}
	}
	return emit()
}

// boundedWriter caps captured output; excess bytes are dropped so a hostile
// script cannot flood the control channel.
type boundedWriter struct {
	w   io.Writer
	max int
	n   int
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if remain := b.max - b.n; remain > 0 {
		n, err := b.w.Write(p[:min(len(p), remain)])
		b.n += n
		return n, err
	}
	return len(p), nil
}

func unpackInputs(b64, dir string) error {
	if b64 == "" {
		return nil
	}
	body, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(body) > 8<<20 {
		return errors.New("input tar invalid")
	}
	reader := tar.NewReader(bytes.NewReader(body))
	seen, total := 0, int64(0)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if header.Typeflag != tar.TypeReg || header.Size > scratchPackFileMax ||
			strings.HasPrefix(name, "..") || path.IsAbs(name) {
			continue
		}
		seen++
		total += header.Size
		if seen > scratchPackMaxFiles || total > scratchPackMaxTotal {
			return errors.New("input budget exceeded")
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(filepath.Separator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.CopyN(out, reader, header.Size)
		out.Close()
		if err != nil {
			return err
		}
	}
}

// packOutputs tars regular files under dir. A nil-safe best effort: an
// unreadable export simply contributes nothing rather than failing the run.
func packOutputs(dir string) string {
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	_ = filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || p == dir || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 2<<20 {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if err := writer.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	})
	if err := writer.Close(); err != nil || buf.Len() == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}
