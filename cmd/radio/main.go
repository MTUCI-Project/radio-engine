package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type config struct {
	IcecastURL     string
	SourceUser     string
	SourcePass     string
	Mount          string
	TrackPath      string
	BitrateKbps    int
	ReconnectDelay time.Duration
}

func main() {
	cfg := loadConfig()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("radio v0.1: streaming %q to %s%s", cfg.TrackPath, cfg.IcecastURL, cfg.Mount)
	for {
		if err := streamOnce(ctx, cfg); err != nil {
			if errors.Is(err, context.Canceled) {
				log.Println("shutdown requested")
				return
			}
			log.Printf("stream ended with error: %v", err)
		}

		select {
		case <-ctx.Done():
			log.Println("shutdown requested")
			return
		case <-time.After(cfg.ReconnectDelay):
			log.Printf("reconnecting in %s", cfg.ReconnectDelay)
		}
	}
}

func loadConfig() config {
	bitrate := envInt("STREAM_BITRATE_KBPS", 128)
	track := env("TRACK_PATH", "")
	if track == "" {
		var err error
		track, err = firstMP3("tracklist")
		if err != nil {
			log.Fatalf("TRACK_PATH is not set and no mp3 was found in tracklist: %v", err)
		}
	}

	mount := env("ICECAST_MOUNT", "/radio.mp3")
	if !strings.HasPrefix(mount, "/") {
		mount = "/" + mount
	}

	return config{
		IcecastURL:     strings.TrimRight(env("ICECAST_URL", "http://127.0.0.1:8000"), "/"),
		SourceUser:     env("ICECAST_SOURCE_USER", "source"),
		SourcePass:     env("ICECAST_SOURCE_PASSWORD", "hackme"),
		Mount:          mount,
		TrackPath:      track,
		BitrateKbps:    bitrate,
		ReconnectDelay: time.Duration(envInt("RECONNECT_DELAY_SECONDS", 3)) * time.Second,
	}
}

func streamOnce(ctx context.Context, cfg config) error {
	file, err := os.Open(cfg.TrackPath)
	if err != nil {
		return fmt.Errorf("open track: %w", err)
	}
	defer file.Close()

	conn, err := dialIcecast(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("connected, mount is available at %s%s", cfg.IcecastURL, cfg.Mount)
	return copyThrottled(ctx, conn, file, cfg.BitrateKbps)
}

func dialIcecast(ctx context.Context, cfg config) (net.Conn, error) {
	streamURL, err := url.Parse(cfg.IcecastURL + cfg.Mount)
	if err != nil {
		return nil, fmt.Errorf("parse icecast url: %w", err)
	}
	if streamURL.Scheme != "http" {
		return nil, fmt.Errorf("unsupported icecast scheme %q: only http is supported in v0.1", streamURL.Scheme)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(cfg.SourceUser + ":" + cfg.SourcePass))

	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", streamURL.Host)
	if err != nil {
		return nil, fmt.Errorf("dial icecast: %w", err)
	}

	headers := strings.Builder{}
	headers.WriteString(fmt.Sprintf("PUT %s HTTP/1.1\r\n", streamURL.RequestURI()))
	headers.WriteString(fmt.Sprintf("Host: %s\r\n", streamURL.Host))
	headers.WriteString("Authorization: Basic " + auth + "\r\n")
	headers.WriteString("Content-Type: audio/mpeg\r\n")
	headers.WriteString("Ice-Name: Radio Engine V0.1\r\n")
	headers.WriteString("Ice-Description: Go proof-of-concept MP3 stream\r\n")
	headers.WriteString("Ice-Genre: Various\r\n")
	headers.WriteString("Ice-Public: 0\r\n")
	headers.WriteString("User-Agent: radio-engine/0.1\r\n")
	headers.WriteString("Connection: close\r\n")
	headers.WriteString("\r\n")

	if _, err := io.WriteString(conn, headers.String()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send source headers: %w", err)
	}

	reader := bufio.NewReader(conn)
	req := &http.Request{Method: http.MethodPut, URL: streamURL}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read icecast response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		conn.Close()
		return nil, fmt.Errorf("icecast rejected source connection: %s", resp.Status)
	}

	return &bufferedConn{Conn: conn, reader: reader}, nil
}

func copyThrottled(ctx context.Context, dst io.Writer, src io.Reader, bitrateKbps int) error {
	if bitrateKbps <= 0 {
		bitrateKbps = 128
	}

	bytesPerSecond := float64(bitrateKbps*1000) / 8
	start := time.Now()
	var sent int64
	buf := make([]byte, 16*1024)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			sent += int64(written)
			if writeErr != nil {
				return fmt.Errorf("write stream: %w", writeErr)
			}
			if written != n {
				return io.ErrShortWrite
			}

			targetElapsed := time.Duration(float64(sent) / bytesPerSecond * float64(time.Second))
			if sleep := targetElapsed - time.Since(start); sleep > 0 {
				timer := time.NewTimer(sleep)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}

		if errors.Is(readErr, io.EOF) {
			log.Println("track finished, looping")
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read track: %w", readErr)
		}
	}
}

func firstMP3(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".mp3") {
			return filepath.Join(dir, entry.Name()), nil
		}
	}
	return "", fmt.Errorf("no .mp3 files in %s", dir)
}

func env(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("invalid %s=%q, using %d", name, value, fallback)
		return fallback
	}
	return parsed
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
