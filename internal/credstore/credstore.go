// Package credstore persists secret values as ciphertext behind opaque locators.
// It does not decide ToolHub ownership or authorization.
package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	Schema         = 1
	KeySize        = 32
	MaxValues      = 32
	MaxValueBytes  = 16 * 1024
	StatusActive   = "active"
	StatusRevoked  = "revoked"
	StatusRemoved  = "removed"
	StatusDegraded = "degraded"
	BackendLocal   = "local"
	BackendVault   = "vault"
)

var (
	ErrInvalid   = errors.New("invalid credential store")
	ErrNotFound  = errors.New("credential locator not found")
	ErrForbidden = errors.New("credential locator owner mismatch")
	ErrKey       = errors.New("credential encryption key rejected")
	ErrStale     = errors.New("credential ciphertext is not readable with this key")
)

type RecordInfo struct {
	Locator          string
	Owner            string
	Names            []string
	Status           string
	Revision         uint64
	TerminalExposure bool
}

type Backend interface {
	Name() string
	NewLocator() (string, error)
	Put(locator, owner string, values map[string]string) error
	Get(locator, owner string) (map[string]string, error)
	Delete(locator, owner string) error
	Info(locator, owner string) (RecordInfo, error)
	List(owner string) ([]RecordInfo, error)
	SetStatus(locator, owner, status string) error
	SetTerminalExposure(locator, owner string, enabled bool) error
	Backup(w io.Writer) error
	Restore(r io.Reader) error
	RotateKey(newKey []byte) error
}

type Options struct {
	Path    string
	Key     []byte
	KeyFile string
	Backend string
}

type Local struct {
	mu      sync.Mutex
	path    string
	key     []byte
	backend string
	state   snapshot
}

type snapshot struct {
	Schema  int                     `json:"schema"`
	KeyID   string                  `json:"key_id"`
	Backend string                  `json:"backend"`
	Records map[string]storedRecord `json:"records"`
}

type storedRecord struct {
	Nonce            string   `json:"nonce"`
	Ciphertext       string   `json:"ciphertext"`
	Owner            string   `json:"owner"`
	Names            []string `json:"names"`
	Status           string   `json:"status"`
	Revision         uint64   `json:"revision"`
	TerminalExposure bool     `json:"terminal_exposure,omitempty"`
}

func Open(opts Options) (Backend, error) {
	if err := safePath(opts.Path); err != nil {
		return nil, err
	}
	key, err := loadKey(opts)
	if err != nil {
		return nil, err
	}
	if opts.KeyFile != "" {
		keyAbs, err := filepath.Abs(opts.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("%w: key path", ErrKey)
		}
		storeDir := filepath.Dir(opts.Path)
		if containedPath(storeDir, keyAbs) {
			return nil, fmt.Errorf("%w: encryption key must not live in the secret store directory", ErrKey)
		}
	}
	backend := opts.Backend
	if backend == "" {
		backend = BackendLocal
	}
	if backend != BackendLocal && backend != BackendVault {
		return nil, fmt.Errorf("%w: unknown backend %q", ErrInvalid, backend)
	}
	store := &Local{path: opts.Path, key: append([]byte(nil), key...), backend: backend, state: snapshot{Schema: Schema, KeyID: keyID(key), Backend: backend, Records: map[string]storedRecord{}}}
	if _, err := os.Lstat(opts.Path); errors.Is(err, os.ErrNotExist) {
		if err := store.persistLocked(); err != nil {
			return nil, err
		}
		return store, nil
	} else if err != nil {
		return nil, err
	}
	if err := store.loadLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Local) Name() string { return s.backend }

func (s *Local) NewLocator() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	prefix := "loc"
	if s.backend == BackendVault {
		prefix = "vault"
	}
	return prefix + "-" + hex.EncodeToString(raw[:]), nil
}

func (s *Local) Put(locator, owner string, values map[string]string) error {
	if err := validateLocator(locator); err != nil {
		return err
	}
	if err := validateOwner(owner); err != nil {
		return err
	}
	names, payload, err := encodeValues(values)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	nonce, ciphertext, err := encrypt(s.key, locator, owner, payload)
	if err != nil {
		return err
	}
	revision := uint64(1)
	terminal := false
	if existing, ok := s.state.Records[locator]; ok {
		if existing.Owner != owner {
			return fmt.Errorf("%w: locator", ErrForbidden)
		}
		revision = existing.Revision + 1
		terminal = existing.TerminalExposure
	}
	s.state.Records[locator] = storedRecord{Nonce: nonce, Ciphertext: ciphertext, Owner: owner, Names: names, Status: StatusActive, Revision: revision, TerminalExposure: terminal}
	return s.persistLocked()
}

func (s *Local) Get(locator, owner string) (map[string]string, error) {
	info, record, err := s.lookup(locator, owner)
	if err != nil {
		return nil, err
	}
	if info.Status != StatusActive {
		return nil, fmt.Errorf("%w: locator status %s", ErrNotFound, info.Status)
	}
	plain, err := decrypt(s.key, locator, owner, record.Nonce, record.Ciphertext)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	if err := json.Unmarshal(plain, &values); err != nil {
		return nil, fmt.Errorf("%w: ciphertext payload", ErrStale)
	}
	return values, nil
}

