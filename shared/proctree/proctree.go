// Package proctree owns the platform plumbing that keeps spawned service
// processes bound to their launcher: no child may survive the process that
// spawned it, and every child gets a graceful-stop signal before a kill.
//
// Windows binds children to a Job Object with KILL_ON_JOB_CLOSE and gives
// each its own console group so CTRL_BREAK interrupts only the child. Unix
// puts each child in its own process group and (on Linux) sets Pdeathsig so
// a dead launcher takes the child down with it.
package proctree

import (
	"os/exec"
	"sync"
)

// Group is the orphan bound for one launcher: every child assigned to it is
// killed when the launcher's handle closes — on a clean exit or a crash.
// Platforms without Job Objects (Unix) keep the group empty; per-child
// Pdeathsig/Setpgid in Configure provides the bound there.
type Group struct {
	mu     sync.Mutex
	handle uintptr
}

// NewGroup opens the launcher's orphan bound. Failure fails closed: a
// launcher that cannot bind its children must not spawn orphans.
func NewGroup() (*Group, error) {
	g := &Group{}
	if err := g.open(); err != nil {
		return nil, err
	}
	return g, nil
}

// Assign binds cmd's started process to the group. A failure is fatal for
// the child: callers must cancel it, or it could orphan.
func (g *Group) Assign(cmd *exec.Cmd) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.assign(cmd)
}

// Close releases the group handle. Children already stopped stay stopped;
// any survivor is terminated by the kernel on the last handle close.
func (g *Group) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.close()
}
