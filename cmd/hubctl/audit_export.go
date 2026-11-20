package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/shared/locale"
)

const (
	auditExportBatchRecords = 100
	auditExportMaxBodyBytes = 256 * 1024
	auditExportResponseMax  = 64 * 1024
)

var auditExportKindPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type auditExportConfig struct {
	dir         string
	environment string
	principal   string
	destination string
	token       string
	kinds       []string
	kindSet     map[string]bool
	once        bool
	poll        time.Duration
	client      *http.Client
}

type auditExportEnvelope struct {
	Schema int           `json:"schema"`
	Events []audit.Event `json:"events"`
}

func runAuditExport(ctx context.Context, lang locale.Lang, args []string) error {
	usage := locale.T(lang, "hubctl.audit-export-usage")
	f := flag.NewFlagSet("audit export", flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	environment := f.String("env", "prod", "deployment environment recorded in export control audit")
	webhook := f.String("webhook", "", "bounded HTTPS webhook destination")
	tokenEnv := f.String("token-env", "", "environment variable holding the sink bearer token")
	kindsCSV := f.String("kinds", "", "optional comma-separated audit event kinds")
	once := f.Bool("once", false, "export currently available records and exit")
	poll := f.Duration("poll", time.Second, "idle ledger poll interval")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *dir == "" || *webhook == "" {
		return errors.New(usage)
	}
	dirAbs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	cfg, err := auditExportConfiguration(dirAbs, *environment, *webhook, *tokenEnv, *kindsCSV, *once, *poll)
	if err != nil {
		return fmt.Errorf("%s: %w", locale.T(lang, "hubctl.audit-export-invalid"), err)
	}
	return runAuditExportLoop(ctx, cfg)
}

func auditExportConfiguration(dir, environment, destination, tokenEnv, kindsCSV string, once bool, poll time.Duration) (auditExportConfig, error) {
	cfg := auditExportConfig{dir: dir, environment: environment, principal: filepath.Base(filepath.Clean(dir)), once: once, poll: poll}
	if environment != "dev" && environment != "prod" || cfg.principal == "." || cfg.principal == string(filepath.Separator) {
		return cfg, fmt.Errorf("invalid environment or deployment directory")
	}
	if poll < 100*time.Millisecond || poll > time.Minute {
		return cfg, fmt.Errorf("poll must be between 100ms and 1m")
	}
	u, err := url.ParseRequestURI(destination)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return cfg, fmt.Errorf("invalid webhook URL")
	}
	loopback := auditExportLoopback(u.Hostname())
	if u.Scheme != "https" && !loopback {
		return cfg, fmt.Errorf("webhook outside loopback requires HTTPS")
	}
	if !loopback && tokenEnv == "" {
		return cfg, fmt.Errorf("webhook outside loopback requires --token-env")
	}
	if tokenEnv != "" {
		cfg.token = strings.TrimSpace(os.Getenv(tokenEnv))
		if cfg.token == "" {
			return cfg, fmt.Errorf("token environment variable is empty")
		}
	}
	cfg.destination = destination
	kinds, kindSet, err := parseAuditExportKinds(kindsCSV)
	if err != nil {
		return cfg, err
	}
	cfg.kinds, cfg.kindSet = kinds, kindSet
	cfg.client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return cfg, nil
}

func auditExportLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && addr.IsLoopback()
}

func parseAuditExportKinds(value string) ([]string, map[string]bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil, nil
	}
	kinds := []string{}
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		kind := strings.TrimSpace(part)
		if !auditExportKindPattern.MatchString(kind) || kind == "audit-export" || seen[kind] {
			return nil, nil, fmt.Errorf("invalid audit export kind %q", kind)
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds, seen, nil
}

