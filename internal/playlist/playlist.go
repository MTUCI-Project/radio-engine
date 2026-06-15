package playlist

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Track struct {
	Path  string
	Title string
}

type Playlist struct {
	mu     sync.Mutex
	tracks []Track
	next   int
}

func FromDir(dir string) (*Playlist, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var tracks []Track
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".mp3") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		tracks = append(tracks, Track{
			Path:  path,
			Title: strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
		})
	}

	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].Path < tracks[j].Path
	})

	if len(tracks) == 0 {
		return nil, fmt.Errorf("no .mp3 files in %s", dir)
	}
	return &Playlist{tracks: tracks}, nil
}

func Single(path string) *Playlist {
	return &Playlist{
		tracks: []Track{{
			Path:  path,
			Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		}},
	}
}

func (p *Playlist) Next() Track {
	p.mu.Lock()
	defer p.mu.Unlock()

	track := p.tracks[p.next]
	p.next = (p.next + 1) % len(p.tracks)
	return track
}

func (p *Playlist) Tracks() []Track {
	p.mu.Lock()
	defer p.mu.Unlock()

	tracks := make([]Track, len(p.tracks))
	copy(tracks, p.tracks)
	return tracks
}
