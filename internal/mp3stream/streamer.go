package mp3stream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

var ErrSkipped = errors.New("track skipped")

type Streamer struct {
	FrameFallbackDelay time.Duration
	InitialBuffer      time.Duration
	SendID3v2Tags      bool
}

type Session struct {
	streamer *Streamer
	pacer    *pacer
}

func NewStreamer() *Streamer {
	return &Streamer{
		FrameFallbackDelay: 26 * time.Millisecond,
		InitialBuffer:      2 * time.Second,
	}
}

func (s *Streamer) NewSession() *Session {
	return &Session{
		streamer: s,
		pacer:    newPacer(time.Now(), s.initialBuffer()),
	}
}

func (s *Streamer) Stream(ctx context.Context, dst io.Writer, src io.Reader, commands <-chan struct{}) error {
	return s.NewSession().Stream(ctx, dst, src, commands)
}

func (s *Session) Stream(ctx context.Context, dst io.Writer, src io.Reader, commands <-chan struct{}) error {
	reader := bufio.NewReader(src)
	id3Writer := io.Discard
	if s.streamer.SendID3v2Tags {
		id3Writer = dst
	}
	if err := s.streamer.consumeID3v2(ctx, id3Writer, reader, commands); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-commands:
			return ErrSkipped
		default:
		}

		frame, duration, err := readFrame(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		if _, err := dst.Write(frame); err != nil {
			return fmt.Errorf("write mp3 frame: %w", err)
		}

		if duration <= 0 {
			duration = s.streamer.frameFallbackDelay()
		}
		if err := s.pacer.wait(ctx, commands, duration); err != nil {
			return err
		}
	}
}

func (s *Session) StreamSilence(ctx context.Context, dst io.Writer, commands <-chan struct{}) error {
	frame, duration, ok := parseSilentFrame()
	if !ok {
		return errors.New("embedded silent mp3 frame is invalid")
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-commands:
			return ErrSkipped
		default:
		}

		if _, err := dst.Write(frame); err != nil {
			return fmt.Errorf("write silent mp3 frame: %w", err)
		}
		if duration <= 0 {
			duration = s.streamer.frameFallbackDelay()
		}
		if err := s.pacer.wait(ctx, commands, duration); err != nil {
			return err
		}
	}
}

func (s *Session) ResetTiming() {
	s.pacer.reset(time.Now(), s.streamer.initialBuffer())
}

func (s *Streamer) frameFallbackDelay() time.Duration {
	if s.FrameFallbackDelay <= 0 {
		return 26 * time.Millisecond
	}
	return s.FrameFallbackDelay
}

func (s *Streamer) initialBuffer() time.Duration {
	if s.InitialBuffer < 0 {
		return 0
	}
	if s.InitialBuffer == 0 {
		return 2 * time.Second
	}
	return s.InitialBuffer
}

func (s *Streamer) consumeID3v2(ctx context.Context, dst io.Writer, reader *bufio.Reader, commands <-chan struct{}) error {
	header, err := reader.Peek(10)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, bufio.ErrBufferFull) {
			return nil
		}
		return fmt.Errorf("peek id3 header: %w", err)
	}
	if !bytes.Equal(header[:3], []byte("ID3")) {
		return nil
	}

	size := 10 + syncSafeInt(header[6:10])
	if header[5]&0x10 != 0 {
		size += 10
	}

	remaining := size
	buf := make([]byte, 4096)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-commands:
			return ErrSkipped
		default:
		}

		chunk := len(buf)
		if remaining < chunk {
			chunk = remaining
		}
		n, err := io.ReadFull(reader, buf[:chunk])
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("consume id3 tag: %w", writeErr)
			}
			remaining -= n
		}
		if err != nil {
			return fmt.Errorf("read id3 tag: %w", err)
		}
	}
	return nil
}

func readFrame(reader *bufio.Reader) ([]byte, time.Duration, error) {
	for {
		first, err := reader.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		if first != 0xff {
			continue
		}

		second, err := reader.ReadByte()
		if err != nil {
			return nil, 0, err
		}
		if second&0xe0 != 0xe0 {
			continue
		}

		header := [4]byte{first, second}
		if _, err := io.ReadFull(reader, header[2:]); err != nil {
			return nil, 0, err
		}

		info, ok := parseHeader(header)
		if !ok {
			continue
		}

		frame := make([]byte, info.frameSize)
		copy(frame, header[:])
		if _, err := io.ReadFull(reader, frame[4:]); err != nil {
			return nil, 0, err
		}
		return frame, info.duration, nil
	}
}

type frameInfo struct {
	frameSize int
	duration  time.Duration
}

