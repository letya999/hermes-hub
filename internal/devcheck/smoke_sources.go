//go:build integration

package devcheck

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

func supervisorDesiredSourcesSmoke(ctx context.Context, m *supervisor.Manager, binding supervisor.Binding, runner communication.HTTPRunner, template communication.Job, root, image, nativeAuth string, starts *atomic.Int32) error {
	request := hubruntime.ExecuteRequest{Envelope: template.Envelope, UserID: template.UserID, ActorID: template.ActorID, OrganizationID: template.OrganizationID, ScopeID: template.ScopeID}
	if err := m.SetPin(request, true); err != nil {
		return err
	}
	m.ReconcilePins(ctx)
	pinned, _, err := m.Status(binding)
	if err != nil || pinned.State != supervisor.Idle {
		return fmt.Errorf("real pin did not wake: %v", err)
	}
	m.ReconcileHealth(ctx, time.Now())
	pinned, _, err = m.Status(binding)
	if err != nil || pinned.RuntimeHealth != "running" || pinned.ConnectorHealth == "not_checked" {
		return fmt.Errorf("real layered health failed: process=%s hermes=%s connector=%s error=%v", pinned.RuntimeHealth, pinned.HermesReadiness, pinned.ConnectorHealth, err)
	}
	nativeID, err := smokeOwnedContainerID(ctx, m, pinned)
	if err != nil {
		return err
	}
	nativeHealth, err := hermesHTTP(ctx, func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }, nativeID, nativeAuth, http.MethodGet, "/health/detailed", "", "")
	var health struct {
		Readiness struct {
			Status string `json:"status"`
		} `json:"readiness"`
	}
	if err != nil || nativeHealth.status != 200 || json.Unmarshal(nativeHealth.body, &health) != nil {
		return fmt.Errorf("real native health comparison unavailable")
	}
	expected := "unavailable"
	switch health.Readiness.Status {
	case "ok":
		expected = "ready"
	case "degraded":
		expected = "degraded"
	}
	if pinned.HermesReadiness != expected {
		return fmt.Errorf("layered readiness %s differs from native observation %s (expected %s)", pinned.HermesReadiness, health.Readiness.Status, expected)
	}
	fmt.Printf("Real layered health passed: process running; native readiness %s; connections %s\n", health.Readiness.Status, pinned.ConnectorHealth)
	if err := supervisorOrphanSmoke(ctx, m, pinned, image); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(time.Hour)); err != nil {
		return err
	}
	kept, _, _ := m.Status(binding)
	if kept.Generation != pinned.Generation || kept.State == supervisor.Stopped {
		return fmt.Errorf("real pin was reaped")
	}
	if err := m.SetPin(request, false); err != nil {
		return err
	}
	raceSpool, err := communication.NewSpool(filepath.Join(root, "reap-race-spool"))
	if err != nil {
		return err
	}
	raceJob := template
	raceJob.ID, raceJob.IdempotencyKey, raceJob.Text = "real-reap-race", "real-reap-race", "contract probe"
	if _, err := raceSpool.Enqueue(raceJob); err != nil {
		return err
	}
	raceRunner := runner
	raceRunner.Spool, raceRunner.Resume = raceSpool, false
	// Recovery after an uncertain outcome goes through the production resume
	// endpoint — the same hop the gateway worker takes after requeue.
	resumeRunner := runner
	resumeRunner.Spool, resumeRunner.Resume = raceSpool, true
	start := make(chan struct{})
	results := make(chan error, 2)
	beforeRace := starts.Load()
	go func() { <-start; results <- m.Reap(ctx, time.Now().Add(time.Hour)) }()
	go func() {
		<-start
		outcome, err := raceRunner.RunOutcome(ctx, raceJob)
		// Pinned Hermes conservatively retains a crashed turn lease for 300s when
		// Docker reuses its PID. Keep the admitted run and use production resume;
		// never delete upstream locks or resubmit the prompt to make a fast test.
		admittedRun := outcome.RunID
		settleDeadline := time.Now().Add(6 * time.Minute)
		if errors.Is(err, communication.ErrUncertain) && admittedRun != "" {
			fmt.Println("Real native crashed-turn wait: original run held; resume without input replay")
		}
		unboundSince := time.Time{}
		for errors.Is(err, communication.ErrUncertain) && time.Now().Before(settleDeadline) {
			m.ReconcileHealth(ctx, time.Now())
			current, _, statusErr := m.Status(binding)
			// Resume leases exist only while a resume call is in flight; the
			// durable desired-work flag is what may never drop mid-settle.
			if statusErr != nil || !current.Desired {
				err = fmt.Errorf("waiting native lease lost desired work/hold")
				break
			}
			if reapErr := m.Reap(ctx, time.Now().Add(time.Hour)); reapErr != nil {
				err = reapErr
				break
			}
			if waitErr := contractSleep(ctx, 5*time.Second); waitErr != nil {
				err = waitErr
				break
			}
			outcome, err = resumeRunner.RunOutcome(ctx, raceJob)
			if admittedRun != "" && outcome.RunID != "" && outcome.RunID != admittedRun {
				err = fmt.Errorf("waiting native lease changed admitted run")
				break
			}
			if outcome.RunID != "" {
				admittedRun = outcome.RunID
				unboundSince = time.Time{}
			} else if unboundSince.IsZero() {
				unboundSince = time.Now()
			} else if time.Since(unboundSince) > 45*time.Second {
				// Admission never bound a run and no reconciler runs in this
				// fixture: the production fallthrough is the same expiry sweep
				// that closes unobservable records — interrupt is the named
				// failure, never a silent duplicate.
				m.ExpireUncertain(ctx, time.Now().Add(2*time.Hour))
			}
		}
		// The chaos contract settles at one answer or one named failure:
		// interrupted is the named failure for an admission that lost its run,
		// not a defect.
		if err != nil && outcome.Status == "interrupted" {
			err = nil
		}
		if err != nil {
			mapping, _, _ := raceSpool.Mapping(raceJob.ID)
			err = fmt.Errorf("status=%s event=%s run=%s mapping=%s/%s: %w", outcome.Status, outcome.LastEvent, outcome.RunID, mapping.Status, mapping.LastKnownEvent, err)
			if current, _, e := m.Status(binding); e == nil && mapping.RunID != "" {
				if id, e := smokeOwnedContainerID(ctx, m, current); e == nil {
					if response, e := hermesHTTP(ctx, func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }, id, nativeAuth, http.MethodGet, "/v1/runs/"+mapping.RunID, "", ""); e == nil {
						var native struct {
							Status string `json:"status"`
							Error  string `json:"error"`
						}
						if json.Unmarshal(response.body, &native) == nil {
							if len(native.Error) > 512 {
								native.Error = native.Error[:512]
							}
							err = fmt.Errorf("native=%s/%s: %w", native.Status, native.Error, err)
						}
					}
				}
			}
		}
		if err == nil && outcome.Status != "completed" && outcome.Status != "interrupted" {
			err = fmt.Errorf("race job neither completed nor named-failed: %s", outcome.Status)
		}
		results <- err
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			return fmt.Errorf("real new-job/reap race: %w", err)
		}
	}
	if starts.Load() > beforeRace+1 {
		return fmt.Errorf("reap race started duplicate replacement")
	}
	kept, err = smokeWaitIdle(ctx, m, binding)
	if err != nil {
		return err
	}
	id, err := smokeOwnedContainerID(ctx, m, kept)
	if err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "docker", "rm", "-f", id).Run(); err != nil {
		return err
	}
	m.ReconcileHealth(ctx, time.Now())
	m.RecoverMissingWork(ctx)
	missingIdle, _, _ := m.Status(binding)
	if missingIdle.Generation != kept.Generation || missingIdle.RuntimeHealth != "missing" {
		return fmt.Errorf("missing idle registration woke")
	}
	if err := m.Reap(ctx, time.Now().Add(time.Hour)); err != nil {
		return err
	}
	m.ReconcileHealth(ctx, time.Now())
	m.RecoverMissingWork(ctx)
	idle, _, _ := m.Status(binding)
	if idle.State != supervisor.Stopped {
		return fmt.Errorf("idle registration woke without work")
	}
	spool, err := communication.NewSpool(filepath.Join(root, "routine-spool"))
	if err != nil {
		return err
	}
	occurrence := communication.RoutineOccurrence{ScheduleID: "synthetic-one-shot", Revision: 1, DueAt: time.Now().UTC(), Job: template}
	occurrence.Job.Text = "contract probe"
	if err := spool.PutOccurrence(occurrence, template.Envelope); err != nil {
		return err
	}
	for range 2 {
		if err := spool.DispatchDueOccurrences(time.Now(), func(job communication.Job) error {
			if !job.Envelope.Equal(template.Envelope) {
				return fmt.Errorf("routine authority changed")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	job, err := spool.ClaimJob()
	if err != nil || job == nil {
		return fmt.Errorf("due routine did not enqueue: %v", err)
	}
	// Include the supervisor four-minute readiness bound before native execution.
	runner.Limit = 5 * time.Minute
	runner.Spool, runner.Resume = spool, false
	outcome, runErr := runner.RunOutcome(ctx, *job)
	admittedRun := outcome.RunID
	observeCtx, stopObservation := context.WithTimeout(ctx, 5*time.Minute)
	defer stopObservation()
	if errors.Is(runErr, communication.ErrUncertain) && admittedRun != "" {
		fmt.Println("Real due routine observation: resume admitted run without input replay")
	}
	for errors.Is(runErr, communication.ErrUncertain) && admittedRun != "" && observeCtx.Err() == nil {
		mapping, found, err := spool.Mapping(job.ID)
		if err != nil || !found || mapping.RunID != admittedRun || mapping.SessionID == "" {
			return errors.New("due routine lost admitted run during observation")
		}
		m.ReconcileHealth(observeCtx, time.Now())
		if err := contractSleep(observeCtx, time.Second); err != nil {
			break
		}
		// HTTPRunner resumes the durable mapping and clears the input, as the gateway does.
		outcome, runErr = runner.RunOutcome(observeCtx, *job)
		if outcome.RunID != "" && outcome.RunID != admittedRun {
			return errors.New("due routine observation changed admitted run")
		}
	}
	if runErr != nil || outcome.Status != "completed" {
		mapping, _, _ := spool.Mapping(job.ID)
		return fmt.Errorf("real due routine wake failed: status=%s event=%s run=%s mapping=%s/%s error=%v", outcome.Status, outcome.LastEvent, outcome.RunID, mapping.Status, mapping.LastKnownEvent, runErr)
	}
	if err := spool.CompleteJob(job.ID); err != nil {
		return err
	}
	finalFound := false
	for range 16 {
		delivery, err := spool.ClaimDelivery()
		if err != nil {
			return fmt.Errorf("routine durable final missing: %v", err)
		}
		if delivery == nil {
			break
		}
		if delivery.Text == "contract final answer" && delivery.JobID == job.ID {
			finalFound = true
		} else if delivery.JobID != "" {
			return fmt.Errorf("routine published an unexpected final")
		}
		if err := spool.CompleteDelivery(delivery.ID); err != nil {
			return err
		}
		if finalFound {
			break
		}
	}
	if !finalFound {
		return fmt.Errorf("routine durable final missing: progress exhausted")
	}
	if duplicate, err := spool.ClaimJob(); err != nil || duplicate != nil {
		return fmt.Errorf("routine dispatched twice")
	}
	if _, err := smokeWaitIdle(ctx, m, binding); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(time.Hour)); err != nil {
		return err
	}
	settled, _, _ := m.Status(binding)
	if settled.State != supervisor.Stopped {
		return fmt.Errorf("routine runtime did not sleep after handoff")
	}
	fmt.Println("Real desired-state sources passed: operator pin, idle no-wake, due routine wake/final/handoff/sleep and preservation canaries")
	return nil
}

func supervisorOrphanSmoke(ctx context.Context, m *supervisor.Manager, current supervisor.Runtime, image string) error {
	id, err := smokeOwnedContainerID(ctx, m, current)
	if err != nil {
		return err
	}
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Config.Labels}}", id).Output()
	var labels map[string]string
	if err != nil || json.Unmarshal(raw, &labels) != nil {
		return fmt.Errorf("real ownership labels unavailable")
	}
	var created []string
	defer func() {
		for _, id := range created {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
	}()
	for _, foreign := range []bool{false, true} {
		owner := labels["hermes-hub.owner"]
		if foreign {
			owner = "synthetic-other-supervisor"
		}
		out, err := exec.CommandContext(ctx, "docker", "create", "--name", fmt.Sprintf("%s-inventory-%t", current.Container, foreign), "--label", "hermes-hub.owner="+owner, "--label", "hermes-hub.context="+labels["hermes-hub.context"], "--label", "hermes-hub.generation="+current.Generation, image).Output()
		fixtureID := strings.TrimSpace(string(out))
		if err != nil || len(fixtureID) != 64 {
			return fmt.Errorf("real orphan fixture create failed: %v", err)
		}
		if _, err := hex.DecodeString(fixtureID); err != nil {
			return err
		}
		created = append(created, fixtureID)
	}
	if err := m.DetectOrphans(ctx); err != nil {
		return err
	}
	orphans := m.Orphans()
	if len(orphans) != 1 || !strings.HasPrefix(created[0], orphans[0].Container) {
		return fmt.Errorf("real same-generation orphan or foreign isolation failed: count=%d", len(orphans))
	}
	m.ReapOrphans(ctx)
	for _, id := range created {
		if err := exec.CommandContext(ctx, "docker", "inspect", id).Run(); err != nil {
			return fmt.Errorf("unknown or foreign orphan was removed")
		}
		if err := exec.CommandContext(ctx, "docker", "rm", "-f", id).Run(); err != nil {
			return err
		}
	}
	created = nil
	if err := m.DetectOrphans(ctx); err != nil {
		return err
	}
	if len(m.Orphans()) != 0 {
		return fmt.Errorf("orphan inventory did not settle after verified test cleanup")
	}
	fmt.Println("Real orphan inventory passed: duplicate generation detected; unknown and foreign compute preserved")
	return nil
}