func (s *Local) Delete(locator, owner string) error {
	if _, _, err := s.lookup(locator, owner); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	record, ok := s.state.Records[locator]
	if !ok || record.Owner != owner {
		return fmt.Errorf("%w: locator", ErrNotFound)
	}
	delete(s.state.Records, locator)
	return s.persistLocked()
}

func (s *Local) Info(locator, owner string) (RecordInfo, error) {
	info, _, err := s.lookup(locator, owner)
	return info, err
}

func (s *Local) List(owner string) ([]RecordInfo, error) {
	if err := validateOwner(owner); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	result := make([]RecordInfo, 0)
	for locator, record := range s.state.Records {
		if record.Owner != owner {
			continue
		}
		result = append(result, infoOf(locator, record))
	}
	slices.SortFunc(result, func(a, b RecordInfo) int {
		return strings.Compare(strings.Join(a.Names, ","), strings.Join(b.Names, ","))
	})
	return result, nil
}

func (s *Local) SetStatus(locator, owner, status string) error {
	switch status {
	case StatusActive, StatusRevoked, StatusRemoved, StatusDegraded:
	default:
		return fmt.Errorf("%w: status", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	record, ok := s.state.Records[locator]
	if !ok {
		return fmt.Errorf("%w: locator", ErrNotFound)
	}
	if record.Owner != owner {
		return fmt.Errorf("%w: locator", ErrForbidden)
	}
	record.Status = status
	record.Revision++
	s.state.Records[locator] = record
	return s.persistLocked()
}

func (s *Local) SetTerminalExposure(locator, owner string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	record, ok := s.state.Records[locator]
	if !ok {
		return fmt.Errorf("%w: locator", ErrNotFound)
	}
	if record.Owner != owner {
		return fmt.Errorf("%w: locator", ErrForbidden)
	}
	record.TerminalExposure = enabled
	record.Revision++
	s.state.Records[locator] = record
	return s.persistLocked()
}

func (s *Local) Backup(w io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(string(body)), "hub_credential_key") {
		return fmt.Errorf("%w: backup would include a key name", ErrInvalid)
	}
	_, err = w.Write(body)
	return err
}

func (s *Local) Restore(r io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(r, 8<<20+1))
	if err != nil {
		return err
	}
	if len(body) > 8<<20 {
		return fmt.Errorf("%w: backup too large", ErrInvalid)
	}
	state, err := decodeSnapshot(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	return s.persistLocked()
}

func (s *Local) RotateKey(newKey []byte) error {
	if err := validateKey(newKey); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	next := snapshot{Schema: Schema, KeyID: keyID(newKey), Backend: s.backend, Records: map[string]storedRecord{}}
	for locator, record := range s.state.Records {
		plain, err := decrypt(s.key, locator, record.Owner, record.Nonce, record.Ciphertext)
		if err != nil {
			return err
		}
		nonce, ciphertext, err := encrypt(newKey, locator, record.Owner, plain)
		if err != nil {
			return err
		}
		record.Nonce = nonce
		record.Ciphertext = ciphertext
		next.Records[locator] = record
	}
	s.key = append([]byte(nil), newKey...)
	s.state = next
	return s.persistLocked()
}

func (s *Local) lookup(locator, owner string) (RecordInfo, storedRecord, error) {
	if err := validateLocator(locator); err != nil {
		return RecordInfo{}, storedRecord{}, err
	}
	if err := validateOwner(owner); err != nil {
		return RecordInfo{}, storedRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return RecordInfo{}, storedRecord{}, err
	}
	record, ok := s.state.Records[locator]
	if !ok {
		return RecordInfo{}, storedRecord{}, fmt.Errorf("%w: locator", ErrNotFound)
	}
	if record.Owner != owner {
		return RecordInfo{}, storedRecord{}, fmt.Errorf("%w: locator", ErrForbidden)
	}
	return infoOf(locator, record), record, nil
}

func (s *Local) loadLocked() error {
	body, err := os.ReadFile(s.path) // #nosec G304 -- path is checked for symlink components.
	if err != nil {
		return err
	}
	state, err := decodeSnapshot(body)
	if err != nil {
		return err
	}
	if state.KeyID != keyID(s.key) {
		return fmt.Errorf("%w: store key id", ErrStale)
	}
	s.state = state
	if s.state.Backend == "" {
		s.state.Backend = s.backend
	}
	return nil
}

func (s *Local) reloadLocked() error {
	if _, err := os.Lstat(s.path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return s.loadLocked()
}

func (s *Local) persistLocked() error {
	if s.state.Records == nil {
		s.state.Records = map[string]storedRecord{}
	}
	s.state.Schema = Schema
	s.state.KeyID = keyID(s.key)
	s.state.Backend = s.backend
	body, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".credstore-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func decodeSnapshot(body []byte) (snapshot, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var state snapshot
	if err := decoder.Decode(&state); err != nil {
		return snapshot{}, fmt.Errorf("%w: store JSON: %v", ErrInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return snapshot{}, fmt.Errorf("%w: one store JSON document expected", ErrInvalid)
	}
	if state.Schema != Schema || state.KeyID == "" {
		return snapshot{}, fmt.Errorf("%w: store schema", ErrInvalid)
	}
	if state.Records == nil {
		state.Records = map[string]storedRecord{}
	}
	return state, nil
}

func encodeValues(values map[string]string) ([]string, []byte, error) {
	if len(values) == 0 || len(values) > MaxValues {
		return nil, nil, fmt.Errorf("%w: values", ErrInvalid)
	}
	names := make([]string, 0, len(values))
	for key, value := range values {
		if !validName(key) || len(value) == 0 || len(value) > MaxValueBytes || strings.ContainsAny(value, "\x00") {
			return nil, nil, fmt.Errorf("%w: secret %s", ErrInvalid, key)
		}
		names = append(names, key)
	}
	slices.Sort(names)
	payload, err := json.Marshal(values)
	if err != nil {
		return nil, nil, err
	}
	return names, payload, nil
}

func encrypt(key []byte, locator, owner string, plain []byte) (string, string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	sealed := gcm.Seal(nil, nonce, plain, []byte(locator+"\x00"+owner))
	return base64.RawStdEncoding.EncodeToString(nonce), base64.RawStdEncoding.EncodeToString(sealed), nil
}

func decrypt(key []byte, locator, owner, nonceB64, ciphertextB64 string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStale, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStale, err)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(nonceB64)
	if err != nil {
		return nil, fmt.Errorf("%w: nonce", ErrStale)
	}
	sealed, err := base64.RawStdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("%w: ciphertext", ErrStale)
	}
	plain, err := gcm.Open(nil, nonce, sealed, []byte(locator+"\x00"+owner))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStale, err)
	}
	return plain, nil
}

