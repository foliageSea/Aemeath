package voice

const (
	preRollSamples      = sampleRate * 2
	speechLeadSamples   = sampleRate * 3 / 10
	maxUtteranceSamples = sampleRate * 30
	startTimeoutSamples = sampleRate * 5
	manualWindowSamples = sampleRate * 6
	endSilenceSamples   = sampleRate * 8 / 10
)

// sampleRing retains only the latest samples, including the suffix arriving
// between the actual keyword endpoint and the streaming detector's decision.
type sampleRing struct {
	data        [preRollSamples]int16
	next, count int
}

func (r *sampleRing) add(p []int16) {
	for _, s := range p {
		r.data[r.next] = s
		r.next = (r.next + 1) % len(r.data)
		if r.count < len(r.data) {
			r.count++
		}
	}
}
func (r *sampleRing) tail(n int) []int16 {
	if n > r.count {
		n = r.count
	}
	if n < 0 {
		n = 0
	}
	p := make([]int16, n)
	start := (r.next - n + len(r.data)) % len(r.data)
	for i := range p {
		p[i] = r.data[(start+i)%len(r.data)]
	}
	return p
}
func (r *sampleRing) reset() { r.next = 0; r.count = 0 }

type capture struct {
	pcm              []int16
	elapsed, silence int
	speech           bool
}

func (c *capture) feed(p []int16, speech bool) (done, empty bool) {
	c.elapsed += len(p)
	c.pcm = append(c.pcm, p...)
	if speech {
		c.speech = true
		c.silence = 0
	} else {
		c.silence += len(p)
	}
	if !c.speech {
		if len(c.pcm) > speechLeadSamples {
			copy(c.pcm, c.pcm[len(c.pcm)-speechLeadSamples:])
			c.pcm = c.pcm[:speechLeadSamples]
		}
		return c.elapsed >= startTimeoutSamples, true
	}
	if len(c.pcm) >= maxUtteranceSamples {
		c.pcm = c.pcm[:maxUtteranceSamples]
		return true, false
	}
	return c.silence >= endSilenceSamples, false
}
