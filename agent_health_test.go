package lifecycled

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestAgentHealthConfigValidate(t *testing.T) {
	config := testAgentHealthConfig("http://127.0.0.1:39001", 1)
	if err := config.Validate(); runtime.GOOS == "linux" && err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}

	config.ExpectedWorkers = 0
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "expected-workers must be positive") {
		t.Errorf("Validate() error = %v, want an expected-workers error", err)
	}

	config = testAgentHealthConfig("http://example.com", 1)
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Errorf("Validate() error = %v, want a loopback error", err)
	}
}

func TestAgentHealthProbe(t *testing.T) {
	tests := []struct {
		name         string
		workers      []int
		workerStatus map[int]int
		statusStatus int
		wantState    agentHealthState
		wantHealthy  int
	}{
		{
			name:         "all workers healthy",
			workers:      []int{1, 2},
			workerStatus: map[int]int{1: http.StatusOK, 2: http.StatusOK},
			wantState:    agentHealthHealthy,
			wantHealthy:  2,
		},
		{
			name:         "all workers have failed heartbeats",
			workers:      []int{1, 2},
			workerStatus: map[int]int{1: http.StatusInternalServerError, 2: http.StatusInternalServerError},
			wantState:    agentHealthUnhealthy,
		},
		{
			name:         "one worker is degraded",
			workers:      []int{1, 2},
			workerStatus: map[int]int{1: http.StatusOK, 2: http.StatusInternalServerError},
			wantState:    agentHealthDegraded,
			wantHealthy:  1,
		},
		{
			name:         "worker count mismatch is local failure",
			workers:      []int{1},
			workerStatus: map[int]int{1: http.StatusOK},
			wantState:    agentHealthLocalFailure,
		},
		{
			name:         "unexpected worker status is local failure",
			workers:      []int{1, 2},
			workerStatus: map[int]int{1: http.StatusOK, 2: http.StatusNotFound},
			wantState:    agentHealthLocalFailure,
			wantHealthy:  1,
		},
		{
			name:         "status endpoint failure is local failure",
			workers:      []int{1, 2},
			workerStatus: map[int]int{},
			statusStatus: http.StatusServiceUnavailable,
			wantState:    agentHealthLocalFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newAgentHealthServer(tt.workers, tt.workerStatus, tt.statusStatus)
			defer server.Close()

			config := testAgentHealthConfig(server.URL, 2)
			listener := newAgentHealthListener("i-test", config, server.Client(), staticMemoryObserver{}, fakePoweroff{})
			got := listener.probe(t.Context())
			if got.state != tt.wantState {
				t.Errorf("probe() state = %s, want %s (reason: %s)", got.state, tt.wantState, got.reason)
			}
			if got.healthyWorkers != tt.wantHealthy {
				t.Errorf("probe() healthy workers = %d, want %d", got.healthyWorkers, tt.wantHealthy)
			}
		})
	}
}

func TestHealthFailureTracker(t *testing.T) {
	start := time.Unix(100, 0)
	tracker := healthFailureTracker{
		startedAt:         start,
		startupGrace:      5 * time.Minute,
		unhealthyDuration: 2 * time.Minute,
	}

	checks := []struct {
		at         time.Duration
		actionable bool
		want       bool
	}{
		{at: 4 * time.Minute, actionable: true, want: false},
		{at: 5 * time.Minute, actionable: true, want: false},
		{at: 6*time.Minute + 59*time.Second, actionable: true, want: false},
		{at: 7 * time.Minute, actionable: true, want: true},
		{at: 8 * time.Minute, actionable: false, want: false},
		{at: 8*time.Minute + time.Second, actionable: true, want: false},
	}
	for _, check := range checks {
		if got := tracker.Observe(start.Add(check.at), check.actionable); got != check.want {
			t.Errorf("Observe(%s, %t) = %t, want %t", check.at, check.actionable, got, check.want)
		}
	}
}

