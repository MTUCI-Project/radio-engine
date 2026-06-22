package audio

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMixerDuckingAndLimiter(t *testing.T) {
	mixer := NewMixer(AnnouncementProfile{
		MusicGainDB:        -18,
		AnnouncementGainDB: 0,
		AttackMS:           1,
		ReleaseMS:          1,
		LimiterThresholdDB: -3,
	})
	music := make([]float32, BlockFrames*Channels)
	announcement := make([]float32, BlockFrames*Channels)
	for i := range music {
		music[i] = 0.8
		announcement[i] = 0.8
	}
	out := mixer.Mix(music, announcement, true)
	threshold := DBToGain(-3)
	for i, sample := range out {
		if sample > threshold+0.001 {
			t.Fatalf("sample %d = %f exceeds limiter threshold %f", i, sample, threshold)
		}
	}
}

func TestCrossfadeEqualPower(t *testing.T) {
	left := []float32{1, 1}
	right := []float32{0, 0}
	start := ApplyTransition(left, right, 0, "equal_power")
	if start[0] < 0.99 {
		t.Fatalf("start gain = %f, want current near 1", start[0])
	}
	end := ApplyTransition(left, right, 1, "equal_power")
	if end[0] > 0.01 {
		t.Fatalf("end gain = %f, want current near 0", end[0])
	}
}

func TestFFmpegDecodeAndEncode(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "tone.wav")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.2", "-y", input)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate tone: %v: %s", err, output)
	}

	ff := NewFFmpeg("ffmpeg", dir, nil)
	asset, err := ff.Decode(context.Background(), input)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	defer asset.Remove()
	if asset.TotalFrames <= 0 {
		t.Fatalf("total frames = %d", asset.TotalFrames)
	}
	reader, err := OpenPCM(asset)
	if err != nil {
		t.Fatalf("open pcm: %v", err)
	}
	samples, done, err := reader.ReadFrames(BlockFrames)
	if err != nil {
		t.Fatalf("read pcm: %v", err)
	}
	if done {
		t.Fatal("first block unexpectedly finished asset")
	}
	if len(samples) != BlockFrames*Channels {
		t.Fatalf("samples = %d", len(samples))
	}
	_ = reader.Close()

	var encoded bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	encoder, err := ff.StartEncoder(ctx, &encoded, OutputConfig{BitrateKbps: 128})
	if err != nil {
		t.Fatalf("start encoder: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := encoder.Write(samples); err != nil {
			t.Fatalf("encoder write: %v", err)
		}
	}
	if err := encoder.Close(); err != nil {
		t.Fatalf("encoder close: %v", err)
	}
	if encoded.Len() == 0 {
		t.Fatal("encoded MP3 is empty")
	}
	if err := os.WriteFile(filepath.Join(dir, "out.mp3"), encoded.Bytes(), 0o640); err != nil {
		t.Fatalf("write encoded sample: %v", err)
	}
}
