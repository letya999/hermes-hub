package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type cappedPDFOutput struct {
	buf bytes.Buffer
	max int
}

func (o *cappedPDFOutput) Write(p []byte) (int, error) {
	if o.buf.Len()+len(p) > o.max {
		return 0, errors.New("pdf output exceeds limit")
	}
	return o.buf.Write(p)
}

func (o *cappedPDFOutput) String() string { return o.buf.String() }

func popplerText(body []byte) (string, error) {
	if len(body) > MaxDocumentBytes {
		return "", fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
	}
	f, err := os.CreateTemp("", "hub-pdf-*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), DocumentTimeout)
	defer cancel()
	info := exec.CommandContext(ctx, "pdfinfo", f.Name())
	var meta cappedPDFOutput
	meta.max = 4096
	info.Stdout, info.Stderr = &meta, io.Discard
	if err = info.Run(); err != nil {
		return "", errors.New("document is invalid")
	}
	pages := 0
	for _, line := range strings.Split(meta.String(), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Pages:"); ok {
			pages, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return "", errors.New("document is invalid")
			}
			break
		}
	}
	if pages < 1 {
		return "", errors.New("document is invalid")
	}
	if pages > MaxPages {
		return "", fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	cmd := exec.CommandContext(ctx, "pdftotext", "-enc", "UTF-8", "-nopgbrk", f.Name(), "-")
	var output cappedPDFOutput
	output.max = MaxDocumentBytes
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err = cmd.Run(); err != nil {
		return "", errors.New("document text extraction failed")
	}
	return strings.TrimSpace(output.String()), nil
}
