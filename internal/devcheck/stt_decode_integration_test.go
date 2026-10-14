//go:build integration

package devcheck

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestSTTDecodeContract(t *testing.T) {
	image := os.Getenv("HUB_STT_CONTRACT_IMAGE")
	if image == "" {
		t.Skip("set HUB_STT_CONTRACT_IMAGE to the built base or standalone STT image")
	}
	python := os.Getenv("HUB_STT_CONTRACT_PYTHON")
	if python == "" {
		python = "/opt/hermes/.venv/bin/python"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Exercise the real decoder without network, model downloads or personal audio.
	if err := sttDecodeSmoke(ctx, image, python); err != nil {
		t.Fatal(err)
	}
}
