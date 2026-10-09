package main

import (
	"encoding/json"
	"testing"
	"time"
)

// The sampler has to report CPU as a *rate*. A single absolute reading of
// "42 seconds of CPU since start" says nothing about how busy the process
// is now, which is the only thing a dashboard is asked.
func TestSamplerDifferentiatesCpuOverTheInterval(t *testing.T) {
	m := &metricsSampler{lastCPU: 10, lastAt: time.Now().Add(-2 * time.Second)}

	// Pin the "current" CPU reading by faking the elapsed window: two
	// seconds of wall clock against however much CPU this process has
	// really used. What is asserted is the shape, not the value.
	got := m.sample(0, time.Now())
	if got.CPUSeconds <= 0 {
		t.Fatalf("no CPU time reported: %+v", got)
	}
	if got.CPUPercent < 0 {
		t.Fatalf("negative CPU percentage: %v", got.CPUPercent)
	}

	// The window is closed by sampling, so an immediate second sample
	// covers almost no wall clock and must not report the whole history
	// again as if it had all happened just now.
	second := m.sample(0, time.Now())
	if second.CPUSeconds < got.CPUSeconds {
		t.Fatalf("CPU seconds went backwards: %v then %v", got.CPUSeconds, second.CPUSeconds)
	}
}

// A restarted or unmeasurable counter must never produce a negative rate,
// which would show up in Grafana as a spike when read as a counter.
func TestSamplerNeverReportsNegativeCpu(t *testing.T) {
	m := &metricsSampler{lastCPU: 1e9, lastAt: time.Now().Add(-time.Second)}
	if got := m.sample(0, time.Now()); got.CPUPercent != 0 {
		t.Fatalf("expected 0%% from a counter that went backwards, got %v", got.CPUPercent)
	}
}

func TestSamplerAveragesTickDurationAndDrainsIt(t *testing.T) {
	m := newMetricsSampler()
	m.observeTick(10 * time.Millisecond)
	m.observeTick(20 * time.Millisecond)

	if got := m.sample(0, time.Now()).TickSeconds; got != 0.015 {
		t.Fatalf("tick average %v, want 0.015", got)
	}
	// Each sample describes the interval since the last one, so the ticks
	// already reported must not be counted again in the next window.
	if got := m.sample(0, time.Now()).TickSeconds; got != 0 {
		t.Fatalf("tick total was not drained: %v", got)
	}
}

func TestSamplerReportsMemoryAndFleetSize(t *testing.T) {
	got := newMetricsSampler().sample(200, time.Now())

	if got.HeapBytes <= 0 || got.HeapSysBytes <= 0 {
		t.Fatalf("no heap reported: %+v", got)
	}
	if got.Goroutines <= 0 {
		t.Fatalf("no goroutines reported: %d", got.Goroutines)
	}
	if got.Npcs != 200 {
		t.Fatalf("fleet size %d, want 200", got.Npcs)
	}
	// Resident size is only measurable where /proc exists. It is allowed
	// to be 0 (ships-go leaves the series out then), never negative.
	if got.ResidentBytes < 0 {
		t.Fatalf("negative resident size: %v", got.ResidentBytes)
	}
}

// The field names are the contract with ships-go's NpcMetricsData: the two
// structs are hand-mirrored, so a rename on one side that is not made on
// the other silently zeroes a metric rather than failing to build.
func TestMetricsMessageWireFormat(t *testing.T) {
	raw, err := json.Marshal(npcMetricsMsg{
		EventName:     "npcMetrics",
		CpuSeconds:    1.5,
		CpuPercent:    2.5,
		ResidentBytes: 3,
		HeapBytes:     4,
		HeapSysBytes:  5,
		Goroutines:    6,
		Npcs:          7,
		TickSeconds:   8.5,
	})
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	want := `{"eventName":"npcMetrics","cpuSeconds":1.5,"cpuPercent":2.5,` +
		`"residentBytes":3,"heapBytes":4,"heapSysBytes":5,"goroutines":6,` +
		`"npcs":7,"tickSeconds":8.5}`
	if string(raw) != want {
		t.Fatalf("wire format drifted:\n got %s\nwant %s", raw, want)
	}
}
