package automation

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/sqlitedb"
)

func TestActivatedLegacySpoolUsesCurrentRowsAndKeepsOwnerBoundary(t *testing.T) {
	root := t.TempDir()
	db, err := sqlitedb.Open(filepath.Join(root, "spool.db"), 2, func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE rec(path TEXT PRIMARY KEY, body BLOB NOT NULL, mtime INTEGER NOT NULL)")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := os.WriteFile(filepath.Join(root, "records.activated"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	b := legacyBindings{root: root}
	put := func(path string, value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO rec VALUES(?, ?, 1)", filepath.ToSlash(rel), body); err != nil {
			t.Fatal(err)
		}
	}
	alice := commodel.Task{TaskID: "task-a", PrincipalID: "alice", ContextID: "alice"}
	bob := commodel.Task{TaskID: "task-b", PrincipalID: "bob", ContextID: "bob"}
	put(b.taskPath("alice", alice.TaskID), alice)
	put(b.taskPath("bob", bob.TaskID), bob)
	put(b.topicPath("alice", 11, 5), map[string]string{"task_id": alice.TaskID})
	put(filepath.Join(root, "mappings", "job-a.json"), commodel.JobMapping{JobID: "job-a", PrincipalID: "alice"})
	put(filepath.Join(root, "running", "job-a.json"), commodel.Job{ID: "job-a", Trigger: "message"})
	if err := ValidateLegacySpool(root); err != nil {
		t.Fatal(err)
	}
	if task, err := b.loadTask("alice", alice.TaskID); err != nil || task.PrincipalID != "alice" {
		t.Fatalf("task: %+v %v", task, err)
	}
	if tasks, err := b.listTasks("alice"); err != nil || len(tasks) != 1 || tasks[0].TaskID != alice.TaskID {
		t.Fatalf("tasks: %+v %v", tasks, err)
	}
	if _, err := b.loadTask("alice", bob.TaskID); err == nil {
		t.Fatal("foreign task accepted")
	}
	if id, err := b.topicTask("alice", 11, 5); err != nil || id != alice.TaskID {
		t.Fatalf("topic: %s %v", id, err)
	}
	if mapping, err := b.loadMapping("job-a"); err != nil || mapping.PrincipalID != "alice" {
		t.Fatalf("mapping: %+v %v", mapping, err)
	}
	put(b.taskPath("alice", alice.TaskID), bob)
	if _, err := b.loadTask("alice", alice.TaskID); err == nil {
		t.Fatal("rebound task accepted")
	}
	put(filepath.Join(root, "running", "job-a.json"), commodel.Job{ID: "job-a", Trigger: "cron"})
	if err := ValidateLegacySpool(root); err == nil {
		t.Fatal("legacy cron accepted")
	}
	if _, err := db.Exec("DELETE FROM rec WHERE path = 'running/job-a.json'"); err != nil {
		t.Fatal(err)
	}
	put(filepath.Join(root, "schedules", "nested", "old.json"), map[string]string{"id": "old"})
	if err := ValidateLegacySpool(root); err == nil {
		t.Fatal("nested legacy schedule accepted")
	}
	reader, err := b.database()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.Exec("DELETE FROM rec"); err == nil {
		t.Fatal("read-only reader mutated gateway data")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM rec").Scan(&count); err != nil || count != 5 {
		t.Fatalf("records changed: %d %v", count, err)
	}
}
