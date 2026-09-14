package contextlife

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

const Schema = 1

var excludedNames = map[string]bool{
	"secrets.dev.env": true, "secrets.prod.env": true, "runtime.auth": true, "store.enc": true,
	"communication.dev.env": true, "communication.prod.env": true, "supervisor.auth": true,
}

type Manifest struct {
	Schema    int               `json:"schema"`
	User      string            `json:"user"`
	ContextID string            `json:"context_id"`
	CreatedAt time.Time         `json:"created_at"`
	Includes  []string          `json:"includes"`
	Excludes  []string          `json:"excludes"`
	SHA256    map[string]string `json:"sha256"`
}

type PurgeReport struct {
	User    string   `json:"user"`
	Removed []string `json:"removed"`
}

func Backup(home, user, out string, spoolDir string) error {
	if !identity.ValidID(user) {
		return errors.New("invalid user")
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	info, err := os.Stat(absHome)
	if err != nil || !info.IsDir() {
		return errors.New("home is not a directory")
	}
	file, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	zipw := zip.NewWriter(file)
	hashes := map[string]string{}
	includes := []string{}
	addDir := func(rel string) error {
		root := filepath.Join(absHome, filepath.FromSlash(rel))
		if _, err := os.Stat(root); err != nil {
			return nil
		}
		includes = append(includes, rel)
		return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if excludedNames[info.Name()] || strings.HasSuffix(info.Name(), ".enc") {
				return nil
			}
			relPath, err := filepath.Rel(absHome, path)
			if err != nil {
				return err
			}
			return addFile(zipw, hashes, filepath.ToSlash(relPath), path)
		})
	}
	for _, rel := range []string{"hermes", "workspace", "connections", "skills"} {
		if err := addDir(rel); err != nil {
			_ = zipw.Close()
			return err
		}
	}
	toolhub := filepath.Join(absHome, "runtime", "toolhub", "store.json")
	if _, err := os.Stat(toolhub); err == nil {
		includes = append(includes, "toolhub")
		if err := addFile(zipw, hashes, "runtime/toolhub/store.json", toolhub); err != nil {
			_ = zipw.Close()
			return err
		}
	}
	if spoolDir != "" {
		for _, rel := range []string{"schedules", "outbox", "mappings", "occurrences"} {
			root := filepath.Join(spoolDir, rel)
			if _, err := os.Stat(root); err != nil {
				continue
			}
			includes = append(includes, "spool/"+rel)
			_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				relPath, err := filepath.Rel(spoolDir, path)
				if err != nil {
					return err
				}
				return addFile(zipw, hashes, "spool/"+filepath.ToSlash(relPath), path)
			})
		}
	}
	manifest := Manifest{Schema: Schema, User: user, ContextID: user, CreatedAt: time.Now().UTC(), Includes: includes, Excludes: []string{"plaintext-secrets", "credential-store"}, SHA256: hashes}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = zipw.Close()
		return err
	}
	header, err := zip.FileInfoHeader(dummyInfo("manifest.json", len(body)))
	if err != nil {
		_ = zipw.Close()
		return err
	}
	header.Name = "manifest.json"
	header.Method = zip.Deflate
	w, err := zipw.CreateHeader(header)
	if err != nil {
		_ = zipw.Close()
		return err
	}
	if _, err := w.Write(append(body, '\n')); err != nil {
		_ = zipw.Close()
		return err
	}
	if err := zipw.Close(); err != nil {
		return err
	}
	return Verify(out, user)
}

