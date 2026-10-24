package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/internal/securefs"
	"github.com/letya999/credential-broker/internal/strictjson"
)

var locatorRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{43}$`)

type Local struct {
	ID          string
	dir         string
	box         *seal.Box
	recoveryDir string
	recoveryBox *seal.Box
}

func NewLocal(id, dir string, key []byte) (*Local, error) {
	if err := securefs.Dir(dir); err != nil {
		return nil, err
	}
	b, e := seal.New(key)
	if e != nil {
		return nil, e
	}
	return &Local{ID: id, dir: dir, box: b}, nil
}

func NewRecoverableLocal(id, dir, recoveryDir string, key, recoveryKey []byte) (*Local, error) {
	l, err := NewLocal(id, dir, key)
	if err != nil {
		return nil, err
	}
	if err := securefs.Dir(recoveryDir); err != nil {
		return nil, err
	}
	l.recoveryBox, err = seal.New(recoveryKey)
	if err != nil {
		return nil, err
	}
	l.recoveryDir = recoveryDir
	return l, nil
}

func (l *Local) BackupExisting(ctx context.Context, ref Ref) error {
	if l.recoveryBox == nil {
		return ErrUnavailable
	}
	values, err := l.Read(ctx, ref)
	if err != nil {
		return err
	}
	defer values.Wipe()
	raw, err := json.Marshal(values)
	if err != nil {
		return ErrUnavailable
	}
	defer clear(raw)
	aad := []byte(l.ID + "/" + ref.Locator + "/1")
	existing, err := securefs.Read(l.recoveryDir, ref.Locator, 600<<10)
	if err == nil {
		plain, err := l.recoveryBox.Open(existing, aad)
		if err != nil || !bytes.Equal(plain, raw) {
			return ErrUnavailable
		}
		clear(plain)
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	enc, err := l.recoveryBox.Seal(raw, aad)
	if err != nil || securefs.Create(l.recoveryDir, ref.Locator, enc, 0600) != nil {
		return ErrUnavailable
	}
	return nil
}
func (*Local) Capabilities() Capabilities { return Capabilities{true, true, true, true} }
func (l *Local) Write(ctx context.Context, b Bundle) (Ref, error) {
	if ctx.Err() != nil {
		return Ref{}, ctx.Err()
	}
	raw, e := json.Marshal(b)
	if e != nil || len(raw) > 512<<10 {
		return Ref{}, ErrUnavailable
	}
	defer clear(raw)
	id := seal.Random()
	ref := Ref{l.ID, id, "1"}
	enc, e := l.box.Seal(raw, []byte(l.ID+"/"+id+"/1"))
	if e != nil {
		return Ref{}, ErrUnavailable
	}
	if l.recoveryBox != nil {
		backup, err := l.recoveryBox.Seal(raw, []byte(l.ID+"/"+id+"/1"))
		if err != nil || securefs.Create(l.recoveryDir, id, backup, 0600) != nil {
			return Ref{}, ErrUnavailable
		}
	}
	if e = securefs.Create(l.dir, id, enc, 0600); e != nil {
		return Ref{}, ErrUnavailable
	}
	return ref, nil
}
func (l *Local) Read(ctx context.Context, r Ref) (Bundle, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if r.Provider != l.ID || !locatorRE.MatchString(r.Locator) || r.Version != "1" {
		return nil, ErrNotFound
	}
	enc, e := securefs.Read(l.dir, r.Locator, 600<<10)
	box := l.box
	if errors.Is(e, os.ErrNotExist) && l.recoveryBox != nil {
		enc, e = securefs.Read(l.recoveryDir, r.Locator, 600<<10)
		box = l.recoveryBox
	}
	if errors.Is(e, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, ErrUnavailable
	}
	raw, e := box.Open(enc, []byte(l.ID+"/"+r.Locator+"/1"))
	if e != nil {
		return nil, ErrUnavailable
	}
	defer clear(raw)
	var b Bundle
	if strictjson.Decode(raw, &b) != nil || b == nil {
		return nil, ErrUnavailable
	}
	return b, nil
}
func (l *Local) Delete(ctx context.Context, r Ref) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r.Provider != l.ID || !locatorRE.MatchString(r.Locator) || r.Version != "1" {
		return ErrNotFound
	}
	if l.recoveryBox != nil {
		e := os.Remove(filepath.Join(l.recoveryDir, r.Locator))
		if e != nil && !errors.Is(e, os.ErrNotExist) || securefs.SyncDir(l.recoveryDir) != nil {
			return ErrUnavailable
		}
	}
	e := os.Remove(filepath.Join(l.dir, r.Locator))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return ErrUnavailable
	}
	return securefs.SyncDir(l.dir)
}
