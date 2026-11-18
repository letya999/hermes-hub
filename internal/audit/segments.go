package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
)

const (
	MaxRecordBytes = 16 * 1024
	MaxPageRecords = 1000
)

var (
	ErrFull = errors.New("audit capacity exhausted; export and operator recovery required")
	ErrPage = errors.New("audit result requires pagination")
)

type ledgerBounds struct {
	segment, total int64
	segments       int
	records        uint64
}

// No destructive retention policy is implied by these capacity limits.
var defaultBounds = ledgerBounds{1 << 20, 16 << 20, 16, 65536}

type Ledger struct {
	mu     sync.Mutex
	path   string
	bounds ledgerBounds
}

type Scope struct{ PrincipalID, ContextID, Environment string }
type Page struct {
	Events []Event
	Next   uint64
	More   bool
}

// ExportRecord is one ledger-sequence event plus the cursor position that
// acknowledges it. ExportPage is all-principals because the durable cursor is
// meaningful only in ledger append order.
type ExportRecord struct {
	Index uint64
	Event Event
}

type ExportPage struct {
	Records []ExportRecord
	Next    uint64
	More    bool
}

func Open(path string) (*Ledger, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: ledger path must be absolute", ErrInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: ledger directory", ErrInvalid)
	}
	l := &Ledger{path: path, bounds: defaultBounds}
	err = l.locked(func(root *os.Root) error {
		if _, err := regular(root, filepath.Base(path)); errors.Is(err, os.ErrNotExist) {
			// A missing first segment with surviving later segments is lost state.
			for i := 1; i < l.bounds.segments; i++ {
				if _, e := root.Lstat(l.segmentName(i)); !errors.Is(e, os.ErrNotExist) {
					return fmt.Errorf("%w: missing initial segment", ErrInvalid)
				}
			}
			f, err := root.OpenFile(filepath.Base(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			syncErr := f.Sync()
			closeErr := f.Close()
			if syncErr != nil {
				return syncErr
			}
			if closeErr != nil {
				return closeErr
			}
			return syncAuditDirectory(filepath.Dir(path))
		} else {
			return err
		}
	})
	if err != nil {
		return nil, err
	}
	return l, nil
}

func regular(root *os.Root, name string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: nonregular audit file", ErrInvalid)
	}
	return info, nil
}

func (l *Ledger) segmentName(i int) string {
	if i == 0 {
		return filepath.Base(l.path)
	}
	return fmt.Sprintf("%s.v1-%06d", filepath.Base(l.path), i)
}

func (l *Ledger) locked(fn func(*os.Root) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	root, err := os.OpenRoot(filepath.Dir(l.path))
	if err != nil {
		return err
	}
	defer root.Close()
	lockName := filepath.Base(l.path) + ".lock"
	if _, err := regular(root, lockName); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock := flock.New(l.path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Close()
	return fn(root)
}

type ledgerStats struct {
	bytes        int64
	records      uint64
	last         int
	lastBytes    int64
	scannedBytes int64
	operations   map[string]Event
	cleanups     map[string]string
}

// Scan validates every record before filtering, including foreign scopes. Work
// and allocations are bounded even for corrupt or oversized legacy input.
func (l *Ledger) scan(root *os.Root, visit func(Event, uint64)) (ledgerStats, error) {
	var stats ledgerStats
	missing := false
	for i := 0; i < l.bounds.segments; i++ {
		name := l.segmentName(i)
		info, err := regular(root, name)
		if errors.Is(err, os.ErrNotExist) && i > 0 {
			missing = true
			continue
		}
		if err != nil {
			return stats, err
		}
		if missing {
			return stats, fmt.Errorf("%w: audit segment gap", ErrInvalid)
		}
		stats.bytes += info.Size()
		if stats.bytes > l.bounds.total {
			return stats, ErrFull
		}
		stats.last, stats.lastBytes = i, info.Size()
		f, err := root.Open(name)
		if err != nil {
			return stats, err
		}
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			_ = f.Close()
			return stats, fmt.Errorf("%w: audit file changed", ErrInvalid)
		}
		err = l.scanFile(f, &stats, visit)
		closeErr := f.Close()
		if err != nil {
			return stats, err
		}
		if closeErr != nil {
			return stats, closeErr
		}
	}
	return stats, nil
}