func Verify(archive, user string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	var manifest Manifest
	files := map[string]*zip.File{}
	for _, f := range r.File {
		files[f.Name] = f
		if f.Name == "manifest.json" {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			body, err := io.ReadAll(io.LimitReader(rc, 2<<20))
			_ = rc.Close()
			if err != nil || json.Unmarshal(body, &manifest) != nil || manifest.Schema != Schema {
				return errors.New("invalid backup manifest")
			}
		}
	}
	if manifest.User != user {
		return errors.New("backup user mismatch")
	}
	if _, ok := files["manifest.json"]; !ok {
		return errors.New("backup missing manifest")
	}
	for name, expected := range manifest.SHA256 {
		file, ok := files[name]
		if !ok {
			return errors.New("backup missing " + name)
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		sum := sha256.New()
		_, err = io.Copy(sum, io.LimitReader(rc, 64<<20))
		_ = rc.Close()
		if err != nil {
			return err
		}
		if hex.EncodeToString(sum.Sum(nil)) != expected {
			return errors.New("backup hash mismatch")
		}
	}
	return nil
}

func Restore(home, user, archive, spoolDir string) error {
	if err := Verify(archive, user); err != nil {
		return err
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	absSpool := ""
	if strings.TrimSpace(spoolDir) != "" {
		absSpool, err = filepath.Abs(spoolDir)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(absSpool, 0700); err != nil {
			return err
		}
	}
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	staging := absHome + ".restore-partial"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, f := range r.File {
		if f.Name == "manifest.json" || excludedNames[filepath.Base(f.Name)] || strings.Contains(f.Name, "..") {
			continue
		}
		if strings.HasPrefix(f.Name, "spool/") {
			rel, ok := restoreSpoolRel(f.Name)
			if !ok || absSpool == "" || f.FileInfo().IsDir() {
				continue
			}
			target := filepath.Join(absSpool, filepath.FromSlash(rel))
			if !strings.HasPrefix(target, absSpool) {
				return errors.New("unsafe backup path")
			}
			if err := writeZipFile(f, target); err != nil {
				return err
			}
			continue
		}
		target := filepath.Join(staging, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(target, staging) {
			return errors.New("unsafe backup path")
		}
		if err := writeZipFile(f, target); err != nil {
			return err
		}
	}
	for _, rel := range []string{"hermes", "workspace", "connections", "skills"} {
		src := filepath.Join(staging, rel)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dest := filepath.Join(absHome, rel)
		_ = os.RemoveAll(dest)
		if err := os.Rename(src, dest); err != nil {
			return err
		}
	}
	srcTool := filepath.Join(staging, "runtime", "toolhub", "store.json")
	if _, err := os.Stat(srcTool); err == nil {
		dest := filepath.Join(absHome, "runtime", "toolhub")
		if err := os.MkdirAll(dest, 0700); err != nil {
			return err
		}
		body, err := os.ReadFile(srcTool)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dest, "store.json"), body, 0600); err != nil {
			return err
		}
	}
	return nil
}

func Purge(home, user, confirm string) (PurgeReport, error) {
	report := PurgeReport{User: user}
	if user == "" || user != confirm || !identity.ValidID(user) {
		return report, errors.New("purge requires an explicit matching --confirm user")
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return report, err
	}
	for _, rel := range []string{"hermes", "workspace", "connections", "skills", "archive"} {
		path := filepath.Join(absHome, rel)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return report, err
		}
		report.Removed = append(report.Removed, rel)
	}
	return report, nil
}

func MemoryFiles(home string) ([]string, error) {
	root := filepath.Join(home, "hermes", "memories")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func DeleteMemory(home, name string) error {
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return errors.New("invalid memory name")
	}
	return os.Remove(filepath.Join(home, "hermes", "memories", name))
}

func restoreSpoolRel(name string) (string, bool) {
	rel := strings.TrimPrefix(name, "spool/")
	if strings.HasPrefix(rel, "schedules/") || strings.HasPrefix(rel, "mappings/") {
		return rel, true
	}
	return "", false
}

func writeZipFile(f *zip.File, target string) error {
	if f.FileInfo().IsDir() {
		return os.MkdirAll(target, 0700)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, io.LimitReader(rc, 64<<20))
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func addFile(zipw *zip.Writer, hashes map[string]string, name, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(name), "secret") && strings.Contains(string(body), "=") {
		return nil
	}
	sum := sha256.Sum256(body)
	hashes[name] = hex.EncodeToString(sum[:])
	header, err := zip.FileInfoHeader(dummyInfo(name, len(body)))
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate
	w, err := zipw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

type fileInfo struct {
	name string
	size int
}

func dummyInfo(name string, size int) os.FileInfo { return fileInfo{name: name, size: size} }
func (f fileInfo) Name() string                   { return filepath.Base(f.name) }
func (f fileInfo) Size() int64                    { return int64(f.size) }
func (f fileInfo) Mode() os.FileMode              { return 0600 }
func (f fileInfo) ModTime() time.Time             { return time.Now().UTC() }
func (f fileInfo) IsDir() bool                    { return false }
func (f fileInfo) Sys() any                       { return nil }
