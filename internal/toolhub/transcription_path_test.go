package toolhub

import "testing"

func TestNormalizeTranscriptionPathStaysWithinWorkspace(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"/workspace/work/tools-audit-voice.wav", "work/tools-audit-voice.wav"},
		{"work/tools-audit-voice.wav", "work/tools-audit-voice.wav"},
		{"/workspace/../secret", "/workspace/../secret"},
		{"/workspace/work/../secret", "/workspace/work/../secret"},
		{"/workspace//work/file.wav", "/workspace//work/file.wav"},
		{"/other/work/file.wav", "/other/work/file.wav"},
	} {
		original := map[string]any{"path": tc.input}
		got := normalizeTranscriptionPath("media_transcribe", original)
		if got["path"] != tc.want || original["path"] != tc.input {
			t.Fatalf("path %q became %v; original=%v", tc.input, got["path"], original["path"])
		}
		if other := normalizeTranscriptionPath("media_edit", original); other["path"] != tc.input {
			t.Fatalf("unrelated tool rewrote %q", tc.input)
		}
	}
}
