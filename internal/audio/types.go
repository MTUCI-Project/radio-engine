package audio

import (
	"errors"
	"math"
)

const (
	SampleRate     = 48000
	Channels       = 2
	BlockFrames    = 960 // 20 ms
	BytesPerSample = 4
)

type OutputConfig struct {
	BitrateKbps int `json:"bitrateKbps"`
}

func (c OutputConfig) Normalize() OutputConfig {
	if c.BitrateKbps <= 0 {
		c.BitrateKbps = 192
	}
	if c.BitrateKbps < 64 {
		c.BitrateKbps = 64
	}
	if c.BitrateKbps > 320 {
		c.BitrateKbps = 320
	}
	return c
}

type BandProfile struct {
	Enabled bool    `json:"enabled"`
	GainDB  float64 `json:"gainDb"`
}

type AnnouncementProfile struct {
	MusicGainDB        float64     `json:"musicGainDb"`
	AnnouncementGainDB float64     `json:"announcementGainDb"`
	AttackMS           int         `json:"attackMs"`
	ReleaseMS          int         `json:"releaseMs"`
	LowCutHz           float64     `json:"lowCutHz"`
	HighCutHz          float64     `json:"highCutHz"`
	Low                BandProfile `json:"low"`
	Mid                BandProfile `json:"mid"`
	High               BandProfile `json:"high"`
	LimiterThresholdDB float64     `json:"limiterThresholdDb"`
}

func DefaultAnnouncementProfile() AnnouncementProfile {
	return AnnouncementProfile{
		MusicGainDB:        -12,
		AnnouncementGainDB: 0,
		AttackMS:           180,
		ReleaseMS:          450,
		LowCutHz:           250,
		HighCutHz:          3500,
		LimiterThresholdDB: -1,
	}
}

func (p AnnouncementProfile) Normalize() AnnouncementProfile {
	defaults := DefaultAnnouncementProfile()
	if p.AttackMS <= 0 {
		p.AttackMS = defaults.AttackMS
	}
	if p.ReleaseMS <= 0 {
		p.ReleaseMS = defaults.ReleaseMS
	}
	if p.LowCutHz <= 0 {
		p.LowCutHz = defaults.LowCutHz
	}
	if p.HighCutHz <= p.LowCutHz {
		p.HighCutHz = defaults.HighCutHz
	}
	if p.LimiterThresholdDB == 0 {
		p.LimiterThresholdDB = defaults.LimiterThresholdDB
	}
	p.MusicGainDB = clamp(p.MusicGainDB, -60, 12)
	p.AnnouncementGainDB = clamp(p.AnnouncementGainDB, -60, 12)
	p.LimiterThresholdDB = clamp(p.LimiterThresholdDB, -20, 0)
	p.Low.GainDB = clamp(p.Low.GainDB, -60, 12)
	p.Mid.GainDB = clamp(p.Mid.GainDB, -60, 12)
	p.High.GainDB = clamp(p.High.GainDB, -60, 12)
	return p
}

func ValidateSamples(samples []float32) error {
	if len(samples)%Channels != 0 {
		return errors.New("PCM sample count must be divisible by channel count")
	}
	return nil
}

func DBToGain(db float64) float32 {
	return float32(math.Pow(10, db/20))
}

func clamp(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
