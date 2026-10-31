package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/storagebudget"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// storageOwnerStatus is the allowlisted per-owner projection of owned
// storage: aggregate bytes, file counts, limits and a safe status word.
// Paths, object names and raw errors never leave the process.
type storageOwnerStatus struct {
	Owner        string `json:"owner"`
	Status       string `json:"status"`
	LogicalBytes int64  `json:"logical_bytes"`
	Files        int    `json:"files"`
	LimitBytes   int64  `json:"limit_bytes"`
	LimitFiles   int    `json:"limit_files"`
	Complete     bool   `json:"complete"`
}

type storageStatusReport struct {
	Owners []storageOwnerStatus `json:"owners"`
}

func runStorageStatus(w io.Writer, spoolDir, toolhubStore string) error {
	report := storageStatusReport{Owners: []storageOwnerStatus{
		spoolOwnerStatus(spoolDir),
		toolhubOwnerStatus(toolhubStore),
	}}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func ownerStatus(owner string, snap storagebudget.Snapshot, limits storagebudget.Limits, err error) storageOwnerStatus {
	st := storageOwnerStatus{Owner: owner, Status: "ok"}
	if err != nil {
		st.Status = "unavailable"
		return st
	}
	st.LogicalBytes, st.Files, st.Complete = snap.LogicalBytes, snap.Files, snap.Complete
	st.LimitBytes, st.LimitFiles = limits.Bytes, limits.Files
	return st
}

func spoolOwnerStatus(dir string) storageOwnerStatus {
	spool, err := communication.InspectSpool(dir)
	if err != nil {
		return ownerStatus("communication-spool", storagebudget.Snapshot{}, storagebudget.Limits{}, err)
	}
	snap, limits, err := spool.StorageStatus()
	return ownerStatus("communication-spool", snap, limits, err)
}

func toolhubOwnerStatus(storePath string) storageOwnerStatus {
	snap, limits, err := toolhub.StorageStatus(storePath)
	return ownerStatus("toolhub-state", snap, limits, err)
}

func runStorageCleanup(w io.Writer, spoolDir string, apply bool, planDigest string, retention time.Duration) error {
	if strings.TrimSpace(spoolDir) == "" {
		return errors.New("spool directory is required")
	}
	if !apply {
		spool, err := communication.InspectSpool(spoolDir)
		if err != nil {
			return err
		}
		plan, err := spool.StoragePlan()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}
	unlock, err := communication.LockSpool(spoolDir)
	if err != nil {
		return err
	}
	defer unlock()
	spool, err := communication.OpenSpool(spoolDir)
	if err != nil {
		return err
	}
	fresh, err := spool.StoragePlan()
	if err != nil {
		return err
	}
	if fresh.Digest != planDigest {
		return communication.ErrStalePlan
	}
	return spool.Compact(time.Now(), retention, communication.CompactLimits{})
}
