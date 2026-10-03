//go:build !sherpa

package mediasvc

import "errors"

// Default build has no native dependency: the sherpa engine compiles only
// with `-tags sherpa` and the sherpa-onnx C library installed. Until then the
// binary refuses the engine name rather than failing at first request.
func newSherpaSTT(Config) (STTEngine, error) {
	return nil, errors.New("sherpa engine requires building with -tags sherpa")
}

func newSherpaTTS(Config) (TTSEngine, error) {
	return nil, errors.New("sherpa engine requires building with -tags sherpa")
}
