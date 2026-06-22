package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

type Metrics struct {
	Stations          prometheus.Gauge
	EventBacklog      prometheus.Gauge
	MediaFailures     *prometheus.CounterVec
	TrackEvents       *prometheus.CounterVec
	Announcements     *prometheus.CounterVec
	IcecastReconnects *prometheus.CounterVec
	AudioUnderruns    *prometheus.CounterVec
	FFmpegProcesses   prometheus.Gauge
}

func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Stations: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "radio_engine", Name: "stations", Help: "Active station runtimes.",
		}),
		EventBacklog: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "radio_engine", Name: "event_backlog", Help: "Events waiting for Redis.",
		}),
		MediaFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "radio_engine", Name: "media_failures_total", Help: "Media fetch/decode failures.",
		}, []string{"station_id", "stage"}),
		TrackEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "radio_engine", Name: "track_events_total", Help: "Track lifecycle events.",
		}, []string{"station_id", "event"}),
		Announcements: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "radio_engine", Name: "announcement_events_total", Help: "Announcement lifecycle events.",
		}, []string{"station_id", "event"}),
		IcecastReconnects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "radio_engine", Name: "icecast_reconnects_total", Help: "Icecast reconnect attempts.",
		}, []string{"station_id"}),
		AudioUnderruns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "radio_engine", Name: "audio_underruns_total", Help: "Audio blocks that could not be rendered in time.",
		}, []string{"station_id"}),
		FFmpegProcesses: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "radio_engine", Name: "ffmpeg_processes", Help: "Active FFmpeg processes.",
		}),
	}
	reg.MustRegister(m.Stations, m.EventBacklog, m.MediaFailures, m.TrackEvents, m.Announcements, m.IcecastReconnects, m.AudioUnderruns, m.FFmpegProcesses)
	return m
}

func Handler() http.Handler { return promhttp.Handler() }
