package sshcap

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
)

func TestAuditFailureBeforeEffectPreventsCall(t *testing.T) {
	var calls atomic.Int32
	f := openFixture(t, sshdOpts{exec: func(string) (string, string, int) { calls.Add(1); return "fixture", "", 0 }}, Grants{}, "    commands: [uptime]\n", "")
	if err := os.Remove(f.audit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Exec(context.Background(), "box", "uptime"); err == nil {
		t.Error("execution admitted without audit")
	}
	if calls.Load() != 0 {
		t.Fatalf("remote command effects=%d", calls.Load())
	}
}

func TestPostEffectAuditFailureNeverRepeatsAction(t *testing.T) {
	var calls atomic.Int32
	paths := make(chan string, 1)
	faults := make(chan error, 1)
	f := openFixture(t, sshdOpts{exec: func(string) (string, string, int) {
		if calls.Add(1) > 1 {
			return "unexpected replay", "", 0
		}
		path := <-paths
		err := os.Rename(path, path+".saved")
		if err == nil {
			err = os.Mkdir(path, 0700)
		}
		faults <- err
		return "private fixture output", "", 0
	}}, Grants{}, "    commands: [uptime]\n", "")
	paths <- f.audit
	if result, err := f.svc.Exec(context.Background(), "box", "uptime"); !errors.Is(err, audit.ErrUncertain) || result != nil {
		t.Errorf("post-effect=%+v %v", result, err)
	}
	if err := <-faults; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.audit); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.audit+".saved", f.audit); err != nil {
		t.Fatal(err)
	}
	// The durable target fence must prevent a second command after recovery.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := f.svc.Exec(ctx, "box", " uptime "); !errors.Is(err, audit.ErrUncertain) {
		t.Errorf("replay not fenced: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("remote command effects=%d", calls.Load())
	}
}

func TestFileEffectsRequireAuditAndKeepPayloadOutOfLedger(t *testing.T) {
	fs := newMemFS()
	fs.files["/tmp/value"] = []byte("private original")
	f := openFixture(t, sshdOpts{fs: fs}, Grants{Write: true}, "    paths: [\"/tmp/*\"]\n    write_paths: [\"/tmp/*\"]\n", "")
	if err := os.Remove(f.audit); err != nil {
		t.Fatal(err)
	}
	if result, err := f.svc.Read(context.Background(), "box", "/tmp/value"); !errors.Is(err, audit.ErrWrite) || result != nil {
		t.Fatalf("read=%+v %v", result, err)
	}
	if result, err := f.svc.Write(context.Background(), "box", "/tmp/value", "private replacement"); !errors.Is(err, audit.ErrWrite) || result != nil {
		t.Fatalf("write=%+v %v", result, err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.reads != 0 || fs.writes != 0 || string(fs.files["/tmp/value"]) != "private original" {
		t.Fatalf("unaudited access: reads=%d writes=%d", fs.reads, fs.writes)
	}
}

func TestWriteCompletionFailureDoesNotRepeatRename(t *testing.T) {
	fs := newMemFS()
	faults := make(chan error, 1)
	f := openFixture(t, sshdOpts{fs: fs}, Grants{Write: true}, "    write_paths: [\"/tmp/*\"]\n", "")
	fs.mu.Lock()
	fs.afterRename = func() {
		err := os.Rename(f.audit, f.audit+".saved")
		if err == nil {
			err = os.Mkdir(f.audit, 0700)
		}
		faults <- err
	}
	fs.mu.Unlock()
	if result, err := f.svc.Write(context.Background(), "box", "/tmp/target", "fixture private bytes"); !errors.Is(err, audit.ErrUncertain) || result != nil {
		t.Fatalf("write=%+v %v", result, err)
	}
	if err := <-faults; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.audit); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.audit+".saved", f.audit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Write(context.Background(), "box", "/tmp/target", "different fixture"); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("write replay not fenced: %v", err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.writes != 1 || string(fs.files["/tmp/target"]) != "fixture private bytes" {
		t.Fatalf("write effects=%d", fs.writes)
	}
	body, err := os.ReadFile(f.audit)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"fixture private bytes", "/tmp/target"} {
		if strings.Contains(string(body), private) {
			t.Fatal("file payload/path in audit")
		}
	}
}

func TestSessionAdmissionFailureCreatesNoSession(t *testing.T) {
	f := openFixture(t, sshdOpts{shell: true, directTCPIP: true}, Grants{Shell: true, Tunnel: true}, "    tunnels:\n      db: {remote_host: 127.0.0.1, remote_port: 5432}\n", "")
	if err := os.Remove(f.audit); err != nil {
		t.Fatal(err)
	}
	if result, err := f.svc.ShellOpen(context.Background(), "box"); !errors.Is(err, audit.ErrWrite) || result != nil {
		t.Fatalf("shell=%+v %v", result, err)
	}
	if result, err := f.svc.TunnelOpen(context.Background(), "box", "db"); !errors.Is(err, audit.ErrWrite) || result != nil {
		t.Fatalf("tunnel=%+v %v", result, err)
	}
	if len(f.svc.shells) != 0 || len(f.svc.tunnels) != 0 {
		t.Fatal("unaudited sessions retained")
	}
}

func TestAuditFailureDoesNotPreventSessionClose(t *testing.T) {
	f := openFixture(t, sshdOpts{shell: true, directTCPIP: true}, Grants{Shell: true, Tunnel: true}, "    tunnels:\n      db: {remote_host: 127.0.0.1, remote_port: 5432}\n", "")
	sh, err := f.svc.ShellOpen(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	tun, err := f.svc.TunnelOpen(context.Background(), "box", "db")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.audit); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ShellClose(sh.ID); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("shell cleanup=%v", err)
	}
	if err := f.svc.TunnelClose(tun.ID); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("tunnel cleanup=%v", err)
	}
	if len(f.svc.shells) != 0 || len(f.svc.tunnels) != 0 {
		t.Fatal("audit failure prevented transport cleanup")
	}
}

