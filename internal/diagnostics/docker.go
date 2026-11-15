package diagnostics

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
)

var errDiagnosticBatch = errors.New("diagnostic batch complete")
var errDiagnosticLine = errors.New("diagnostic source line exceeds bound")

// Docker is for bounded metadata commands. Log collection uses dockerLines.
func Docker(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var out bytes.Buffer
	writer := &diagnosticCommandWriter{write: func(p []byte) error {
		if out.Len()+len(p) > maxDiagnosticBatch {
			return errDiagnosticBatch
		}
		_, err := out.Write(p)
		return err
	}, cancel: cancel}
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- caller supplies fixed Docker operations and owned IDs.
	cmd.Stdout = writer
	cmd.Stderr = writer
	err := cmd.Run()
	if writer.err != nil {
		return out.Bytes(), writer.err
	}
	return out.Bytes(), err
}

type diagnosticCommandWriter struct {
	write  func([]byte) error
	cancel context.CancelFunc
	err    error
}

func (w *diagnosticCommandWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if err := w.write(p); err != nil {
		w.err = err
		w.cancel()
		return 0, err
	}
	return len(p), nil
}

// streamDiagnosticLines bounds memory and total input, retaining complete lines.
func streamDiagnosticLines(r io.Reader, consume func(string) error) error {
	var pending []byte
	buf := make([]byte, 32<<10)
	read := 0
	for {
		n, err := r.Read(buf)
		read += n
		if read > 64<<20 {
			return errDiagnosticBatch
		}
		p := buf[:n]
		for len(p) > 0 {
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				i = len(p)
			}
			if len(pending)+i > maxDiagnosticLine {
				return errDiagnosticLine
			}
			pending = append(pending, p[:i]...)
			if i == len(p) {
				break
			}
			if err := consume(string(pending)); err != nil {
				return err
			}
			pending = pending[:0]
			p = p[i+1:]
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			if len(pending) > 0 {
				return consume(string(pending))
			}
			return nil
		}
	}
}

func dockerLines(ctx context.Context, consume func(string) error, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	cmd := exec.CommandContext(ctx, "docker", args...) // #nosec G204 -- fixed logs operation and authority-scoped container ID.
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return err
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); _ = writer.CloseWithError(err); done <- err }()
	streamErr := streamDiagnosticLines(reader, consume)
	_ = reader.Close()
	cancel()
	commandErr := <-done
	if streamErr != nil {
		return streamErr
	}
	return commandErr
}
