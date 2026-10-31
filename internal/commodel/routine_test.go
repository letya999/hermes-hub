package commodel

import "testing"

// The wait contract is a name property of the projected tool: a tool that
// holds the call until a batch or deadline needs no inter-round pacing on a
// stream schedule, while every other mcp_tool probe is a plain poll that
// must declare its cadence. This is the one contract both stream validation
// and the stream loop agree on.
func TestWaitCapableProbeToolContract(t *testing.T) {
	cases := map[string]bool{
		"hub-tg-wait_for_settled_message-abc": true,
		"hub-tg-wait_for_new_message-abc":     true,
		"hub-tg-wait-abc":                     true,
		"hub-tg-settled-abc":                  true,
		"hub-x-new_message-abc":               true,
		"hub-mail-events-abc":                 false,
		"hub-gh-list_issues-abc":              false,
		"hub-feed-poll-abc":                   false,
		"":                                    false,
	}
	for name, want := range cases {
		if got := WaitCapableProbeTool(name); got != want {
			t.Errorf("WaitCapableProbeTool(%q) = %v, want %v", name, got, want)
		}
	}
}
