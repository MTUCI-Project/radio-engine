package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"radio_engine/internal/config"
	"radio_engine/internal/playlist"
)

type Asset struct {
	Path string
	ETag string
	Size int64
}

type Store interface {
	Resolve(context.Context, playlist.MediaRef) (Asset, error)
	Ping(context.Context) error
}

type MinIOStore struct {
	client   *minio.Client
	cacheDir string
	maxBytes int64
	timeout  time.Duration
	retries  int

	mu       sync.Mutex
	inflight map[string]*download
}

type download struct {
	done  chan struct{}
	asset Asset
	err   error
}

func NewMinIOStore(cfg config.MinIO, cacheCfg config.Cache) (*MinIOStore, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Retries <= 0 {
		cfg.Retries = 3
	}
	if cacheCfg.Dir == "" {
		cacheCfg.Dir = filepath.Join(os.TempDir(), "radio-engine-cache")
	}
	if err := os.MkdirAll(cacheCfg.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("create media cache: %w", err)
	}
	return &MinIOStore{
		client:   client,
		cacheDir: cacheCfg.Dir,
		maxBytes: cacheCfg.MaxBytes,
		timeout:  cfg.Timeout,
		retries:  cfg.Retries,
		inflight: make(map[string]*download),
	}, nil
}

func (s *MinIOStore) Resolve(ctx context.Context, ref playlist.MediaRef) (Asset, error) {
	if err := ref.Validate(); err != nil {
		return Asset{}, err
	}
	key := cacheKey(ref)
	path := filepath.Join(s.cacheDir, key+".media")
	if info, err := os.Stat(path); err == nil {
		_ = os.Chtimes(path, time.Now(), time.Now())
		return Asset{Path: path, Size: info.Size()}, nil
	}

	s.mu.Lock()
	if active := s.inflight[key]; active != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Asset{}, ctx.Err()
		case <-active.done:
			return active.asset, active.err
		}
	}
	active := &download{done: make(chan struct{})}
	s.inflight[key] = active
	s.mu.Unlock()

	active.asset, active.err = s.download(ctx, ref, path)
	close(active.done)
	s.mu.Lock()
	delete(s.inflight, key)
	s.mu.Unlock()
	return active.asset, active.err
}

func (s *MinIOStore) download(ctx context.Context, ref playlist.MediaRef, target string) (Asset, error) {
	var lastErr error
	for attempt := 1; attempt <= s.retries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, s.timeout)
		opts := minio.GetObjectOptions{}
		if ref.VersionID != "" {
			opts.VersionID = ref.VersionID
		}
		object, err := s.client.GetObject(attemptCtx, ref.Bucket, ref.ObjectKey, opts)
		if err == nil {
			var stat minio.ObjectInfo
			stat, err = object.Stat()
			if err == nil {
				err = s.writeObject(attemptCtx, object, target)
			}
			_ = object.Close()
			if err == nil {
				cancel()
				s.evict()
				return Asset{Path: target, ETag: stat.ETag, Size: stat.Size}, nil
			}
		}
		cancel()
		lastErr = err
		if !sleep(ctx, time.Duration(attempt)*200*time.Millisecond) {
			return Asset{}, ctx.Err()
		}
	}
	return Asset{}, fmt.Errorf("download s3://%s/%s: %w", ref.Bucket, ref.ObjectKey, lastErr)
}

func (s *MinIOStore) writeObject(ctx context.Context, src io.Reader, target string) error {
	tmp, err := os.CreateTemp(s.cacheDir, ".download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	_, copyErr := copyWithContext(ctx, tmp, src)
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	return nil
}

func (s *MinIOStore) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	_, err := s.client.ListBuckets(ctx)
	return err
}

func (s *MinIOStore) evict() {
	if s.maxBytes <= 0 {
		return
	}
	entries, err := os.ReadDir(s.cacheDir)
	if err != nil {
		return
	}
	type cachedFile struct {
		path string
		size int64
		used time.Time
	}
	files := make([]cachedFile, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || filepath.Ext(entry.Name()) != ".media" {
			continue
		}
		total += info.Size()
		files = append(files, cachedFile{
			path: filepath.Join(s.cacheDir, entry.Name()),
			size: info.Size(),
			used: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].used.Before(files[j].used) })
	for _, file := range files {
		if total <= s.maxBytes {
			break
		}
		if os.Remove(file.path) == nil {
			total -= file.size
		}
	}
}

func cacheKey(ref playlist.MediaRef) string {
	sum := sha256.Sum256([]byte(ref.Bucket + "\x00" + ref.ObjectKey + "\x00" + ref.VersionID))
	return hex.EncodeToString(sum[:])
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 128*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			w, writeErr := dst.Write(buf[:n])
			written += int64(w)
			if writeErr != nil {
				return written, writeErr
			}
			if w != n {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

func sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
