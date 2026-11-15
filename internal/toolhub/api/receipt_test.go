package api

import (
	"strings"
	"testing"
)

func TestSafeReceiptRejectsPayloadAndAuthLinks(t *testing.T) {
	for _, value := range []string{
		"https://auth.invalid/callback?code=fixture", "https:fixture-secret", "Bearer fixture-secret",
		"Authorization:fixture-secret", `{"credential":"fixture-secret"}`, "[fixture-secret]",
		"key=fixture-secret", "wl-1@unknown-scope@cell=cell-1,rt=runc", "wl-1@filesystem,network,pid@cell=cell-1,credential=fixture", "primary:event\n", "primary:private text",
		"primary:event\x00", strings.Repeat("x", 257),
	} {
		if SafeReceipt(value) != "" {
			t.Fatal("unsafe receipt reached the audit contract")
		}
	}
	if SafeReceipt(" primary:event ") != "primary:event" {
		t.Fatal("ordinary surrounding whitespace changed the receipt identity")
	}
	for _, value := range []string{"T01:C02:12345.678", "primary:event-1", "person+tag@example.com:event_1", "receipt-1", "wl-1@filesystem,network,pid@cell=cell-1,rt=runc"} {
		if SafeReceipt(value) != value {
			t.Fatal("canonical resource identifier was changed")
		}
	}
}
