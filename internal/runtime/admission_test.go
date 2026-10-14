package runtime

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAdmissionReceiptGuards(t *testing.T) {
	for _, mode := range []string{"missing", "foreign-job", "foreign-principal", "corrupt", "empty", "expired", "future"} {
		t.Run(mode, func(t *testing.T) {
			oldState := state
			state = t.TempDir()
			t.Cleanup(func() { state = oldState })
			request := validExecuteRequest("alice", "personal", "idem", "private")
			request.JobID = "job-1"
			payload := hermesRunBody{Input: "private", SessionID: "original-session"}
			if mode != "missing" {
				if _, err := admissionPayload(request, &payload); err != nil {
					t.Fatal(err)
				}
				files, err := os.ReadDir(filepath.Join(state, "admissions"))
				if err != nil || len(files) != 1 {
					t.Fatalf("journal files=%d err=%v", len(files), err)
				}
				path := filepath.Join(state, "admissions", files[0].Name())
				switch mode {
				case "foreign-job":
					request.JobID = "job-2"
				case "foreign-principal":
					request.PrincipalID = "bob"
				case "corrupt", "empty":
					body := []byte("{")
					if mode == "empty" {
						body = []byte("{}")
					}
					if err := os.WriteFile(path, body, 0600); err != nil {
						t.Fatal(err)
					}
				case "expired", "future":
					stamp := time.Now().Add(-23 * time.Hour)
					if mode == "future" {
						stamp = time.Now().Add(time.Hour)
					}
					if err := os.Chtimes(path, stamp, stamp); err != nil {
						t.Fatal(err)
					}
				}
			}
			request.Text = ""
			if _, err := admissionPayload(request, nil); err == nil {
				t.Fatal("unsafe receipt authorized readmission")
			}
		})
	}
}

func TestAdmissionConcurrentPayloadConflict(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	request := validExecuteRequest("alice", "personal", "idem", "")
	request.JobID = "job-1"
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan hermesRunBody, 2)
	for _, input := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			payload := hermesRunBody{Input: input, SessionID: "s"}
			if saved, err := admissionPayload(request, &payload); err == nil {
				results <- saved
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	if len(results) != 1 {
		t.Fatalf("successful conflicting writers=%d, want one", len(results))
	}
	winner := <-results
	saved, err := admissionPayload(request, nil)
	if err != nil || saved != winner {
		t.Fatalf("saved=%+v winner=%+v err=%v", saved, winner, err)
	}
}

func TestAdmissionCompactsExpiredPayloads(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	request := validExecuteRequest("alice", "personal", "idem", "")
	request.JobID = "old-job"
	payload := hermesRunBody{Input: "old private prompt", SessionID: "s"}
	if _, err := admissionPayload(request, &payload); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(state, "admissions"))
	if err != nil || len(files) != 1 {
		t.Fatalf("receipt missing: %v", err)
	}
	path := filepath.Join(state, "admissions", files[0].Name())
	stamp := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	fresh := request
	fresh.JobID, fresh.IdempotencyKey = "new-job", "new-key"
	if _, err := admissionPayload(fresh, &payload); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired private payload retained: %v", err)
	}
	if _, err := admissionPayload(request, nil); err == nil {
		t.Fatal("compacted receipt admitted old work")
	}
	if actual, err := admissionPayload(fresh, nil); err != nil || actual != payload {
		t.Fatalf("fresh receipt lost: %+v %v", actual, err)
	}
}
