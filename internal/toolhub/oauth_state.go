package toolhub

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The account filename exists only in this lease; Broker snapshots the fixed
// contract filename after the backend has quiesced the workload.
func materializeOAuthStateView(a *api.OAuthRecipe, injection *api.CredentialInjection) error {
	if a == nil || a.AccountInput == "" {
		return nil
	}
	account := injection.Environment[a.AccountInput]
	if strings.ContainsAny(account, "/\\\x00\r\n") || strings.Contains(account, "..") {
		return api.ErrIsolation
	}
	alias := strings.ReplaceAll(url.QueryEscape(account), "%40", "@") + ".json"
	var directory string
	for _, mount := range injection.Mounts {
		if mount.Target == a.StateTarget && !mount.ReadOnly {
			directory = mount.Source
		}
	}
	if directory == "" || injection.Checkpoint == nil {
		return api.ErrIsolation
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return api.ErrIsolation
	}
	defer root.Close()
	data, err := readOAuthSession(root, a.TokenFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return api.ErrIsolation
	}
	defer clear(data)
	if _, err := root.Lstat(alias); !errors.Is(err, os.ErrNotExist) {
		return api.ErrIsolation
	}
	file, err := root.OpenFile(alias, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return api.ErrIsolation
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return api.ErrIsolation
	}
	checkpoint := injection.Checkpoint
	injection.Checkpoint = func() error {
		root, err := os.OpenRoot(directory)
		if err != nil {
			return api.ErrIsolation
		}
		defer root.Close()
		updated, err := readOAuthSession(root, alias)
		if err != nil {
			return api.ErrIsolation
		}
		defer clear(updated)
		temporary := ".oauth-" + api.RandomNonce() + ".partial"
		file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return api.ErrIsolation
		}
		defer root.Remove(temporary)
		_, writeErr := file.Write(updated)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return api.ErrIsolation
		}
		if err := root.Rename(temporary, a.TokenFile); err != nil {
			return api.ErrIsolation
		}
		return checkpoint()
	}
	return nil
}

func readOAuthSession(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, api.ErrIsolation
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, api.ErrIsolation
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 || !json.Valid(data) {
		clear(data)
		return nil, api.ErrIsolation
	}
	return data, nil
}
