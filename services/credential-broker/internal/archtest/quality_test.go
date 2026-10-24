package archtest

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSecurityToolsResolveBrokerModule(t *testing.T) {
	command := exec.Command("python3", "-c", `import json, pathlib, sys
from unittest.mock import patch
sys.path.insert(0, "scripts")
import quality
seen = []
def run(args, **kwargs):
    if args == ["go", "env", "GOVERSION"]:
        return json.loads(pathlib.Path("tools.json").read_text())["go_production"]
    seen.append(kwargs.get("env", {}))
    return ""
quality.run = run
with patch.object(pathlib.Path, "exists", return_value=True):
    quality.external_tools()
assert len(seen) == 3 and all(env.get("GOWORK") == "off" for env in seen), "security tools inherited the parent workspace"
`)
	command.Dir = filepath.Clean("../..")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("security tool module isolation: %v\n%s", err, output)
	}
}
