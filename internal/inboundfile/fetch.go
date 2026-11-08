package inboundfile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
)

type fetcher struct {
	CommunicationURL  string
	CommunicationAuth string
	InboundPrincipal  string
	HTTP              *http.Client
	lock              *flock.Flock
	unpack            func(*os.Root, *zip.Reader, string) (int, error)
}

// Fetch writes an inbound transfer under a confined root and records its receipt.
func Fetch(ctx context.Context, ws *os.Root, prefix, id, communicationURL, auth, principal string, client *http.Client, lock *flock.Flock, unpack func(*os.Root, *zip.Reader, string) (int, error)) (map[string]any, error) {
	t := &fetcher{CommunicationURL: communicationURL, CommunicationAuth: auth, InboundPrincipal: principal, HTTP: client, lock: lock, unpack: unpack}
	return t.fetchAt(ctx, ws, id, prefix)
}

func safe(p string) bool {
	return fs.ValidPath(p) && !strings.Contains(p, "\\") && !strings.ContainsRune(p, 0)
}

// MaterializeInbound writes only the owner's inbox through a control-plane
// mount. The agent's view of the same directory remains read-only.
func MaterializeInbound(ctx context.Context, inboxRoot, principal, communicationURL, auth, id string) (map[string]any, error) {
	if !identity.ValidID(principal) || !filepath.IsAbs(inboxRoot) || communicationURL == "" || auth == "" {
		return nil, errors.New("inbound control materialization is unavailable")
	}
	root, err := os.OpenRoot(inboxRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	t := &fetcher{CommunicationURL: communicationURL, CommunicationAuth: auth, InboundPrincipal: principal,
		HTTP: &http.Client{Timeout: 30 * time.Second}, lock: flock.New(filepath.Join(inboxRoot, ".hub-writer.lock")), unpack: SafeUnpack}
	return t.fetchAt(ctx, root, id, "")
}

func (t *fetcher) fetchAt(ctx context.Context, ws *os.Root, id string, prefix string) (result map[string]any, err error) {
	if t.CommunicationURL == "" || !safe(id) || strings.Contains(id, "/") || id == "." {
		return nil, errors.New("inbox fetch requires a safe staged file id")
	}
	if t.lock != nil {
		if err := t.lock.Lock(); err != nil {
			return nil, err
		}
		defer t.lock.Unlock()
	}
	for _, source := range []string{"people", "services"} {
		receipt := prefix + source + "/" + id + "/receipt.json"
		body, err := ws.ReadFile(receipt)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &result); err != nil || result == nil {
			return nil, errors.New("invalid inbound receipt")
		}
		result["acknowledged"] = t.inboundConsume(ctx, id) == nil
		return result, nil
	}
	name, source, data, err := t.inboundGet(ctx, id)
	if err != nil {
		return nil, err
	}
	if source != "people" && source != "services" {
		return nil, errors.New("unknown inbound source")
	}
	target := prefix + source + "/" + id
	logical := "inbox/" + source + "/" + id
	receipt := target + "/receipt.json"
	base := path.Base(name)
	if !safe(base) || base == "." {
		return nil, errors.New("unsafe file name")
	}
	if err := ws.MkdirAll(prefix+source, 0700); err != nil {
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
		files, err := t.unpack(ws, zr, target+"/files")
		if err != nil {
			return nil, err
		}
		result = map[string]any{"path": logical + "/files", "files": files}
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
		result = map[string]any{"path": logical + "/files/" + base, "size": len(data)}
	}
	result["name"] = base
	result["source"] = source
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
	result["acknowledged"] = t.inboundConsume(ctx, id) == nil
	return result, nil
}

func (t *fetcher) inboundCall(ctx context.Context, method, id string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.CommunicationURL, "/")+"/v1/inbound/"+id, nil)
	if err != nil {
		return nil, err
	}
	if t.CommunicationAuth != "" {
		req.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	}
	principal := t.InboundPrincipal
	if principal == "" {
		principal = os.Getenv("HUB_PRINCIPAL_ID")
	}
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

func (t *fetcher) inboundGet(ctx context.Context, id string) (string, string, []byte, error) {
	resp, err := t.inboundCall(ctx, http.MethodGet, id)
	if err != nil {
		return "", "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", nil, fmt.Errorf("inbound fetch rejected: %d", resp.StatusCode)
	}
	name := resp.Header.Get("X-Hub-Name")
	source := resp.Header.Get("X-Hub-Source")
	if source == "" {
		source = "people"
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
	if err != nil {
		return "", "", nil, err
	}
	if len(data) > 9<<20 {
		return "", "", nil, errors.New("inbound file exceeds size limit")
	}
	return name, source, data, nil
}

func (t *fetcher) inboundConsume(ctx context.Context, id string) error {
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
