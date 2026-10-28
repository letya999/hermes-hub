package channel

import (
	"context"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type capturePoster struct{ texts []string }

func (p *capturePoster) PostMessage(ctx context.Context, channel, thread, text string) error {
	p.texts = append(p.texts, text)
	return nil
}

// DeliverArtifact composes the artifact note in the delivery's stamped
// language — the claim-time resolver is the source of truth.
func TestSlackArtifactNoteLocalized(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  hubruntime.ArtifactRef
		key  string
	}{
		{"ready", hubruntime.ArtifactRef{Name: "report.pdf"}, "slack.artifact-ready"},
		{"failed", hubruntime.ArtifactRef{Name: "report.pdf", Error: "too large"}, "slack.artifact-failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			poster := &capturePoster{}
			c := NewSlack(SlackDeps{Slack: func() SlackPoster { return poster }})
			d := commodel.Delivery{Lang: locale.LangES, SlackChannel: "D0123"}
			if err := c.DeliverArtifact(context.Background(), &d, tc.ref); err != nil {
				t.Fatalf("DeliverArtifact: %v", err)
			}
			if _, ok := locale.Lookup(locale.LangES, tc.key); !ok {
				t.Fatalf("catalog missing es %q", tc.key)
			}
			want := locale.T(locale.LangES, tc.key, "report.pdf")
			if len(poster.texts) != 1 {
				t.Fatalf("posts=%d want 1", len(poster.texts))
			}
			if poster.texts[0] != want {
				t.Fatalf("note %q != %q", poster.texts[0], want)
			}
		})
	}
}
