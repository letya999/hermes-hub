package hosting

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

func (s *Service) snapshot(ctx context.Context, source, release string) (int64, int, error) {
	parts := strings.Split(source, "/")
	current := ""
	for _, p := range parts {
		current = path.Join(current, p)
		info, err := s.workspace.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return 0, 0, ErrInvalid
		}
	}
	beforeRoot, err := s.workspace.Lstat(source)
	if err != nil {
		return 0, 0, ErrInvalid
	}
	src, err := s.workspace.OpenRoot(source)
	if err != nil {
		return 0, 0, ErrInvalid
	}
	defer src.Close()
	afterRoot, err := src.Stat(".")
	if err != nil || !os.SameFile(beforeRoot, afterRoot) {
		return 0, 0, ErrInvalid
	}
	if err = s.root.Mkdir(release, 0700); err != nil {
		return 0, 0, ErrUnavailable
	}
	dst, err := s.root.OpenRoot(release)
	if err != nil {
		return 0, 0, ErrUnavailable
	}
	defer dst.Close()
	used, count, err := s.usage()
	if err != nil {
		return 0, 0, err
	}
	manifest := map[string]os.FileInfo{".": beforeRoot}
	directories := []string{"."}
	var size int64
	files := 0
	err = fs.WalkDir(src.FS(), ".", func(p string, d fs.DirEntry, e error) error {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		if e != nil {
			return ErrInvalid
		}
		if p == "." {
			return nil
		}
		if !safePath(p) {
			return ErrInvalid
		}
		before, err := src.Lstat(p)
		if err != nil || before.Mode()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		manifest[p] = before
		if before.IsDir() {
			directories = append(directories, p)
			if count+files >= s.cfg.MaxFiles-4 || used+size+4096 > s.cfg.MaxBytes-metadataReserve {
				return ErrQuota
			}
			used += 4096
			count++
			if dst.Mkdir(p, 0700) != nil {
				return ErrUnavailable
			}
			return nil
		}
		if !before.Mode().IsRegular() || multipleLinks(before) {
			return ErrInvalid
		}
		in, err := src.OpenFile(p, readFlags(), 0)
		if err != nil {
			return ErrInvalid
		}
		defer in.Close()
		opened, err := in.Stat()
		if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || openedMultipleLinks(in) {
			return ErrInvalid
		}
		if opened.Size() > s.cfg.MaxReleaseBytes-size || opened.Size() > s.cfg.MaxBytes-metadataReserve-used-size || count+files >= s.cfg.MaxFiles-4 {
			return ErrQuota
		}
		out, err := dst.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return ErrUnavailable
		}
		limit := min(s.cfg.MaxReleaseBytes-size, s.cfg.MaxBytes-metadataReserve-used-size)
		n, copyErr := io.Copy(out, io.LimitReader(in, limit+1))
		syncErr := out.Sync()
		closeErr := out.Close()
		after, statErr := in.Stat()
		again, lstatErr := src.Lstat(p)
		if copyErr != nil || syncErr != nil || closeErr != nil {
			return ErrUnavailable
		}
		if n > limit {
			return ErrQuota
		}
		if statErr != nil || lstatErr != nil || !os.SameFile(before, again) || again.Mode()&os.ModeSymlink != 0 || after.Size() != n || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
			return ErrInvalid
		}
		size += n
		files++
		return nil
	})
	if err == nil {
		visited := 0
		err = fs.WalkDir(src.FS(), ".", func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return ErrInvalid
			}
			prior, ok := manifest[p]
			if !ok {
				return ErrInvalid
			}
			current, e := src.Lstat(p)
			if e != nil || !os.SameFile(prior, current) || current.Mode() != prior.Mode() || current.Size() != prior.Size() || !current.ModTime().Equal(prior.ModTime()) {
				return ErrInvalid
			}
			visited++
			return nil
		})
		if err == nil && visited != len(manifest) {
			err = ErrInvalid
		}
	}
	if err == nil {
		for i := len(directories) - 1; i >= 0; i-- {
			if syncDirectory(s.cfg.Store+string(os.PathSeparator)+release+string(os.PathSeparator)+directories[i]) != nil {
				err = ErrUnavailable
				break
			}
		}
	}
	if err == nil && files == 0 {
		err = ErrInvalid
	}
	return size, files, err
}