func loadKey(opts Options) ([]byte, error) {
	if len(opts.Key) > 0 {
		if err := validateKey(opts.Key); err != nil {
			return nil, err
		}
		return append([]byte(nil), opts.Key...), nil
	}
	if opts.KeyFile != "" {
		if err := safePath(opts.KeyFile); err != nil {
			return nil, err
		}
		body, err := os.ReadFile(opts.KeyFile) // #nosec G304 -- key path is checked for symlink components.
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrKey, err)
		}
		return parseKey(body)
	}
	if raw := strings.TrimSpace(os.Getenv("HUB_CREDENTIAL_KEY")); raw != "" {
		return parseKey([]byte(raw))
	}
	if path := strings.TrimSpace(os.Getenv("HUB_CREDENTIAL_KEY_FILE")); path != "" {
		opts.KeyFile = path
		return loadKey(opts)
	}
	return nil, fmt.Errorf("%w: HUB_CREDENTIAL_KEY or HUB_CREDENTIAL_KEY_FILE is required", ErrKey)
}

func parseKey(body []byte) ([]byte, error) {
	if len(body) == KeySize {
		key := append([]byte(nil), body...)
		return key, validateKey(key)
	}
	raw := strings.TrimSpace(string(body))
	if len(raw) == hex.EncodedLen(KeySize) {
		key, err := hex.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: hex key", ErrKey)
		}
		return key, validateKey(key)
	}
	key := []byte(raw)
	if len(key) != KeySize {
		decoded, err := hex.DecodeString(raw)
		if err == nil {
			key = decoded
		}
	}
	return key, validateKey(key)
}

func validateKey(key []byte) error {
	if len(key) != KeySize {
		return fmt.Errorf("%w: key must be %d bytes", ErrKey, KeySize)
	}
	return nil
}

func keyID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

func WriteKeyFile(path string, key []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := safePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".credkey-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write([]byte(hex.EncodeToString(key)))
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func validateLocator(locator string) error {
	if locator == "" || len(locator) > 256 || strings.ContainsAny(locator, "\r\n=") {
		return fmt.Errorf("%w: locator", ErrInvalid)
	}
	return nil
}

func validateOwner(owner string) error {
	if owner == "" || len(owner) > 64 || strings.ContainsAny(owner, "\r\n") {
		return fmt.Errorf("%w: owner", ErrInvalid)
	}
	return nil
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		if i == 0 && (r < 'A' || r > 'Z') {
			return false
		}
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func infoOf(locator string, record storedRecord) RecordInfo {
	names := append([]string(nil), record.Names...)
	return RecordInfo{Locator: locator, Owner: record.Owner, Names: names, Status: record.Status, Revision: record.Revision, TerminalExposure: record.TerminalExposure}
}

func safePath(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: path must be absolute", ErrInvalid)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: path: %v", ErrInvalid, err)
	}
	volume := filepath.VolumeName(abs)
	root := volume + string(filepath.Separator)
	current := root
	for _, part := range strings.Split(strings.TrimPrefix(abs, root), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: path: %v", ErrInvalid, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: path contains symlink", ErrInvalid)
		}
	}
	return nil
}

func containedPath(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
