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
