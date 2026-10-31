package routines

import (
	"context"
	"testing"
	"time"
)

func TestScriptProbeDecodesStructuredEvent(t *testing.T) {
	script := `event = json.decode(input())
emit("keep" if event["chat_id"] == -1001 and "github" in event["text"].lower() else "drop")`
	for input, want := range map[string]string{
		`{"chat_id":-1001,"text":"GitHub issue opened"}`: "keep",
		`{"chat_id":-1002,"text":"GitHub issue opened"}`: "drop",
		`{"chat_id":-1001,"text":"ordinary chat"}`:       "drop",
	} {
		got, err := RunScriptProbe(context.Background(), script, time.Unix(0, 0), input, "", nil)
		if err != nil || len(got) != 1 || got[0] != want {
			t.Fatalf("event %s: got %v, err %v", input, got, err)
		}
	}
}
