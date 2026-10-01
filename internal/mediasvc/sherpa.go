//go:build sherpa

// Native engine: sherpa-onnx Go bindings (cgo over onnxruntime). One native
// dependency covers STT (whisper/paraformer — ru included), TTS
// (vits/piper/kokoro voices) and speaker diarization (pyannote segmentation +
// 3dspeaker embeddings), all fully offline. Build with `-tags sherpa` and the
// sherpa-onnx C library installed; the default build uses the stub.
package mediasvc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

type sherpaSTT struct {
	mu         sync.Mutex
	recognizer *sherpa.OfflineRecognizer
	diarizer   *sherpa.OfflineSpeakerDiarization
}

func newSherpaSTT(c Config) (STTEngine, error) {
	eng := &sherpaSTT{}
	modelDir := c.ModelDir
	rec := &sherpa.OfflineRecognizerConfig{}
	rec.ModelConfig.Transducer = sherpa.OfflineTransducerModelConfig{} // zero
	switch {
	case strings.Contains(c.STTModel, "whisper"):
		rec.ModelConfig.Whisper = sherpa.OfflineWhisperModelConfig{
			Encoder: filepath.Join(modelDir, c.STTModel+"-encoder.onnx"),
			Decoder: filepath.Join(modelDir, c.STTModel+"-decoder.onnx"),
		}
	default:
		return nil, fmt.Errorf("sherpa stt model %q is not configured; set HUB_MEDIA_STT_MODEL", c.STTModel)
	}
	rec.ModelConfig.Tokens = filepath.Join(modelDir, c.STTModel+"-tokens.txt")
	rec.ModelConfig.NumThreads = 4
	r, err := sherpa.NewOfflineRecognizer(rec)
	if err != nil {
		return nil, err
	}
	eng.recognizer = r
	if c.DiarizeModel != "" {
		dc := &sherpa.OfflineSpeakerDiarizationConfig{
			Segmentation: sherpa.OfflineSpeakerSegmentationModelConfig{
				Pyannote: sherpa.OfflineSpeakerSegmentationPyannoteModelConfig{
					Model: filepath.Join(modelDir, c.DiarizeModel),
				},
			},
			Embedding: sherpa.SpeakerEmbeddingExtractorConfig{
				Model: filepath.Join(modelDir, c.EmbedModel),
			},
			Clustering: sherpa.FastClusteringConfig{NumClusters: -1},
		}
		if d, err := sherpa.NewOfflineSpeakerDiarization(dc); err == nil {
			eng.diarizer = d
		}
	}
	return eng, nil
}

func (e *sherpaSTT) Transcribe(ctx context.Context, wav string, opts TranscribeOpts) ([]Segment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	stream := sherpa.NewOfflineStream(e.recognizer)
	defer sherpa.DeleteOfflineStream(stream)
	data, err := wavSamples(wav)
	if err != nil {
		return nil, err
	}
	stream.AcceptWaveform(16000, data)
	e.recognizer.Decode(stream)
	res := stream.GetResult()
	text := strings.TrimSpace(res.Text)
	if text == "" {
		return nil, errors.New("empty transcript")
	}
	segs := []Segment{{Text: text}}
	if opts.Diarize && e.diarizer != nil {
		if labeled, err := e.diarize(data, segs); err == nil {
			segs = labeled
		}
	}
	return segs, nil
}

func (e *sherpaSTT) diarize(samples []float32, segs []Segment) ([]Segment, error) {
	result := e.diarizer.Process(samples)
	for i := range segs {
		var speaker string
		var best float64
		for _, d := range result.Segments {
			mid := (segs[i].Start + segs[i].End) / 2
			if mid >= d.Start && mid <= d.End && d.End-d.Start > best {
				best, speaker = d.End-d.Start, fmt.Sprintf("speaker-%d", d.Speaker)
			}
		}
		segs[i].Speaker = speaker
	}
	return segs, nil
}

func (e *sherpaSTT) Ready(context.Context) bool { return e.recognizer != nil }

type sherpaTTS struct {
	mu  sync.Mutex
	tts *sherpa.OfflineTts
}

func newSherpaTTS(c Config) (TTSEngine, error) {
	cfg := &sherpa.OfflineTtsConfig{}
	cfg.Model.Vits.Model = filepath.Join(c.ModelDir, c.TTSModel)
	cfg.Model.Vits.Tokens = filepath.Join(c.ModelDir, c.TTSModel+".tokens.txt")
	cfg.Model.Vits.DataDir = c.ModelDir
	cfg.Model.NumThreads = 2
	t, err := sherpa.NewOfflineTts(cfg)
	if err != nil {
		return nil, err
	}
	return &sherpaTTS{tts: t}, nil
}

func (e *sherpaTTS) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	audio := e.tts.Generate(text, 0, 1.0)
	if audio == nil || len(audio.Samples) == 0 {
		return nil, errors.New("empty synthesis")
	}
	return wavBytes(audio.Samples, audio.SampleRate), nil
}

func (e *sherpaTTS) Ready(context.Context) bool { return e.tts != nil }

func wavSamples(path string) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var header [44]byte
	if _, err := f.Read(header[:]); err != nil {
		return nil, err
	}
	pcm, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	out := make([]float32, len(pcm)/2)
	for i := range out {
		out[i] = float32(int16(binary.LittleEndian.Uint16(pcm[i*2:]))) / 32768
	}
	return out, nil
}

func wavBytes(samples []float32, rate int) []byte {
	pcm := make([]byte, len(samples)*2)
	for i, s := range samples {
		v := int16(s * 32767)
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [4]byte{'R', 'I', 'F', 'F'})
	binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	binary.Write(&b, binary.LittleEndian, [8]byte{'W', 'A', 'V', 'E', 'f', 'm', 't', ' '})
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	binary.Write(&b, binary.LittleEndian, [4]byte{'d', 'a', 't', 'a'})
	binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}
