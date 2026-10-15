package api

import (
	"strings"
	"testing"
)

func TestTelegramDefinitionContract(t *testing.T) {
	if !TelegramTool("get_account", ReadEffect) || TelegramTool("get_account", WriteEffect) ||
		!TelegramTool("send_message", WriteEffect) || TelegramTool("send_message", ReadEffect) ||
		TelegramTool("unknown", ReadEffect) {
		t.Fatal("TelegramTool classification")
	}

	deployment := readyDefinition("sha256:" + strings.Repeat("ab", 32))
	read, err := TelegramDefinition(deployment, false)
	if err != nil {
		t.Fatal(err)
	}
	if read.DefinitionID != "telegram-account-read" || len(read.Tools) != 5 || len(read.Credentials) != 5 {
		t.Fatalf("read def: %+v", read)
	}
	for _, tool := range read.Tools {
		if tool.Effect != ReadEffect || len(tool.InputSchema) == 0 {
			t.Fatalf("read tool: %+v", tool)
		}
	}
	write, err := TelegramDefinition(deployment, true)
	if err != nil {
		t.Fatal(err)
	}
	if write.DefinitionID != "telegram-account-write" || len(write.Tools) != 3 {
		t.Fatalf("write def: %+v", write)
	}
	for _, tool := range write.Tools {
		if tool.Effect != WriteEffect {
			t.Fatalf("write tool effect: %+v", tool)
		}
	}
	// a deployment that overrides the adapter entrypoint is refused
	bad := deployment
	bad.Source.Command = "evil"
	if _, err := TelegramDefinition(bad, false); err == nil {
		t.Fatal("entrypoint override accepted")
	}
}