func (l *Ledger) scanFile(f *os.File, stats *ledgerStats, visit func(Event, uint64)) error {
	reader := bufio.NewReaderSize(io.LimitReader(f, l.bounds.total+1), MaxRecordBytes+1)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil
		}
		if err != nil || len(line) > MaxRecordBytes {
			return fmt.Errorf("%w: partial or oversized audit record", ErrInvalid)
		}
		stats.scannedBytes += int64(len(line))
		if stats.scannedBytes > l.bounds.total {
			return ErrFull
		}
		var event Event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || validate(event) != nil || containsSensitive(line) {
			return fmt.Errorf("%w: corrupt audit record", ErrInvalid)
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("%w: trailing audit data", ErrInvalid)
		}
		if err := stats.trackOperation(event); err != nil {
			return fmt.Errorf("%w: operation history", ErrInvalid)
		}
		stats.records++
		if stats.records > l.bounds.records {
			return ErrFull
		}
		if visit != nil {
			visit(event, stats.records)
		}
	}
}

func (l *Ledger) Append(event Event) error {
	if l == nil {
		return fmt.Errorf("%w: nil ledger", ErrWrite)
	}
	if err := validate(event); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > MaxRecordBytes || containsSensitive(body) {
		return fmt.Errorf("%w: record size or forbidden material", ErrInvalid)
	}
	err = l.locked(func(root *os.Root) error {
		stats, err := l.scan(root, nil)
		if err != nil {
			return err
		}
		if err := stats.trackOperation(event); err != nil {
			return err
		}
		i := stats.last
		if stats.lastBytes+int64(len(body)) > l.bounds.segment {
			if stats.lastBytes == 0 {
				return ErrFull
			}
			i++
		}
		if i >= l.bounds.segments {
			return ErrFull
		}
		segmentBytes := stats.lastBytes + int64(len(body))
		if i > stats.last {
			segmentBytes = int64(len(body))
		}
		if err := l.completionReserve(stats, int64(len(body)), i, segmentBytes); err != nil {
			return err
		}
		flags := os.O_APPEND | os.O_WRONLY
		if i > stats.last {
			flags |= os.O_CREATE | os.O_EXCL
		}
		f, err := root.OpenFile(l.segmentName(i), flags, 0600)
		if err != nil {
			return err
		}
		n, err := f.Write(body)
		if err == nil && n != len(body) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return syncAuditDirectory(filepath.Dir(l.path))
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrWrite, err)
	}
	return nil
}

func (l *Ledger) ReadPage(scope Scope, after uint64, limit int) (Page, error) {
	var page Page
	if l == nil || scope.PrincipalID == "" || limit < 1 || limit > MaxPageRecords {
		return page, fmt.Errorf("%w: audit scope or page limit", ErrInvalid)
	}
	page.Events = make([]Event, 0, limit)
	err := l.locked(func(root *os.Root) error {
		stats, err := l.scan(root, func(event Event, index uint64) {
			if index <= after || event.PrincipalID != scope.PrincipalID || scope.ContextID != "" && event.ContextID != scope.ContextID || scope.Environment != "" && event.Environment != scope.Environment {
				return
			}
			if len(page.Events) == limit {
				page.More = true
				return
			}
			page.Events = append(page.Events, event)
			page.Next = index
		})
		if err != nil {
			return err
		}
		if after > stats.records {
			return fmt.Errorf("%w: future audit cursor", ErrInvalid)
		}
		if !page.More {
			page.Next = stats.records
		}
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	return page, nil
}

// ExportPage reads a bounded all-principals page in ledger order. Exporter
// control records are deliberately excluded so an export transition cannot
// recursively generate another export item.
func (l *Ledger) ExportPage(after uint64, limit int, kinds map[string]bool) (ExportPage, error) {
	var page ExportPage
	if l == nil || limit < 1 || limit > MaxPageRecords {
		return page, fmt.Errorf("%w: audit export page limit", ErrInvalid)
	}
	page.Records = make([]ExportRecord, 0, limit)
	err := l.locked(func(root *os.Root) error {
		stats, err := l.scan(root, func(event Event, index uint64) {
			if index <= after || event.Kind == "audit-export" || kinds != nil && !kinds[event.Kind] {
				return
			}
			if len(page.Records) == limit {
				page.More = true
				return
			}
			page.Records = append(page.Records, ExportRecord{Index: index, Event: event})
			page.Next = index
		})
		if err != nil {
			return err
		}
		if after > stats.records {
			return fmt.Errorf("%w: future audit cursor", ErrInvalid)
		}
		if !page.More {
			page.Next = stats.records
		}
		return nil
	})
	if err != nil {
		return ExportPage{}, err
	}
	return page, nil
}

// List preserves small callers and refuses silent truncation of larger histories.
func (l *Ledger) List(principal string) ([]Event, error) {
	page, err := l.ReadPage(Scope{PrincipalID: principal}, 0, MaxPageRecords)
	if err != nil {
		return nil, err
	}
	if page.More {
		return nil, ErrPage
	}
	return page.Events, nil
}