var mpeg1BitratesKbps = [4][16]int{
	1: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448},
	2: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},
	3: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
}

var mpeg2BitratesKbps = [4][16]int{
	1: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256},
	2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	3: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
}

func parseHeader(header [4]byte) (frameInfo, bool) {
	versionID := (header[1] >> 3) & 0x03
	layerID := (header[1] >> 1) & 0x03
	bitrateIndex := (header[2] >> 4) & 0x0f
	sampleRateIndex := (header[2] >> 2) & 0x03
	padding := int((header[2] >> 1) & 0x01)

	if versionID == 1 || layerID == 0 || bitrateIndex == 0 || bitrateIndex == 15 || sampleRateIndex == 3 {
		return frameInfo{}, false
	}

	layer := 4 - int(layerID)
	bitrate := bitrateKbps(versionID, layer, bitrateIndex)
	sampleRate := sampleRate(versionID, sampleRateIndex)
	if bitrate == 0 || sampleRate == 0 {
		return frameInfo{}, false
	}

	samples := samplesPerFrame(versionID, layer)
	var frameSize int
	if layer == 1 {
		frameSize = ((12 * bitrate * 1000 / sampleRate) + padding) * 4
	} else if layer == 3 && versionID != 3 {
		frameSize = 72*bitrate*1000/sampleRate + padding
	} else {
		frameSize = 144*bitrate*1000/sampleRate + padding
	}
	if frameSize < 4 {
		return frameInfo{}, false
	}

	return frameInfo{
		frameSize: frameSize,
		duration:  time.Duration(float64(samples) / float64(sampleRate) * float64(time.Second)),
	}, true
}

func bitrateKbps(versionID byte, layer int, index byte) int {
	if versionID == 3 {
		return mpeg1BitratesKbps[layer][index]
	}
	return mpeg2BitratesKbps[layer][index]
}

func sampleRate(versionID byte, index byte) int {
	base := []int{44100, 48000, 32000}
	rate := base[index]
	switch versionID {
	case 3:
		return rate
	case 2:
		return rate / 2
	case 0:
		return rate / 4
	default:
		return 0
	}
}

func samplesPerFrame(versionID byte, layer int) int {
	switch layer {
	case 1:
		return 384
	case 2:
		return 1152
	case 3:
		if versionID == 3 {
			return 1152
		}
		return 576
	default:
		return 0
	}
}

func syncSafeInt(value []byte) int {
	return int(value[0])<<21 | int(value[1])<<14 | int(value[2])<<7 | int(value[3])
}

type pacer struct {
	start   time.Time
	elapsed time.Duration
	lead    time.Duration
}

func newPacer(start time.Time, lead time.Duration) *pacer {
	if lead < 0 {
		lead = 0
	}
	return &pacer{start: start, lead: lead}
}

func (p *pacer) wait(ctx context.Context, commands <-chan struct{}, frameDuration time.Duration) error {
	delay := p.delayFor(time.Now(), frameDuration)
	if delay <= 0 {
		p.advance(frameDuration)
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-commands:
		return ErrSkipped
	case <-timer.C:
		p.advance(frameDuration)
		return nil
	}
}

func (p *pacer) delayFor(now time.Time, frameDuration time.Duration) time.Duration {
	return p.start.Add(p.elapsed + frameDuration - p.lead).Sub(now)
}

func (p *pacer) advance(frameDuration time.Duration) {
	p.elapsed += frameDuration
}

func (p *pacer) reset(start time.Time, lead time.Duration) {
	if lead < 0 {
		lead = 0
	}
	p.start = start
	p.elapsed = 0
	p.lead = lead
}

func makeSilentMP3Frame() []byte {
	frame := []byte{
		0xff, 0xfb, 0x92, 0xc4, 0x39, 0x03, 0xc0, 0x00,
		0x01, 0xa4, 0x00, 0x00, 0x00, 0x20, 0x00, 0x00,
		0x34, 0x80, 0x00, 0x00, 0x04,
	}
	for len(frame) < 257 {
		frame = append(frame, 0x55)
	}
	frame = append(frame, []byte("LAME3.101 (beta 3)")...)
	for len(frame) < 418 {
		frame = append(frame, 0x55)
	}
	return frame
}

func parseSilentFrame() ([]byte, time.Duration, bool) {
	frame := makeSilentMP3Frame()
	var header [4]byte
	copy(header[:], frame[:4])
	info, ok := parseHeader(header)
	if !ok || info.frameSize != len(frame) {
		return nil, 0, false
	}
	return frame, info.duration, true
}

const silentMP3FrameBase64 = "//uSxDkDwAABpAAAACAAADSAAAAEVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVUxBTUUzLjEwMSAoYmV0YSAzKVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVQ=="
