//go:build integration

package devcheck

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// TTSCachedModelCanary requires the real Silero backend, not its piper/espeak fallback.
func TTSCachedModelCanary(ctx context.Context, image string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	probe := `import runpy,wave
w=runpy.run_path('/usr/local/bin/tts-worker')
assert w['silero_wav']('Короткая проверка', 'xenia', '/tmp/silero.wav'), 'Silero unavailable offline'
with wave.open('/tmp/silero.wav') as f:
 assert f.getnchannels()==1 and f.getsampwidth()==2
 assert 0.2 < f.getnframes()/f.getframerate() < 15
 assert any(f.readframes(f.getnframes())), 'silent waveform'
print('SILERO_OFFLINE=ok')
`
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--read-only",
		"--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--memory", "2g", "--cpus", "2", "--pids-limit", "128", "--tmpfs", "/tmp:rw,nosuid,nodev,size=128m,mode=1777",
		"--entrypoint", "python3", image, "-c", probe)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "SILERO_OFFLINE=ok") {
		return fmt.Errorf("offline Silero contract: %v: %s", err, tailProbeOutput(string(out)))
	}
	fmt.Println("TTS real Silero passed with network disabled and read-only baked model cache")
	return nil
}
