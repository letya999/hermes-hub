package communication

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SpoolImportReport summarizes an offline record import: how many record
// files landed in spool.db and where it lives.
type SpoolImportReport struct {
	Records int    `json:"records"`
	Bytes   int64  `json:"bytes"`
	DBPath  string `json:"db_path"`
}

// SpoolActivateReport summarizes the cutover: verified row count, the
// marker written and how many record files retired into substrate-backup/.
type SpoolActivateReport struct {
	Records    int    `json:"records"`
	Retired    int    `json:"retired"`
	MarkerPath string `json:"marker_path"`
	BackupRoot string `json:"backup_root"`
}

// recordFiles lists every record-classified regular file under root. The
// classifier keeps payloads, scratch, backups and migration artifacts out —
// a symlink is never a regular file, so foreign content stays unimported.
func recordFiles(root string) ([]string, error) {
	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() || !spoolRecordPath(root, path) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	return paths, err
}

// ImportSpoolRecords copies every record file under root into spool.db rows,
// preserving each file's mtime so retention aging sees the same record ages
// after the switch. The source files stay in place until activation retires
// them.
func ImportSpoolRecords(root string) (SpoolImportReport, error) {
	var report SpoolImportReport
	release, err := LockSpool(root)
	if err != nil {
		return report, err
	}
	defer release()
	fsys, err := openSpoolDB(root, false)
	if err != nil {
		return report, err
	}
	defer fsys.Close()
	paths, err := recordFiles(root)
	if err != nil {
		return report, err
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return report, err
		}
		body, err := os.ReadFile(path) // #nosec G304 -- operator-selected spool root
		if err != nil {
			return report, err
		}
		if err := fsys.WriteFile(path, body); err != nil {
			return report, fmt.Errorf("import %s: %w", path, err)
		}
		if err := fsys.Chtimes(path, time.Now(), info.ModTime()); err != nil {
			return report, err
		}
		report.Records++
		report.Bytes += int64(len(body))
	}
	report.DBPath = filepath.Join(root, "spool.db")
	return report, nil
}

// ActivateSpoolRecords flips the spool to the row backend: every record file
// must have a byte-identical row first, the marker is written, then the
// retired files move under substrate-backup/ where no record scan can see
// them. A crash mid-retire leaves duplicates the union listing dedupes.
func ActivateSpoolRecords(root string) (SpoolActivateReport, error) {
	var report SpoolActivateReport
	release, err := LockSpool(root)
	if err != nil {
		return report, err
	}
	defer release()
	fsys, err := openSpoolDB(root, true)
	if err != nil {
		return report, fmt.Errorf("import spool.db before activation: %w", err)
	}
	defer fsys.Close()
	paths, err := recordFiles(root)
	if err != nil {
		return report, err
	}
	for _, path := range paths {
		if _, err := fsys.ReadFile(path); err != nil {
			return report, fmt.Errorf("record %s missing from spool.db: %w", path, err)
		}
	}
	report.Records = len(paths)
	report.MarkerPath = filepath.Join(root, "records.activated")
	marker := fmt.Sprintf("{\"version\":1,\"records\":%d,\"activated_at\":%q}\n", report.Records, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(report.MarkerPath, []byte(marker), 0600); err != nil {
		return report, err
	}
	backup := filepath.Join(root, "substrate-backup")
	for _, path := range paths {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return report, err
		}
		dst := filepath.Join(backup, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return report, err
		}
		if err := os.Rename(path, dst); err != nil {
			return report, err
		}
		report.Retired++
	}
	report.BackupRoot = backup
	return report, nil
}
