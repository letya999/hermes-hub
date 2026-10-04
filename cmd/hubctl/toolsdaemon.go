package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

// tools-daemon is the persistent private agent-tools executor: the ToolHub
// gateway keeps one `docker exec -i` session per owning runtime container and
// multiplexes framed calls over its stdin/stdout, so an admitted call costs a
// frame instead of a process spawn. The contract is identical to tools-exec
// — verbatim admitted scopes, no authority fields, bounded results — only
// the channel lifetime changes. It is never an MCP server and exposes no
// tool surface of its own. Frames arrive on a channel only the control
// plane can open; protocol violations end the daemon so a fresh session
// restarts clean.
const toolsDaemonMaxInFlight = 16

func runToolsDaemon(ctx context.Context) error {
	t, err := openExecTools()
	if err != nil {
		return fmt.Errorf("tools-daemon open: %w", err)
	}
	defer t.Close()

	reader := bufio.NewReaderSize(os.Stdin, 64*1024)
	encoder := json.NewEncoder(os.Stdout)
	var writeMu sync.Mutex
	var inFlight sync.WaitGroup
	sem := make(chan struct{}, toolsDaemonMaxInFlight)

	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > toolsExecMaxRequest+4096 {
			return errors.New("tools-daemon frame exceeds bound")
		}
		if len(line) > 0 {
			var frame toolhub.AgentExecFrame
			if jsonErr := json.Unmarshal(line, &frame); jsonErr != nil {
				return fmt.Errorf("tools-daemon frame: %w", jsonErr)
			}
			select {
			case sem <- struct{}{}:
				inFlight.Add(1)
				go func() {
					defer inFlight.Done()
					defer func() { <-sem }()
					reply := toolhub.AgentExecReply{ID: frame.ID, AgentExecResult: execRequest(ctx, t, frame.AgentExecRequest)}
					writeMu.Lock()
					_ = encoder.Encode(reply)
					writeMu.Unlock()
				}()
			default:
				reply := toolhub.AgentExecReply{ID: frame.ID, AgentExecResult: toolhub.AgentExecResult{Error: "tools-daemon busy"}}
				writeMu.Lock()
				_ = encoder.Encode(reply)
				writeMu.Unlock()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				inFlight.Wait()
				return nil
			}
			return fmt.Errorf("tools-daemon read: %w", err)
		}
	}
}