func TestCommandWhitespaceDoesNotRequestSudo(t *testing.T) {
	seen := make(chan string, 1)
	f := openFixture(t, sshdOpts{exec: func(command string) (string, string, int) { seen <- command; return "fixture", "", 0 }}, Grants{}, "    commands: [uptime]\n    sudo: passwordless\n", "")
	if _, err := f.svc.Exec(context.Background(), "box", " uptime "); err != nil {
		t.Fatal(err)
	}
	if command := <-seen; command != "uptime" {
		t.Fatalf("whitespace elevated command to %q", command)
	}
}

func TestSessionCleanupUsesOpeningReservation(t *testing.T) {
	f := openFixture(t, sshdOpts{shell: true, directTCPIP: true}, Grants{Shell: true, Tunnel: true}, "    tunnels:\n      db: {remote_host: 127.0.0.1, remote_port: 5432}\n", "")
	sh, err := f.svc.ShellOpen(context.Background(), "box")
	if err != nil {
		t.Fatal(err)
	}
	tun, err := f.svc.TunnelOpen(context.Background(), "box", "db")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ShellClose(sh.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.TunnelClose(tun.ID); err != nil {
		t.Fatal(err)
	}
	ledger, err := audit.Open(f.audit)
	if err != nil {
		t.Fatal(err)
	}
	events, err := ledger.List("me")
	if err != nil {
		t.Fatal(err)
	}
	openings := map[string]string{}
	verified := 0
	for _, event := range events {
		if event.Phase == "admitted" && event.CleanupAction != "" {
			openings[event.OperationID] = event.CleanupAction
		}
		if event.Phase == "completed" && event.CleanupOf != "" && openings[event.CleanupOf] == event.Action {
			verified++
		}
	}
	if verified != 2 {
		t.Fatalf("bound lifecycle cleanup receipts=%d, want 2", verified)
	}
}
