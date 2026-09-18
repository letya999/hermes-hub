// Package provider is a replaceable secret-source boundary. No provider is a
// mandatory canonical store. Normal ToolHub APIs never return Bundle values.
package provider

import (
	"context"
	"errors"
)

var (
	ErrUnavailable = errors.New("secret provider unavailable")
	ErrReadOnly    = errors.New("secret provider is read-only")
	ErrNotFound    = errors.New("secret object not found")
)

type Bundle map[string][]byte

func (b Bundle) Clone() Bundle {
	out := Bundle{}
	for k, v := range b {
		out[k] = append([]byte(nil), v...)
	}
	return out
}
func (b Bundle) Wipe() {
	for _, v := range b {
		clear(v)
	}
}

type Ref struct {
	Provider string `json:"provider"`
	Locator  string `json:"locator"`
	Version  string `json:"version,omitempty"`
}
type Capabilities struct {
	Write     bool `json:"write"`
	Delete    bool `json:"delete"`
	Versioned bool `json:"versioned"`
	Binary    bool `json:"binary"`
}
type Reader interface {
	Read(context.Context, Ref) (Bundle, error)
}
type Provider interface {
	Reader
	Capabilities() Capabilities
	Write(context.Context, Bundle) (Ref, error)
	Delete(context.Context, Ref) error
}

type Registry map[string]Provider

func (r Registry) Resolve(ctx context.Context, ref Ref) (Bundle, error) {
	p, ok := r[ref.Provider]
	if !ok {
		return nil, ErrUnavailable
	}
	return p.Read(ctx, ref)
}
func (r Registry) Put(ctx context.Context, id string, b Bundle) (Ref, error) {
	p, ok := r[id]
	if !ok {
		return Ref{}, ErrUnavailable
	}
	if !p.Capabilities().Write {
		return Ref{}, ErrReadOnly
	}
	return p.Write(ctx, b)
}
