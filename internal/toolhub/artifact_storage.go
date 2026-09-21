package toolhub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StoredOCIArtifact is a private, integrity-checked quarantine object, not a
// trusted catalog definition. No build/review/secret-scan claim is synthesized.
// ArchiveDigest addresses exact tar bytes; Evidence addresses OCI content.
type StoredOCIArtifact struct {
	ArchiveDigest string              `json:"archive_digest"`
	Size          int64               `json:"size"`
	Evidence      OCIArtifactEvidence `json:"evidence"`
}

// PersistOCIArtifact requires an existing administrator-owned absolute directory.
// Publication is a same-directory hard link: existing objects are never replaced,
// including under concurrent writers or when a process crashes after publication.
func PersistOCIArtifact(ctx context.Context, directory string, source io.Reader, maxBytes int64) (StoredOCIArtifact, error) {
	if source == nil || maxBytes < 1 || maxBytes > 8<<30 {
		return StoredOCIArtifact{}, ErrInvalid
	}
	if err := safeStorePath(filepath.Join(directory, "artifact-check")); err != nil {
		return StoredOCIArtifact{}, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	defer root.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return StoredOCIArtifact{}, err
	}
	temporary := ".artifact-" + hex.EncodeToString(nonce[:]) + ".partial"
	file, err := root.OpenFile(temporary, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	defer root.Remove(temporary)
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(artifactContextReader{ctx, source}, maxBytes+1))
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	if n > maxBytes {
		return StoredOCIArtifact{}, fmt.Errorf("%w: oversized artifact", ErrInvalid)
	}
	if err := file.Sync(); err != nil {
		return StoredOCIArtifact{}, err
	}
	evidence, err := VerifyOCIArtifact(artifactContextReaderAt{ctx, file}, n, maxBytes)
	if ctx.Err() != nil {
		return StoredOCIArtifact{}, ctx.Err()
	}
	if err != nil {
		return StoredOCIArtifact{}, err
	}
	result := StoredOCIArtifact{ArchiveDigest: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Size: n, Evidence: evidence}
	if err := file.Close(); err != nil {
		return StoredOCIArtifact{}, err
	}
	if err := root.Link(temporary, artifactObjectName(result.ArchiveDigest)); err != nil {
		if !os.IsExist(err) {
			return StoredOCIArtifact{}, err
		}
		existing, stored, err := openArtifactObject(ctx, root, result.ArchiveDigest, maxBytes)
		if err != nil {
			return StoredOCIArtifact{}, err
		}
		defer existing.Close()
		if stored != result {
			return StoredOCIArtifact{}, ErrConflict
		}
	}
	return result, nil
}

// OpenStoredOCIArtifact rechecks checksum and OCI evidence on every read. It
// refuses symlinks, malformed digests and corrupted objects. The caller closes
// the returned file and must not give workloads access to this host directory.
func OpenStoredOCIArtifact(ctx context.Context, directory, digest string, maxBytes int64) (*os.File, StoredOCIArtifact, error) {
	if !digestPattern.MatchString(digest) || maxBytes < 1 || maxBytes > 8<<30 {
		return nil, StoredOCIArtifact{}, ErrInvalid
	}
	if err := safeStorePath(filepath.Join(directory, "artifact-check")); err != nil {
		return nil, StoredOCIArtifact{}, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, StoredOCIArtifact{}, err
	}
	defer root.Close()
	return openArtifactObject(ctx, root, digest, maxBytes)
}

func openArtifactObject(ctx context.Context, root *os.Root, digest string, maxBytes int64) (*os.File, StoredOCIArtifact, error) {
	name := artifactObjectName(digest)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, StoredOCIArtifact{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, StoredOCIArtifact{}, ErrInvalid
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, StoredOCIArtifact{}, err
	}
	failed := func(err error) (*os.File, StoredOCIArtifact, error) {
		_ = file.Close()
		return nil, StoredOCIArtifact{}, err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.NewSectionReader(artifactContextReaderAt{ctx, file}, 0, info.Size()+1))
	if err != nil {
		return failed(err)
	}
	if n != info.Size() || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != digest {
		return failed(ErrConflict)
	}
	evidence, err := VerifyOCIArtifact(artifactContextReaderAt{ctx, file}, n, maxBytes)
	if ctx.Err() != nil {
		return failed(ctx.Err())
	}
	if err != nil {
		return failed(err)
	}
	return file, StoredOCIArtifact{ArchiveDigest: digest, Size: n, Evidence: evidence}, nil
}

func artifactObjectName(digest string) string {
	return "sha256-" + strings.TrimPrefix(digest, "sha256:") + ".oci.tar"
}

type artifactContextReader struct {
	ctx context.Context
	io.Reader
}

func (r artifactContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}

type artifactContextReaderAt struct {
	ctx context.Context
	io.ReaderAt
}

func (r artifactContextReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReaderAt.ReadAt(data, offset)
}