func TestCgroupMemoryObserver(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFiles(t, dir, "oom 3\noom_kill 2\n", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=1.25 avg60=0.00 avg300=0.00 total=0\n")
	observer := newCgroupMemoryObserver(dir)

	got, err := observer.Observe()
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if got.oom || got.fullPressure != 1.25 {
		t.Errorf("Observe() = %+v, want existing OOM counters to establish a baseline and full pressure 1.25", got)
	}

	writeMemoryFiles(t, dir, "oom 4\noom_kill 3\n", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=24.50 avg60=0.00 avg300=0.00 total=0\n")
	got, err = observer.Observe()
	if err != nil {
		t.Fatalf("Observe() after OOM error = %v", err)
	}
	if !got.oom || got.fullPressure != 24.5 {
		t.Errorf("Observe() after OOM = %+v, want OOM and full pressure 24.5", got)
	}

	got, err = observer.Observe()
	if err != nil {
		t.Fatalf("Observe() repeated error = %v", err)
	}
	if got.oom {
		t.Error("Observe() reported the same OOM counter increment twice")
	}
}

func TestAgentHealthListenerFailureGates(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		memory     memoryObservation
		wantNotice bool
	}{
		{
			name:       "unreachable local server emits notice",
			status:     http.StatusServiceUnavailable,
			wantNotice: true,
		},
		{
			name:   "heartbeat failure without memory evidence does not emit notice",
			status: http.StatusInternalServerError,
		},
		{
			name:       "heartbeat failure with OOM evidence emits notice",
			status:     http.StatusInternalServerError,
			memory:     memoryObservation{oom: true, reason: "test"},
			wantNotice: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newAgentHealthServer([]int{1}, map[int]int{1: tt.status}, func() int {
				if tt.status == http.StatusServiceUnavailable {
					return http.StatusServiceUnavailable
				}
				return http.StatusOK
			}())
			defer server.Close()

			config := testAgentHealthConfig(server.URL, 1)
			config.CheckInterval = 5 * time.Millisecond
			config.StartupGrace = time.Nanosecond
			config.UnhealthyDuration = 15 * time.Millisecond
			listener := newAgentHealthListener(
				"i-test",
				config,
				server.Client(),
				staticMemoryObserver{observation: tt.memory},
				fakePoweroff{},
			)

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			notices := make(chan TerminationNotice, 1)
			logger := logrus.New()
			logger.SetOutput(io.Discard)
			if err := listener.Start(ctx, notices, logrus.NewEntry(logger)); err != nil {
				t.Fatalf("Start() error = %v", err)
			}

			select {
			case notice := <-notices:
				if !tt.wantNotice {
					t.Fatalf("unexpected notice %q", notice.Type())
				}
				if got := notice.Type(); got != "agent-health" {
					t.Errorf("notice type = %q, want agent-health", got)
				}
			default:
				if tt.wantNotice {
					t.Fatal("expected an agent-health notice, got none")
				}
			}
		})
	}
}

func TestAgentHealthTerminationNotice(t *testing.T) {
	sentinel := errors.New("operation not permitted")
	poweroff := fakePoweroff{err: sentinel}
	notice := agentHealthTerminationNotice{
		instanceID: "i-test",
		reason:     "agent unavailable",
		poweroff:   poweroff,
	}

	err := notice.Handle(t.Context(), nil, nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Handle() error = %v, want it to wrap %v", err, sentinel)
	}
}

func newAgentHealthServer(workers []int, statuses map[int]int, statusStatus int) *httptest.Server {
	if statusStatus == 0 {
		statusStatus = http.StatusOK
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status.json" {
			w.WriteHeader(statusStatus)
			if statusStatus != http.StatusOK {
				return
			}
			_, _ = fmt.Fprint(w, `{"health":"ok","workers":[`)
			for i, worker := range workers {
				if i > 0 {
					_, _ = fmt.Fprint(w, ",")
				}
				_, _ = fmt.Fprintf(w, `{"spawn_index":%d}`, worker)
			}
			_, _ = fmt.Fprint(w, `]}`)
			return
		}
		var spawnIndex int
		if _, err := fmt.Sscanf(r.URL.Path, "/agent/%d", &spawnIndex); err != nil {
			http.NotFound(w, r)
			return
		}
		status, ok := statuses[spawnIndex]
		if !ok {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
	}))
}

func testAgentHealthConfig(url string, workers int) AgentHealthConfig {
	return AgentHealthConfig{
		URL:                   url,
		ExpectedWorkers:       workers,
		CheckInterval:         10 * time.Second,
		RequestTimeout:        2 * time.Second,
		StartupGrace:          5 * time.Minute,
		UnhealthyDuration:     2 * time.Minute,
		MemoryEvidenceWindow:  5 * time.Minute,
		CgroupPath:            "/sys/fs/cgroup/buildkite-agent.slice",
		FullPressureThreshold: 20,
		FullPressureDuration:  30 * time.Second,
	}
}

func writeMemoryFiles(t *testing.T, dir, events, pressure string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "memory.events.local"), []byte(events), 0o600); err != nil {
		t.Fatalf("write memory events: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.pressure"), []byte(pressure), 0o600); err != nil {
		t.Fatalf("write memory pressure: %v", err)
	}
}

type staticMemoryObserver struct {
	observation memoryObservation
	err         error
}

func (o staticMemoryObserver) Observe() (memoryObservation, error) {
	return o.observation, o.err
}

type fakePoweroff struct {
	err error
}

func (p fakePoweroff) PowerOff() error {
	return p.err
}
