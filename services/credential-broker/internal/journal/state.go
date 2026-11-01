package journal

// StateWrite upserts one current-state row atomically with the journal record
// it was derived from. Kind and ID stay opaque to the journal — domain
// decoding is the caller's job.
type StateWrite struct {
	Kind string
	ID   string
	Body []byte
}

// AuditWrite appends one audit row atomically with its journal record. The
// principal/context columns exist only for scoped pagination; the full event
// lives in Body.
type AuditWrite struct {
	Kind      string
	Principal string
	Context   string
	Body      []byte
}

// AuxWrites carries the normalized-table writes committed in the same
// transaction as a journal record. A nil AuxWrites appends the bare record
// (which intentionally leaves the normalized marker behind the head).
type AuxWrites struct {
	States []StateWrite
	Audit  *AuditWrite
}

// StateRow is one current-state row from the normalized tables.
type StateRow struct {
	Kind string
	ID   string
	Seq  uint64
	Body []byte
}

// AuditRow is one audit-table row.
type AuditRow struct {
	Seq       uint64
	Kind      string
	Principal string
	Context   string
	Body      []byte
}

// AuditScope filters AuditPage results: rows matching Broadcast are always
// visible; otherwise Context must match and either Principal must match or
// AnyPrincipal must hold (context-manager reads).
type AuditScope struct {
	Broadcast    string
	Principal    string
	Context      string
	AnyPrincipal bool
}

// NormalizeFunc derives normalized-table writes from one journaled record.
// The domain decoder lives in the caller; the journal stays record-blind.
type NormalizeFunc func(Record) ([]StateWrite, *AuditWrite, error)

// AuditRetention is the hot-audit policy wired onto an open Journal:
// HotMaxBytes caps the byte size of live audit rows (always bounded, no
// unlimited value), ArchiveDir receives sealed archives, and Destructive is
// the explicit opt-in that lets a verified archive's source rows be deleted.
// Without Destructive retention only exports — filling the cap stops
// admission visibly instead of hiding deletes (SPEC-0087 R5.3).
type AuditRetention struct {
	HotMaxBytes int64
	ArchiveDir  string
	Destructive bool
	// ArchiveMaxBytes and ArchiveMaxAgeDays bound the archive directory
	// itself: the maintenance tick permanently deletes the oldest verified
	// archive pairs over the budget — except the newest pair, which carries
	// the provenance link to the current retained floor (SPEC-0087 R5.2).
	// Zero means unbounded.
	ArchiveMaxBytes   int64
	ArchiveMaxAgeDays int
}

// RetainReport describes one audit-retention pass: the exported range, the
// archive artifact, whether the source was pruned, and whether the hot table
// is still over cap (export-only mode leaves it full).
type RetainReport struct {
	Exported bool
	Archive  string
	SeqLo    uint64
	SeqHi    uint64
	Rows     int
	Bytes    int64
	Pruned   bool `json:"pruned"`
	HotBytes int64
	OverCap  bool
}

// RecordsRetention is the history-rotation policy wired onto an open
// Journal: ArchiveDir receives sealed records archives and Destructive is
// the explicit opt-in that lets a verified archive's source prefix be
// deleted and the file compacted (SPEC-0087 R5.2/R5.3). Without Destructive
// rotation only exports — the ledger still stops admission visibly at its
// byte cap instead of silently losing history.
type RecordsRetention struct {
	ArchiveDir  string
	Destructive bool
	// ArchiveMaxBytes and ArchiveMaxAgeDays bound the archive directory
	// itself: the maintenance tick permanently deletes the oldest verified
	// archive pairs over the budget — except the newest pair, which carries
	// the provenance link to the current retained floor (SPEC-0087 R5.2).
	// Zero means unbounded.
	ArchiveMaxBytes   int64
	ArchiveMaxAgeDays int
}

// MaintenanceReport describes one high-water maintenance tick: each retain
// pass that ran (audit / records) and the last error observed. LastError is
// persisted on the journal so a failing cleanup stays visible across
// restarts instead of repeating silently (SPEC-0087 R5.2).
type MaintenanceReport struct {
	Audit     *RetainReport
	Records   *RetainReport
	LastError string
	// Marker names the persisted outcome marker written this tick; empty
	// when the marker was already in the target state — repeats do not
	// rewrite meta at sustained high water.
	Marker string
}

func sqlValidateAux(aux *AuxWrites) error {
	if aux == nil {
		return nil
	}
	for _, s := range aux.States {
		if s.Kind == "" || s.ID == "" || len(s.Body) == 0 || len(s.Body) > MaxRecord {
			return ErrUnavailable
		}
	}
	if a := aux.Audit; a != nil && (a.Kind == "" || len(a.Body) == 0 || len(a.Body) > MaxRecord) {
		return ErrUnavailable
	}
	return nil
}
