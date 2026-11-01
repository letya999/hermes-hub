package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

// SpoolSubstrateReport records a verified filesystem-to-spool.db import or
// its activation: record counts, byte volume and where the manifest and the
// retired backup live.
type SpoolSubstrateReport struct {
	SpoolDir     string    `json:"spool_dir"`
	DBPath       string    `json:"db_path"`
	BackupRoot   string    `json:"backup_root,omitempty"`
	MarkerPath   string    `json:"marker_path,omitempty"`
	ManifestPath string    `json:"manifest_path"`
	Records      int       `json:"records"`
	Bytes        int64     `json:"bytes"`
	Retired      int       `json:"retired,omitempty"`
	MigratedAt   time.Time `json:"migrated_at"`
}

// MigrateSpoolSubstrate copies every record-classified spool file into
// spool.db rows under the writer fence, then leaves a verified manifest so
// activation can prove the import ran before flipping.
func MigrateSpoolSubstrate(spoolDir string) (SpoolSubstrateReport, error) {
	var report SpoolSubstrateReport
	imported, err := communication.ImportSpoolRecords(spoolDir)
	if err != nil {
		return report, err
	}
	report = SpoolSubstrateReport{
		SpoolDir:     spoolDir,
		DBPath:       imported.DBPath,
		ManifestPath: imported.DBPath + ".manifest.json",
		Records:      imported.Records,
		Bytes:        imported.Bytes,
		MigratedAt:   time.Now().UTC(),
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	if err := writePrivateFile(report.ManifestPath, append(body, '\n')); err != nil {
		return report, err
	}
	return report, nil
}

// ActivateSpoolSubstrate flips the spool to spool.db once the verified
// import's manifest is present, retiring the record files under
// substrate-backup/.
func ActivateSpoolSubstrate(spoolDir string) (SpoolSubstrateReport, error) {
	var report SpoolSubstrateReport
	manifestPath := filepath.Join(spoolDir, "spool.db.manifest.json")
	if _, err := os.Lstat(manifestPath); err != nil {
		return report, fmt.Errorf("verified migration manifest missing: %w", err)
	}
	activated, err := communication.ActivateSpoolRecords(spoolDir)
	if err != nil {
		return report, err
	}
	report = SpoolSubstrateReport{
		SpoolDir:     spoolDir,
		MarkerPath:   activated.MarkerPath,
		BackupRoot:   activated.BackupRoot,
		ManifestPath: manifestPath,
		Records:      activated.Records,
		Retired:      activated.Retired,
		MigratedAt:   time.Now().UTC(),
	}
	return report, nil
}
