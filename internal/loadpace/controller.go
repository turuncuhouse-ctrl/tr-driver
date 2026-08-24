package loadpace

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

var ErrOverloaded = errors.New("server overloaded")

// Snapshot is returned to clients so they can slow down or wait.
type Snapshot struct {
	CPUPercent      float64 `json:"cpuPercent"`
	ActiveUploads   int     `json:"activeUploads"`
	MaxConcurrent   int     `json:"maxConcurrent"`
	DelayMs         int     `json:"delayMs"`
	Mode            string  `json:"mode"` // fast | normal | slow | pause
	AcceptUploads   bool    `json:"acceptUploads"`
	RetryAfterSec   int     `json:"retryAfterSec"`
	RecommendedBatch int    `json:"recommendedBatch"`
}

// Controller limits concurrent uploads and advises clients based on CPU + queue pressure.
type Controller struct {
	maxSlots int
	sem      chan struct{}
	active   atomic.Int32

	mu         sync.Mutex
	cpuPercent float64
	prevIdle   uint64
	prevTotal  uint64
	haveSample bool
}

func New(maxConcurrent int) *Controller {
	if maxConcurrent < 1 {
		maxConcurrent = 3
	}
	c := &Controller{
		maxSlots: maxConcurrent,
		sem:      make(chan struct{}, maxConcurrent),
	}
	c.sampleCPU()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for range t.C {
			c.sampleCPU()
		}
	}()
	return c
}

func (c *Controller) Snapshot() Snapshot {
	cpu := c.currentCPU()
	active := int(c.active.Load())
	maxSlots := c.maxSlots
	occupancy := 0.0
	if maxSlots > 0 {
		occupancy = float64(active) / float64(maxSlots)
	}

	mode := "normal"
	delayMs := 350
	accept := true
	retryAfter := 0
	batch := 8

	switch {
	case cpu >= 92 || (occupancy >= 1.0 && cpu >= 80):
		mode = "pause"
		delayMs = 3000
		accept = false
		retryAfter = 5
		batch = 1
	case cpu >= 85 || occupancy >= 0.85:
		mode = "slow"
		delayMs = 2000
		batch = 2
		retryAfter = 2
	case cpu <= 50 && occupancy < 0.5:
		mode = "fast"
		delayMs = 80
		batch = 15
	default:
		mode = "normal"
		delayMs = 400
		batch = 8
	}

	return Snapshot{
		CPUPercent:       round1(cpu),
		ActiveUploads:    active,
		MaxConcurrent:    maxSlots,
		DelayMs:          delayMs,
		Mode:             mode,
		AcceptUploads:    accept,
		RetryAfterSec:    retryAfter,
		RecommendedBatch: batch,
	}
}

// Acquire reserves an upload slot. Returns ErrOverloaded when the server is pausing new work.
func (c *Controller) Acquire(ctx context.Context) (release func(), err error) {
	snap := c.Snapshot()
	if !snap.AcceptUploads {
		// If a slot is free, still allow finishing work during mild pause;
		// only hard-reject when fully saturated or critically hot.
		if c.active.Load() >= int32(c.maxSlots) || snap.CPUPercent >= 95 {
			return nil, ErrOverloaded
		}
	}

	select {
	case c.sem <- struct{}{}:
		c.active.Add(1)
		return func() {
			<-c.sem
			c.active.Add(-1)
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Wait briefly for a free slot instead of hammering the disk.
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case c.sem <- struct{}{}:
		c.active.Add(1)
		return func() {
			<-c.sem
			c.active.Add(-1)
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrOverloaded
	}
}

func (c *Controller) currentCPU() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cpuPercent
}

func (c *Controller) sampleCPU() {
	idle, total, ok := readCPUTimes()
	if !ok {
		// Fallback: approximate pressure from goroutines when /proc is missing (e.g. Windows).
		c.mu.Lock()
		n := float64(runtime.NumGoroutine())
		// Map rough goroutine pressure to 0–100 for local/dev.
		approx := n / 2
		if approx > 100 {
			approx = 100
		}
		c.cpuPercent = approx * 0.15 // keep low unless many goroutines
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.haveSample && total > c.prevTotal {
		idleDelta := float64(idle - c.prevIdle)
		totalDelta := float64(total - c.prevTotal)
		if totalDelta > 0 {
			usage := (1.0 - idleDelta/totalDelta) * 100.0
			if usage < 0 {
				usage = 0
			}
			if usage > 100 {
				usage = 100
			}
			// EMA so short spikes don't thrash clients.
			if c.cpuPercent <= 0 {
				c.cpuPercent = usage
			} else {
				c.cpuPercent = c.cpuPercent*0.6 + usage*0.4
			}
		}
	}
	c.prevIdle = idle
	c.prevTotal = total
	c.haveSample = true
}

func readCPUTimes() (idle, total uint64, ok bool) {
	if runtime.GOOS != "linux" {
		return 0, 0, false
	}
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	// first line: cpu  user nice system idle iowait irq softirq steal ...
	var line string
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			line = string(data[:i])
			break
		}
	}
	if len(line) < 4 || line[:3] != "cpu" {
		return 0, 0, false
	}
	fields := splitFields(line)
	if len(fields) < 5 {
		return 0, 0, false
	}
	var vals []uint64
	for _, f := range fields[1:] {
		v, err := parseUint(f)
		if err != nil {
			return 0, 0, false
		}
		vals = append(vals, v)
	}
	for _, v := range vals {
		total += v
	}
	idle = vals[3]
	if len(vals) > 4 {
		idle += vals[4] // iowait
	}
	return idle, total, true
}

func splitFields(s string) []string {
	out := make([]string, 0, 12)
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func parseUint(s string) (uint64, error) {
	var n uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errors.New("bad uint")
		}
		n = n*10 + uint64(c-'0')
	}
	return n, nil
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
