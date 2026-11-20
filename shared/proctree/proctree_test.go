package proctree

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a test: it is the worker the tests re-exec this
// binary as. PROCTREE_HELPER_ROLE selects the role — "child" sleeps so the
// harness can kill it, "parent" spawns a configured child and reports its
// pid on stdout so the test can kill the parent underneath it.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("PROCTREE_HELPER_ROLE") {
	case "child":
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	case "parent":
		group, err := NewGroup()
		if err != nil {
			fmt.Println("ERR group: " + err.Error())
			os.Exit(2)
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess") // #nosec G204 -- re-execs this test binary
		cmd.Env = append(os.Environ(), "PROCTREE_HELPER_ROLE=child")
		Configure(cmd)
		if err := cmd.Start(); err != nil {
			fmt.Println("ERR start: " + err.Error())
			os.Exit(2)
		}
		if err := group.Assign(cmd); err != nil {
			fmt.Println("ERR assign: " + err.Error())
			os.Exit(2)
		}
		fmt.Println("CHILD_PID=" + strconv.Itoa(cmd.Process.Pid))
		// Block until the harness kills us; the group handle must die with
		// this process so the child cannot outlive it.
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	}
}

// spawnParent launches the parent helper and returns it plus the pid of the
// grandchild it spawned through proctree.
func spawnParent(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess") // #nosec G204 -- re-execs this test binary
	cmd.Env = append(os.Environ(), "PROCTREE_HELPER_ROLE=parent")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	lineCh := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lineCh <- sc.Text()
		}
		close(lineCh)
	}()
	timer := time.After(15 * time.Second)
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatal("parent exited before reporting a child pid")
			}
			if rest, cut := strings.CutPrefix(line, "CHILD_PID="); cut {
				pid, err := strconv.Atoi(rest)
				if err != nil {
					t.Fatalf("bad child pid %q: %v", line, err)
				}
				return cmd, pid
			}
			t.Fatalf("parent reported %q instead of a child pid", line)
		case <-timer:
			t.Fatal("parent did not report a child pid")
		}
	}
}

// waitDead polls until pid is gone or the deadline passes.
func waitDead(t *testing.T, pid int, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !alive(pid)
}
