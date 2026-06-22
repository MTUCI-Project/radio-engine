package audio

import "math"

type Mixer struct {
	profile      AnnouncementProfile
	duck         float32
	lowState     [Channels]float32
	highLowState [Channels]float32
	limiterGain  float32
}

func NewMixer(profile AnnouncementProfile) *Mixer {
	return &Mixer{profile: profile.Normalize(), limiterGain: 1}
}

func (m *Mixer) SetProfile(profile AnnouncementProfile) {
	m.profile = profile.Normalize()
}

func (m *Mixer) Mix(music, announcement []float32, announcementActive bool) []float32 {
	length := len(music)
	if len(announcement) > length {
		length = len(announcement)
	}
	if length == 0 {
		length = BlockFrames * Channels
	}
	out := make([]float32, length)
	target := float32(1)
	if announcementActive {
		target = DBToGain(m.profile.MusicGainDB)
	}
	rampMS := m.profile.ReleaseMS
	if announcementActive {
		rampMS = m.profile.AttackMS
	}
	rampSamples := float32(max(1, rampMS*SampleRate/1000*Channels))
	step := (target - m.duck) / rampSamples
	announcementGain := DBToGain(m.profile.AnnouncementGainDB)

	for i := 0; i < length; i++ {
		if (step > 0 && m.duck < target) || (step < 0 && m.duck > target) {
			m.duck += step
		} else {
			m.duck = target
		}
		var musicSample, announcementSample float32
		if i < len(music) {
			musicSample = music[i]
		}
		if i < len(announcement) {
			announcementSample = announcement[i]
		}
		if announcementActive && (m.profile.Low.Enabled || m.profile.Mid.Enabled || m.profile.High.Enabled) {
			musicSample = m.bandDuck(i%Channels, musicSample)
		} else {
			musicSample *= m.duck
		}
		out[i] = musicSample + announcementSample*announcementGain
	}
	m.limit(out)
	return out
}

func (m *Mixer) bandDuck(channel int, input float32) float32 {
	lowAlpha := onePoleAlpha(m.profile.LowCutHz)
	highAlpha := onePoleAlpha(m.profile.HighCutHz)
	m.lowState[channel] += lowAlpha * (input - m.lowState[channel])
	m.highLowState[channel] += highAlpha * (input - m.highLowState[channel])
	low := m.lowState[channel]
	high := input - m.highLowState[channel]
	mid := input - low - high
	lowGain, midGain, highGain := m.duck, m.duck, m.duck
	if m.profile.Low.Enabled {
		lowGain = DBToGain(m.profile.Low.GainDB)
	}
	if m.profile.Mid.Enabled {
		midGain = DBToGain(m.profile.Mid.GainDB)
	}
	if m.profile.High.Enabled {
		highGain = DBToGain(m.profile.High.GainDB)
	}
	return low*lowGain + mid*midGain + high*highGain
}

func (m *Mixer) limit(samples []float32) {
	threshold := DBToGain(m.profile.LimiterThresholdDB)
	var peak float32
	for _, sample := range samples {
		absolute := float32(math.Abs(float64(sample)))
		if absolute > peak {
			peak = absolute
		}
	}
	targetGain := float32(1)
	if peak > threshold && peak > 0 {
		targetGain = threshold / peak
	}
	if targetGain < m.limiterGain {
		m.limiterGain = targetGain
	} else {
		m.limiterGain += (targetGain - m.limiterGain) * 0.02
	}
	for i := range samples {
		samples[i] *= m.limiterGain
		if samples[i] > 1 {
			samples[i] = 1
		}
		if samples[i] < -1 {
			samples[i] = -1
		}
	}
}

func ApplyTransition(current, next []float32, progress float64, curve string) []float32 {
	length := len(current)
	if len(next) > length {
		length = len(next)
	}
	out := make([]float32, length)
	progress = clamp(progress, 0, 1)
	var currentGain, nextGain float64
	if curve == "linear" {
		currentGain = 1 - progress
		nextGain = progress
	} else {
		currentGain = math.Cos(progress * math.Pi / 2)
		nextGain = math.Sin(progress * math.Pi / 2)
	}
	for i := range out {
		if i < len(current) {
			out[i] += current[i] * float32(currentGain)
		}
		if i < len(next) {
			out[i] += next[i] * float32(nextGain)
		}
	}
	return out
}

func Fade(samples []float32, progress float64, fadeIn bool, curve string) []float32 {
	var gain float64
	if fadeIn {
		if curve == "linear" {
			gain = progress
		} else {
			gain = math.Sin(clamp(progress, 0, 1) * math.Pi / 2)
		}
	} else if curve == "linear" {
		gain = 1 - progress
	} else {
		gain = math.Cos(clamp(progress, 0, 1) * math.Pi / 2)
	}
	out := make([]float32, len(samples))
	for i := range samples {
		out[i] = samples[i] * float32(gain)
	}
	return out
}

func onePoleAlpha(cutoff float64) float32 {
	return float32(1 - math.Exp(-2*math.Pi*cutoff/SampleRate))
}
