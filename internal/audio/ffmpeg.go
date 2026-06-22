package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
)

type PCMAsset struct {
	Path        string
	TotalFrames int64
}

func (a PCMAsset) Remove() { _ = os.Remove(a.Path) }

type FFmpeg struct {
	Binary       string
	TempDir      string
	processCount atomic.Int64
	onProcesses  func(int64)
}

func NewFFmpeg(binary, tempDir string, onProcesses func(int64)) *FFmpeg {
	if binary == "" {
		binary = "ffmpeg"
	}
	return &FFmpeg{Binary: binary, TempDir: tempDir, onProcesses: onProcesses}
}

func (f *FFmpeg) Decode(ctx context.Context, sourcePath string) (PCMAsset, error) {
	tmp, err := os.CreateTemp(f.TempDir, "radio-pcm-*.f32")
	if err != nil {
		return PCMAsset{}, fmt.Errorf("create PCM file: %w", err)
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return PCMAsset{}, err
	}

	cmd := exec.CommandContext(ctx, f.Binary,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", sourcePath,
		"-vn", "-sn", "-dn",
		"-ac", strconv.Itoa(Channels), "-ar", strconv.Itoa(SampleRate),
		"-f", "f32le", "-acodec", "pcm_f32le", "-y", path,
	)
	f.processStarted()
	output, runErr := cmd.CombinedOutput()
	f.processStopped()
	if runErr != nil {
		os.Remove(path)
		return PCMAsset{}, fmt.Errorf("decode media with ffmpeg: %w: %s", runErr, string(output))
	}
	info, err := os.Stat(path)
	if err != nil {
		os.Remove(path)
		return PCMAsset{}, err
	}
	frameBytes := int64(Channels * BytesPerSample)
	if info.Size() == 0 || info.Size()%frameBytes != 0 {
		os.Remove(path)
		return PCMAsset{}, errors.New("decoded PCM is empty or malformed")
	}
	return PCMAsset{Path: path, TotalFrames: info.Size() / frameBytes}, nil
}

type PCMReader struct {
	file      *os.File
	remaining int64
}

func OpenPCM(asset PCMAsset) (*PCMReader, error) {
	file, err := os.Open(asset.Path)
	if err != nil {
		return nil, err
	}
	return &PCMReader{file: file, remaining: asset.TotalFrames}, nil
}

func (r *PCMReader) ReadFrames(frames int) ([]float32, bool, error) {
	if frames <= 0 {
		return nil, r.remaining == 0, nil
	}
	if int64(frames) > r.remaining {
		frames = int(r.remaining)
	}
	if frames == 0 {
		return nil, true, nil
	}
	raw := make([]byte, frames*Channels*BytesPerSample)
	if _, err := io.ReadFull(r.file, raw); err != nil {
		return nil, false, err
	}
	samples := make([]float32, frames*Channels)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	r.remaining -= int64(frames)
	return samples, r.remaining == 0, nil
}

func (r *PCMReader) RemainingFrames() int64 { return r.remaining }
func (r *PCMReader) Close() error           { return r.file.Close() }

type Encoder struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	done      chan error
	closeOnce sync.Once
	ffmpeg    *FFmpeg
}

func (f *FFmpeg) StartEncoder(ctx context.Context, dst io.Writer, cfg OutputConfig) (*Encoder, error) {
	cfg = cfg.Normalize()
	cmd := exec.CommandContext(ctx, f.Binary,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "f32le", "-acodec", "pcm_f32le",
		"-ar", strconv.Itoa(SampleRate), "-ac", strconv.Itoa(Channels), "-i", "pipe:0",
		"-vn", "-acodec", "libmp3lame", "-b:a", strconv.Itoa(cfg.BitrateKbps)+"k",
		"-f", "mp3", "-write_xing", "0", "pipe:1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start ffmpeg encoder: %w", err)
	}
	f.processStarted()
	encoder := &Encoder{cmd: cmd, stdin: stdin, done: make(chan error, 1), ffmpeg: f}
	go func() {
		_, copyErr := io.Copy(dst, stdout)
		waitErr := cmd.Wait()
		f.processStopped()
		if copyErr != nil {
			encoder.done <- copyErr
			return
		}
		encoder.done <- waitErr
	}()
	return encoder, nil
}

func (e *Encoder) Write(samples []float32) error {
	if err := ValidateSamples(samples); err != nil {
		return err
	}
	raw := make([]byte, len(samples)*BytesPerSample)
	for i, sample := range samples {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(sample))
	}
	_, err := e.stdin.Write(raw)
	return err
}

func (e *Encoder) Close() error {
	var result error
	e.closeOnce.Do(func() {
		if err := e.stdin.Close(); err != nil {
			result = err
		}
		if err := <-e.done; result == nil && err != nil {
			result = err
		}
	})
	return result
}

func (f *FFmpeg) processStarted() {
	count := f.processCount.Add(1)
	if f.onProcesses != nil {
		f.onProcesses(count)
	}
}

func (f *FFmpeg) processStopped() {
	count := f.processCount.Add(-1)
	if f.onProcesses != nil {
		f.onProcesses(count)
	}
}
