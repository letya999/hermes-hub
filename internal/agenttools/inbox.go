package agenttools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

// inboxFetch stores a complete receipt before acknowledging the staged copy.
func (t *Tools) inboxFetch(ctx context.Context, ws *os.Root, r Input) (result map[string]any, err error) {
	if t.CommunicationURL == "" || !safe(r.ID) || strings.Contains(r.ID, "/") || r.ID == "." {
		return nil, errors.New("inbox fetch requires a safe staged file id")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lock != nil {
		if err := t.lock.Lock(); err != nil {
			return nil, err
		}
		defer t.lock.Unlock()
	}
	target := "inbox/" + r.ID
	receipt := target + "/receipt.json"
	if body, err := ws.ReadFile(receipt); err == nil {
		if err := json.Unmarshal(body, &result); err != nil || result == nil {
			return nil, errors.New("invalid inbound receipt")
		}
		result["acknowledged"] = t.inboundConsume(ctx, r.ID) == nil
		return result, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	name, data, err := t.inboundGet(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	base := path.Base(name)
	if !safe(base) || base == "." {
		return nil, errors.New("unsafe file name")
	}
	if err := ws.MkdirAll("inbox", 0700); err != nil {
		return nil, err
	}
	if err := ws.Mkdir(target, 0700); err != nil {
		return nil, errors.New("inbound target exists without a completed receipt; inspect it before retrying")
	}
	complete := false
	defer func() {
		if !complete {
			_ = ws.RemoveAll(target)
		}
	}()
	if strings.HasSuffix(strings.ToLower(base), ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, errors.New("inbound file is not a readable zip")
		}
		files, err := unpackZip(ws, zr, target+"/files")
		if err != nil {
			return nil, err
		}
		result = map[string]any{"path": target + "/files", "files": files}
	} else {
		if err := ws.Mkdir(target+"/files", 0700); err != nil {
			return nil, err
		}
		dest := target + "/files/" + base
		f, err := ws.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		result = map[string]any{"path": dest, "size": len(data)}
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	f, err := ws.OpenFile(receipt, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	complete = true
	result["acknowledged"] = t.inboundConsume(ctx, r.ID) == nil
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
	if err != nil {
		return "", nil, err
	}
	if len(data) > 9<<20 {
		return "", nil, errors.New("inbound file exceeds size limit")
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
