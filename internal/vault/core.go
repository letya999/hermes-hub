package vault

import (
	"os"
	"path/filepath"
)

// CoreBlock returns the current memory/CORE.md — the owner-curated block of
// directives and facts injected into private-audience runs (SPEC-0085 R1.6).
// It reads the file on every call: CORE is re-read per use, never cached.
func CoreBlock(root string) string {
	body, err := os.ReadFile(filepath.Join(root, "memory", "CORE.md"))
	if err != nil {
		return ""
	}
	if len(body) > coreCap {
		body = body[:coreCap]
	}
	return string(body)
}

// coreCap bounds the injected block: curated memory stays prompt-bounded no
// matter how much accrues (SPEC-0085 R1.6).
const coreCap = 3600
