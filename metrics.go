package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file measures how hard this process is working, so it can be shipped
// to ships-go and end up on the same Grafana dashboard as everything else.
//
// ships-npc has no HTTP server of its own and is not reachable from anywhere
// (it is a websocket *client*, on localhost, behind ships-go). Giving it a
// port just to be scraped would mean a listener, a firewall rule and a
// Prometheus target for a process that already has a live, authenticated
// connection to a service that is scraped. So the sample travels up the
// existing websocket and ships-go re-exports it; see NpcMetrics.go there.
//
// Everything here is standard library on purpose: pulling in the Prometheus
// client just to produce a handful of floats would add a dependency to a
// service that never exposes them itself.

// processMetrics is one sample of this process's own resource usage.
//
// A resident size of 0 means "could not be measured" (no /proc, i.e. not
// Linux) rather than "no memory in use"; ships-go leaves that series out
// entirely rather than publishing a zero that would read as a real value.
type processMetrics struct {
	// CPUSeconds is total CPU time burned since start, user + system. It
	// only ever grows, which is what lets Grafana rate() it.
	CPUSeconds float64
	// CPUPercent is that same number differentiated over wall-clock time
	// since the previous sample, as a percentage of *one* core: 100 means
	// one core saturated, and a busy multi-core process can exceed it.
	CPUPercent float64
	// ResidentBytes is real memory held right now, as the OS sees it.
	// HeapBytes/HeapSysBytes are Go's view: live heap, and heap reserved
	// from the OS. Resident is the number that matters for a container
	// limit; the heap pair is what tells you whether a rise is the game or
	// the runtime.
	ResidentBytes float64
	HeapBytes     float64
	HeapSysBytes  float64
	Goroutines    int
	// Npcs is how many NPCs were being simulated at sample time, and
	// TickSeconds the average time one simulation tick took over the
	// interval. Together with CPU they answer the only question worth
	// asking of this service: how many ships can it fly before it stops
	// keeping up? A tick that approaches NPC_TICK_INTERVAL_MS is one that
	// is about to start falling behind.
	Npcs        int
	TickSeconds float64
}

// metricsSampler turns the counters the OS and runtime expose into a
// sample. It holds the previous CPU reading because CPU usage is a rate:
// a single absolute reading of "42 seconds of CPU" says nothing about how
// busy the process is now.
type metricsSampler struct {
	mu        sync.Mutex
	lastCPU   float64
	lastAt    time.Time
	ticks     int
	tickTotal time.Duration
}

func newMetricsSampler() *metricsSampler {
	return &metricsSampler{lastCPU: processCPUSeconds(), lastAt: time.Now()}
}

// observeTick records how long one simulation tick took. Called from the
// tick loop, so it must stay as cheap as an add.
func (m *metricsSampler) observeTick(d time.Duration) {
	m.mu.Lock()
	m.ticks++
	m.tickTotal += d
	m.mu.Unlock()
}

// sample reads the current values and closes off the interval since the
// last call: CPU is differentiated and the tick average is drained, so
// every sample describes the period since the previous one rather than
// since the process started.
func (m *metricsSampler) sample(npcs int, now time.Time) processMetrics {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	cpu := processCPUSeconds()

	m.mu.Lock()
	elapsed := now.Sub(m.lastAt).Seconds()
	percent := 0.0
	if elapsed > 0 && cpu >= m.lastCPU {
		percent = (cpu - m.lastCPU) / elapsed * 100
	}
	tickSeconds := 0.0
	if m.ticks > 0 {
		tickSeconds = (m.tickTotal / time.Duration(m.ticks)).Seconds()
	}
	m.lastCPU, m.lastAt = cpu, now
	m.ticks, m.tickTotal = 0, 0
	m.mu.Unlock()

	return processMetrics{
		CPUSeconds:    cpu,
		CPUPercent:    percent,
		ResidentBytes: residentBytes(),
		HeapBytes:     float64(mem.HeapAlloc),
		HeapSysBytes:  float64(mem.HeapSys),
		Goroutines:    runtime.NumGoroutine(),
		Npcs:          npcs,
		TickSeconds:   tickSeconds,
	}
}

// processCPUSeconds is user + system CPU time for this process. getrusage
// is used rather than parsing /proc/self/stat because it needs no file,
// no clock-tick conversion and works the same on every unix.
func processCPUSeconds() float64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	return timevalSeconds(usage.Utime) + timevalSeconds(usage.Stime)
}

func timevalSeconds(tv syscall.Timeval) float64 {
	return float64(tv.Sec) + float64(tv.Usec)/1e6
}

// residentBytes is the process's current resident set size.
//
// Deliberately not getrusage's Maxrss: that is the *peak* since start, so
// it never falls and a single spike would leave the graph flat at the high
// water mark forever. The second field of /proc/self/statm is the live
// figure, in pages. Returns 0 where /proc does not exist.
func residentBytes() float64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0
	}
	return pages * float64(os.Getpagesize())
}
