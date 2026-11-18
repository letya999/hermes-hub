package stack

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// KnowledgeDoctor reports the knowledge_ui edge for `hubctl doctor` (R10.4):
// the per-space OpenKnowledge editor over the vault, reachable only on the
// loopback publish ui_port opts into.
func KnowledgeDoctor(s Settings) []string {
	if !s.Has("knowledge_ui") {
		return nil
	}
	if s.UIPort == 0 {
		return []string{"Knowledge UI: network-only — set ui_port for a loopback editor edge"}
	}
	bind := s.UIBind
	if bind == "" {
		bind = "127.0.0.1"
	}
	if bind != "127.0.0.1" && bind != "::1" {
		return []string{"Knowledge UI: refused — ui_bind " + bind + " is not loopback"}
	}
	addr := net.JoinHostPort(bind, strconv.Itoa(s.UIPort))
	lines := []string{fmt.Sprintf("Knowledge UI: http://localhost:%d — loopback-only on %s", s.UIPort, bind)}
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return append(lines, "Knowledge UI: nothing listening on "+addr+" — run hubctl up first")
	}
	_ = conn.Close()
	return append(lines, "Knowledge UI: listening on "+addr)
}
