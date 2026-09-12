package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const HermesMigrationPin = "869228cab4a8276d3b4c78da9d9939670c47bd0f"

type NativeCronSnapshot struct {
	HermesPin  string `json:"hermes_pin"`
	ActiveJobs int    `json:"active_jobs"`
}

// InspectNativeCron uses the pinned upstream list_jobs SDK in a disposable copy.
// The source home is read-only; native auto-repair can modify only tmpfs.
func InspectNativeCron(ctx context.Context, dir, image string, command func(context.Context, ...string) ([]byte, error)) (NativeCronSnapshot, error) {
	var result NativeCronSnapshot
	home, err := filepath.Abs(filepath.Join(dir, "hermes"))
	if err != nil {
		return result, err
	}
	if info, err := os.Lstat(home); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("native cron source is not the selected regular Hermes home")
	}
	// ponytail: snapshot has a 64MiB ceiling; use an upstream metadata-only export if cron homes exceed it.
	script := "import json,os,shutil,subprocess; from pathlib import Path; p=Path('/source/cron'); dst=Path('/state/hermes/cron'); dst.mkdir(parents=True); shutil.copytree(p,dst,dirs_exist_ok=True) if p.exists() else None; from cron.jobs import list_jobs; print(json.dumps({'hermes_pin':subprocess.check_output(['git','-c','safe.directory=/opt/hermes','-C','/opt/hermes','rev-parse','HEAD'],text=True).strip(),'active_jobs':len(list_jobs(include_disabled=False))}))"
	body, err := command(ctx, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--tmpfs", "/state:size=64m,uid=10001,gid=10001,mode=0700", "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777", "--mount", "type=bind,src="+home+",dst=/source,readonly", "-e", "HERMES_HOME=/state/hermes", "--entrypoint", "/opt/hermes/.venv/bin/python", image, "-c", script)
	if err != nil || len(body) > 16*1024 || json.Unmarshal(body, &result) != nil || result.HermesPin != HermesMigrationPin || result.ActiveJobs < 0 {
		return result, errors.New("pinned upstream native cron snapshot could not be verified")
	}
	return result, nil
}
