package runtime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"
)

type hermesRunBody struct {
	Input        string `json:"input"`
	SessionID    string `json:"session_id"`
	Instructions string `json:"instructions,omitempty"`
}

var admissionMu sync.Mutex
var errAdmissionReceipt = errors.New("admission receipt unavailable")

// Persist the exact Hermes payload in the owning runtime before POST; the
// supervisor keeps metadata only and cannot reconstruct the original input.
func admissionPayload(request ExecuteRequest, create *hermesRunBody) (hermesRunBody, error) {
	admissionMu.Lock()
	defer admissionMu.Unlock()
	var payload hermesRunBody
	if request.JobID == "" {
		return payload, errAdmissionReceipt
	}
	request.Text = ""
	identity, _ := json.Marshal(request)
	hash := sha256.Sum256(identity)
	name := hex.EncodeToString(hash[:]) + ".json"
	root, err := os.OpenRoot(state)
	if err != nil {
		return payload, err
	}
	defer root.Close()
	if create != nil {
		if err := root.MkdirAll("admissions", 0700); err != nil {
			return payload, err
		}
	}
	dir, err := root.OpenRoot("admissions")
	if err != nil {
		return payload, err
	}
	defer dir.Close()
	f, err := dir.Open(name)
	if err == nil {
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return payload, err
		}
		// Never replay beyond Hermes' 24-hour key retention; leave an hour
		// for transit and refuse clock rollback instead of risking new work.
		age := time.Since(info.ModTime())
		if age < 0 || age >= 23*time.Hour {
			return payload, errAdmissionReceipt
		}
		if err := json.NewDecoder(io.LimitReader(f, maxPromptBytes+64*1024)).Decode(&payload); err != nil {
			return payload, err
		}
		if payload.Input == "" || payload.SessionID == "" {
			return payload, errAdmissionReceipt
		}
		if create != nil && *create != payload {
			return payload, errAdmissionReceipt
		}
		return payload, nil
	}
	if !errors.Is(err, os.ErrNotExist) || create == nil {
		return payload, err
	}
	body, _ := json.Marshal(create)
	tmp := ".admission-" + rand.Text()
	f, err = dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return payload, err
	}
	defer dir.Remove(tmp)
	_, err = f.Write(body)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return payload, err
	}
	if closeErr != nil {
		return payload, closeErr
	}
	if err := dir.Rename(tmp, name); err != nil {
		return payload, err
	}
	entries, _ := fs.ReadDir(dir.FS(), ".")
	for _, entry := range entries {
		if len(entry.Name()) != 69 || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
			_ = dir.Remove(entry.Name())
		}
	}
	return *create, nil
}