func runAuditExportLoop(ctx context.Context, cfg auditExportConfig) error {
	degraded := false
	backoff := 250 * time.Millisecond
	for {
		sent, err := auditExportAll(ctx, cfg)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if errors.Is(err, audit.ErrInvalid) || errors.Is(err, audit.ErrFull) {
				_ = auditExportStatus(cfg, "cursor_error")
				return err
			}
			if !degraded {
				if statusErr := auditExportStatus(cfg, "degraded"); statusErr != nil {
					return statusErr
				}
				degraded = true
			}
			if cfg.once {
				return err
			}
			if waitErr := auditExportSleep(ctx, backoff); waitErr != nil {
				return waitErr
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		if degraded {
			if err := auditExportStatus(cfg, "recovered"); err != nil {
				return err
			}
			degraded = false
		}
		backoff = 250 * time.Millisecond
		if cfg.once {
			return nil
		}
		if sent > 0 {
			continue
		}
		if err := auditExportSleep(ctx, cfg.poll); err != nil {
			return err
		}
	}
}

func auditExportSleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func auditExportAll(ctx context.Context, cfg auditExportConfig) (int, error) {
	ledgerPath := filepath.Join(cfg.dir, "runtime", "toolhub", "audit.jsonl")
	ledger, err := audit.Open(ledgerPath)
	if err != nil {
		return 0, err
	}
	cursorPath := audit.ExportCursorPath(ledgerPath)
	after, err := audit.LoadExportCursor(cursorPath, cfg.destination, cfg.kinds)
	if err != nil {
		return 0, err
	}
	sent := 0
	for {
		page, err := ledger.ExportPage(after, auditExportBatchRecords, cfg.kindSet)
		if err != nil {
			return sent, err
		}
		if len(page.Records) == 0 {
			return sent, nil
		}
		for start := 0; start < len(page.Records); {
			records := auditExportBatch(page.Records[start:])
			if len(records) == 0 {
				return sent, fmt.Errorf("%w: audit export batch", audit.ErrInvalid)
			}
			if err := deliverAuditBatch(ctx, cfg, records); err != nil {
				return sent, err
			}
			after = records[len(records)-1].Index
			if err := audit.StoreExportCursor(cursorPath, cfg.destination, cfg.kinds, after); err != nil {
				return sent, err
			}
			sent += len(records)
			start += len(records)
		}
	}
}

func auditExportBatch(records []audit.ExportRecord) []audit.ExportRecord {
	events := make([]audit.Event, 0, len(records))
	count := 0
	for _, record := range records {
		candidate := append(events, record.Event)
		body, err := json.Marshal(auditExportEnvelope{Schema: 1, Events: candidate})
		if err != nil || len(body) > auditExportMaxBodyBytes {
			break
		}
		events = candidate
		count++
	}
	return records[:count]
}

func deliverAuditBatch(ctx context.Context, cfg auditExportConfig, records []audit.ExportRecord) error {
	events := make([]audit.Event, 0, len(records))
	for _, record := range records {
		events = append(events, record.Event)
	}
	body, err := json.Marshal(auditExportEnvelope{Schema: 1, Events: events})
	if err != nil {
		return err
	}
	if len(body) > auditExportMaxBodyBytes {
		return fmt.Errorf("%w: audit export request size", audit.ErrInvalid)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.destination, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.token)
	}
	resp, err := cfg.client.Do(req)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, auditExportResponseMax))
	closeErr := resp.Body.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("audit sink returned %s", resp.Status)
	}
	return nil
}

func auditExportStatus(cfg auditExportConfig, outcome string) error {
	ledger, err := audit.Open(filepath.Join(cfg.dir, "runtime", "toolhub", "audit.jsonl"))
	if err != nil {
		return err
	}
	event := audit.NewEvent("audit-export", cfg.principal, outcome)
	event.Environment = cfg.environment
	event.OperationID = event.EventID
	event.CorrelationID = event.EventID
	event.Phase = "decision"
	event.Action = "export"
	event.Authorization = "allow"
	event.Execution = "not_started"
	event.Delivery = "not_applicable"
	event.Name = "sink:" + audit.ExportDestinationKey(cfg.destination, cfg.kinds)[:12]
	return ledger.Append(event)
}
