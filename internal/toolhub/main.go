package toolhub

import (
	"context"
	"errors"
	"fmt"
	identitycontract "github.com/letya999/hermes-hub/contracts/identity/v1"
	taskcontract "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/shared/locale"
)

var removalRetryInterval = 30 * time.Second
var reconcileInterval = 30 * time.Second

// Main runs the agent zone with the legacy ToolHub environment contract.
func Main(args []string) error {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(args) != 0 {
		return errors.New(locale.T(locale.Fallback(), "hub.agent-usage"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Serve(ctx)
}

func removalRetryLoop(ctx context.Context, interval time.Duration, resume func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := resume(ctx); err != nil {
				log.Printf("ToolHub removal reconciliation: %v", err)
			}
		}
	}
}

// Serve runs the agent zone until cancellation and joins its background work.
func Serve(ctx context.Context) error {
	storePath := os.Getenv("HUB_TOOLHUB_STORE")
	if storePath == "" {
		return errors.New("HUB_TOOLHUB_STORE is required")
	}
	config, err := EndpointConfigFromEnv()
	if err != nil {
		return err
	}
	store, err := Load(storePath)
	if err != nil {
		return fmt.Errorf("load ToolHub store: %w", err)
	}
	if witness := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_IDENTITY_WITNESS")); witness != "" {
		if err := store.VerifyInstallationWitness(witness); err != nil {
			return fmt.Errorf("ToolHub installation witness: %w", err)
		}
	}
	// The PDP's vocabulary is the pinned catalog release the image ships:
	// manifest digests must verify before any right is enforceable. A host
	// binary or an older dev image without the baked tree degrades to a
	// fail-closed PDP — rights deny rather than leak.
	if docs, err := catalog.LoadPinned(); err == nil {
		store.RightsDoc = docs.Rights
	} else {
		fmt.Fprintln(os.Stderr, "hub-toolhub: rights catalog unavailable, PDP fails closed:", err)
	}
	handler, err := NewEndpointHandler(config, store)
	if err != nil {
		return err
	}
	if root := os.Getenv("HUB_AUTOMATION_ROOT"); root != "" {
		if err := handler.(interface {
			ConfigureResearch(string, string, string, string) error
		}).ConfigureResearch(filepath.Join(root, "research"), os.Getenv("HUB_AUTOMATION_JOB_URL"), automationServiceToken(), os.Getenv("HUB_AUTOMATION_JOB_PATH")); err != nil {
			return err
		}
	}
	controlHandler := http.Handler(handler)
	var automationTick func(context.Context, time.Time) error
	var automationCompact func(time.Time, time.Duration) error
	if os.Getenv("HUB_AUTOMATION_ROOT") != "" {
		projection, ok := handler.(AutomationProjection)
		if !ok {
			return errors.New("automation: ToolHub projection unavailable")
		}
		var overloaded func() bool
		if og, ok := handler.(interface{ Overloaded() bool }); ok {
			overloaded = og.Overloaded
		}
		host, err := NewAutomationHost(ctx, projection, store, overloaded)
		if err != nil {
			return err
		}
		defer host.Close()
		handler.(*projectionEndpoint).gateway.pauseSchedules = host.PauseAllForPrincipal
		handler.(*projectionEndpoint).gateway.authorizeAssignmentRun = host.AuthorizeAssignmentRun
		automationTick = host.Tick
		automationCompact = host.CompactOccurrences
		mux := http.NewServeMux()
		automationHandler := host.ControlHandler(automationServiceToken())
		if origin, key := runVerificationPeer(); origin != "" {
			automationHandler = host.SignedControlHandler(identitycontract.NewHTTPClient(origin, key, nil))
			mux.Handle(taskcontract.AssignmentAuthorityPath, host.AssignmentRunHandler(runAuthorityFromEnv(), key))
		}
		mux.Handle("/v1/automation/", automationHandler)
		mux.Handle("/", handler)
		controlHandler = mux
	}
	ctx, stop := context.WithCancel(ctx)
	var background sync.WaitGroup
	defer func() { stop(); background.Wait() }()
	runBackground := func(work func()) {
		background.Add(1)
		go func() { defer background.Done(); work() }()
	}
	if automationTick != nil {
		runBackground(func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			var lastCompact time.Time
			compactInterval := time.Hour
			for {
				now := time.Now().UTC()
				if err := automationTick(ctx, now); err != nil && ctx.Err() == nil {
					log.Print("ToolHub automation tick pending")
				}
				if err := handler.(interface{ ResearchTick(context.Context) error }).ResearchTick(ctx); err != nil && ctx.Err() == nil {
					log.Print("ToolHub research tick pending")
				}
				if now.Sub(lastCompact) >= compactInterval {
					lastCompact = now
					if err := automationCompact(now, 72*time.Hour); err == nil {
						compactInterval = time.Hour
					} else if ctx.Err() == nil {
						compactInterval = 5 * time.Minute
						log.Print("ToolHub occurrence compaction pending")
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		})
	}
	server := &http.Server{Addr: config.Listen, Handler: diagnostics.HTTP("toolhub", controlHandler), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}
	publicServer, publicLn, err := publicServerFor(handler)
	if err != nil {
		return err
	}
	if publicServer != nil {
		defer func() { _ = publicServer.Shutdown(context.Background()) }()
		go func() {
			if err := publicServer.Serve(publicLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "hub-toolhub public listener:", err)
			}
		}()
	}
	remover := handler.(interface{ ResumeRemovals(context.Context) error })
	// A transient startup failure is retried and reported by the periodic loop.
	_ = remover.ResumeRemovals(ctx)
	runBackground(func() { removalRetryLoop(ctx, removalRetryInterval, remover.ResumeRemovals) })
	reconciler := handler.(interface{ Reconcile(context.Context) error })
	runBackground(func() { removalRetryLoop(ctx, reconcileInterval, reconciler.Reconcile) })
	lifecycle := handler.(interface{ ReconcileOnboardings(context.Context) })
	runBackground(func() {
		// Every lifecycle predicate works at >=30s granularity
		// (ObservedAt age, AdmissionNextAttempt, NoticeNextAttempt), so a
		// 1s tick only manufactured broker-cursor churn — CHG-0308 moved the
		// cursor itself to the sidecar; this tick only schedules passes.
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			lifecycle.ReconcileOnboardings(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	if dir := os.Getenv("HUB_DIAGNOSTICS_DIR"); dir != "" {
		scope := diagnostics.Scope{
			Project:  os.Getenv("HUB_DIAGNOSTICS_PROJECT"),
			AgentNet: os.Getenv("HUB_DIAGNOSTICS_AGENT_NET"),
			Owner:    os.Getenv("HUB_DIAGNOSTICS_OWNER"),
		}
		runBackground(func() {
			diagnostics.Follow(ctx, filepath.Join(dir, "hermes-diagnostics.txt"), nil, 15*time.Second, scope)
		})
	}
	refresher := handler.(interface{ RefreshProjection() error })
	restartNotify := api.RuntimeRestartNotifierFromEnv()
	if restartNotify != nil {
		restartNotify.State = store
	}
	runBackground(func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		pending := false
		notify := func() {
			if restartNotify == nil {
				return
			}
			if targets, err := store.ProjectionRevisions(); err == nil {
				if err := restartNotify.Notify(targets); err != nil {
					fmt.Fprintln(os.Stderr, "ToolHub restart notify:", err)
				}
			}
		}
		// A durable pending revision is reported at startup, not one cadence
		// later: the revision snapshot reloads the store file, so it runs on
		// a slower cadence than the projection ticker.
		notify()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := refresher.RefreshProjection(); err != nil {
					if !pending {
						fmt.Fprintln(os.Stderr, "ToolHub projection refresh pending")
					}
					pending = true
				} else {
					pending = false
				}
				// list_changed only reaches clients holding an open stream;
				// the restart marker guarantees the next session reload.
				notify()
			}
		}
	})
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// publicServerFor builds the opt-in public listener (HUB_PUBLIC_LISTEN): it
// serves only the handler's human-action surface — /credentials, /confirm,
// /remove, /connect, /oauth/callback — never control or gateway APIs. With no
// listen address configured no second server exists.
func publicServerFor(handler http.Handler) (*http.Server, net.Listener, error) {
	addr := os.Getenv("HUB_PUBLIC_LISTEN")
	if addr == "" {
		return nil, nil, nil
	}
	public, ok := handler.(interface{ PublicHandler() http.Handler })
	if !ok {
		return nil, nil, errors.New("endpoint handler has no public surface")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	return &http.Server{Addr: addr, Handler: public.PublicHandler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 2 * time.Minute}, ln, nil
}
