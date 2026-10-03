package mediasvc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// sttChunkSeconds is the fixed window long audio is transcribed in. Whisper
// models are trained on 30 s windows; engines that chunk internally still get
// sane input.
const sttChunkSeconds = 30.0

// decodeAudioFn is the audio normalization step — a var so tests can run the
// job pipeline without ffmpeg.
var decodeAudioFn = decodeAudio

// wavSliceFn slices windows out of long audio — a var for the same reason.
var wavSliceFn = wavSlice

// wavDuration reads the PCM data chunk size of a canonical wav header.
func wavDuration(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var header [44]byte
	if _, err := f.Read(header[:]); err != nil {
		return 0, errors.New("invalid wav header")
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return 0, errors.New("not a wav file")
	}
	byteRate := binary.LittleEndian.Uint32(header[28:32])
	if byteRate == 0 {
		return 0, errors.New("invalid wav byte rate")
	}
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return float64(info.Size()-44) / float64(byteRate), nil
}

// wavSlice extracts [offset, offset+seconds) via ffmpeg — re-encoding through
// it keeps the header valid regardless of the source chunk layout.
func wavSlice(ctx context.Context, src, dst string, offset, seconds float64) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-y",
		"-ss", fmt.Sprintf("%.3f", offset), "-t", fmt.Sprintf("%.3f", seconds),
		"-i", src, "-vn", "-ac", "1", "-ar", "16000", "-f", "wav", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("audio slice failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
