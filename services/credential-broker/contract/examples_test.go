package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/credential-broker/internal/strictjson"
)

func TestShippedContractsAreValid(t *testing.T) {
	files, e := filepath.Glob("../examples/contracts/*.json")
	if e != nil {
		t.Fatal(e)
	}
	if len(files) < 5 {
		t.Fatal("missing reviewed examples")
	}
	for _, name := range files {
		t.Run(filepath.Base(name), func(t *testing.T) {
			raw, e := os.ReadFile(name)
			if e != nil {
				t.Fatal(e)
			}
			var c Contract
			if e = strictjson.Decode(raw, &c); e != nil {
				t.Fatal(e)
			}
			if e = c.Validate(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
