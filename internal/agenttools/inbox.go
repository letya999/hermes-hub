package agenttools

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

// inboxFetch materializes one staged inbound file: bytes come from the
// communication hub's spool, the workspace write happens inside this root,
// and only then is the staged copy consumed. A zip is unpacked directly from
// memory — the archive itself never lands on the workspace.
func (t *Tools) inboxFetch(ctx context.Context, ws *os.Root, r Input) (map[string]any, error) {
	if t.CommunicationURL == "" || r.ID == "" || strings.Contains(r.ID, "/") {
		return nil, errors.New("inbox fetch requires a staged file id")
	}
	name, data, err := t.inboundGet(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, errors.New("inbound file is not a readable zip")
		}
		base := strings.TrimSuffix(path.Base(name), path.Ext(name))
		if !safe(base) || strings.Contains(base, "/") {
			return nil, errors.New("unsafe archive name")
		}
		target := "inbox/" + base
		files, err := unpackZip(ws, zr, target)
		if err != nil {
			return nil, err
		}
		result = map[string]any{"path": target, "files": files}
	} else {
		dest := "inbox/" + path.Base(name)
		if !safe(dest) {
			return nil, errors.New("unsafe file name")
		}
		if _, err := ws.Lstat(dest); err == nil {
			dest = "inbox/" + r.ID + "-" + path.Base(name)
		}
		if err := ws.MkdirAll(path.Dir(dest), 0700); err != nil {
			return nil, err
		}
		f, err := ws.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			_ = ws.Remove(dest)
			return nil, err
		}
		if err := f.Close(); err != nil {
			_ = ws.Remove(dest)
			return nil, err
		}
		result = map[string]any{"path": dest, "size": len(data)}
	}
	if err := t.inboundConsume(ctx, r.ID); err != nil {
		return nil, fmt.Errorf("fetched but staged copy not consumed: %w", err)
	}
	return result, nil
}

func (t *Tools) inboundCall(ctx context.Context, method, id string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.CommunicationURL, "/")+"/v1/inbound/"+id, nil)
	if err != nil {
		return nil, err
	}
	if t.CommunicationAuth != "" {
		req.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	}
	principal := os.Getenv("HUB_PRINCIPAL_ID")
	if principal == "" {
		principal = os.Getenv("HUB_USER_ID")
	}
	req.Header.Set("X-Hub-Principal", principal)
	client := t.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

func (t *Tools) inboundGet(ctx context.Context, id string) (string, []byte, error) {
	resp, err := t.inboundCall(ctx, http.MethodGet, id)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("inbound fetch rejected: %d", resp.StatusCode)
	}
	name := resp.Header.Get("X-Hub-Name")
	data, err := io.ReadAll(io.LimitReader(resp.Body, 9<<20))
	if err != nil {
		return "", nil, err
	}
	return name, data, nil
}

func (t *Tools) inboundConsume(ctx context.Context, id string) error {
	resp, err := t.inboundCall(ctx, http.MethodPost, id+"/consume")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("consume rejected: %d", resp.StatusCode)
	}
	return nil
}
