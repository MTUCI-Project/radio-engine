package icecast

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	URL         string
	SourceUser  string
	SourcePass  string
	Mount       string
	Name        string
	Description string
	Genre       string
	Public      bool
}

type Source struct {
	mu  sync.RWMutex
	cfg Config
}

func NewSource(cfg Config) *Source {
	return &Source{cfg: cfg}
}

func (s *Source) MountURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.URL + s.cfg.Mount
}

func (s *Source) Update(cfg Config) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

func (s *Source) Connect(ctx context.Context) (io.WriteCloser, error) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	streamURL, err := url.Parse(cfg.URL + cfg.Mount)
	if err != nil {
		return nil, fmt.Errorf("parse icecast url: %w", err)
	}
	if streamURL.Scheme != "http" {
		return nil, fmt.Errorf("unsupported icecast scheme %q: only http is supported", streamURL.Scheme)
	}

	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", streamURL.Host)
	if err != nil {
		return nil, fmt.Errorf("dial icecast: %w", err)
	}

	if err := writeHeaders(conn, streamURL, cfg); err != nil {
		conn.Close()
		return nil, err
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

	return conn, nil
}

func writeHeaders(w io.Writer, streamURL *url.URL, cfg Config) error {
	auth := base64.StdEncoding.EncodeToString([]byte(cfg.SourceUser + ":" + cfg.SourcePass))
	public := "0"
	if cfg.Public {
		public = "1"
	}

	headers := strings.Builder{}
	headers.WriteString(fmt.Sprintf("PUT %s HTTP/1.1\r\n", streamURL.RequestURI()))
	headers.WriteString(fmt.Sprintf("Host: %s\r\n", streamURL.Host))
	headers.WriteString("Authorization: Basic " + auth + "\r\n")
	headers.WriteString("Content-Type: audio/mpeg\r\n")
	headers.WriteString("Ice-Name: " + cfg.Name + "\r\n")
	headers.WriteString("Ice-Description: " + cfg.Description + "\r\n")
	headers.WriteString("Ice-Genre: " + cfg.Genre + "\r\n")
	headers.WriteString("Ice-Public: " + public + "\r\n")
	headers.WriteString("User-Agent: radio-engine/0.2\r\n")
	headers.WriteString("Connection: close\r\n")
	headers.WriteString("\r\n")

	if _, err := io.WriteString(w, headers.String()); err != nil {
		return fmt.Errorf("send source headers: %w", err)
	}
	return nil
}
