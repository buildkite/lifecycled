package lifecycled

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const maxAgentHealthResponseSize = 64 << 10

// AgentHealthConfig configures local Buildkite agent health monitoring.
type AgentHealthConfig struct {
	URL                   string
	ExpectedWorkers       int
	CheckInterval         time.Duration
	RequestTimeout        time.Duration
	StartupGrace          time.Duration
	UnhealthyDuration     time.Duration
	MemoryEvidenceWindow  time.Duration
	CgroupPath            string
	FullPressureThreshold float64
	FullPressureDuration  time.Duration
}

// Validate rejects agent health settings that cannot produce a safe monitor.
func (c AgentHealthConfig) Validate() error {
	var errs []error
	if runtime.GOOS != "linux" {
		errs = append(errs, errors.New("agent health monitoring is only supported on linux"))
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		errs = append(errs, fmt.Errorf("--agent-health-url must be an http URL, got %q", c.URL))
	} else {
		host, ip := u.Hostname(), net.ParseIP(u.Hostname())
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			errs = append(errs, fmt.Errorf("--agent-health-url must use a loopback address, got %q", c.URL))
		}
		if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			errs = append(errs, fmt.Errorf("--agent-health-url must not include credentials, a path, query, or fragment, got %q", c.URL))
		}
	}
	if c.ExpectedWorkers <= 0 {
		errs = append(errs, fmt.Errorf("--agent-health-expected-workers must be positive, got %d", c.ExpectedWorkers))
	}
	durations := []struct {
		name  string
		value time.Duration
	}{
		{"--agent-health-check-interval", c.CheckInterval},
		{"--agent-health-request-timeout", c.RequestTimeout},
		{"--agent-health-startup-grace", c.StartupGrace},
		{"--agent-unhealthy-duration", c.UnhealthyDuration},
		{"--agent-memory-evidence-window", c.MemoryEvidenceWindow},
		{"--agent-full-pressure-duration", c.FullPressureDuration},
	}
	for _, d := range durations {
		if d.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %s", d.name, d.value))
		}
	}
	if c.CgroupPath == "" {
		errs = append(errs, errors.New("--agent-cgroup-path must not be empty"))
	}
	if c.FullPressureThreshold <= 0 || c.FullPressureThreshold > 100 {
		errs = append(errs, fmt.Errorf("--agent-full-pressure-threshold must be within (0, 100], got %g", c.FullPressureThreshold))
	}
	return errors.Join(errs...)
}

type agentHealthListener struct {
	instanceID string
	config     AgentHealthConfig
	client     *http.Client
	memory     memoryObserver
	poweroff   poweroff
}

func newAgentHealthListener(instanceID string, config AgentHealthConfig, client *http.Client, memory memoryObserver, poweroff poweroff) *agentHealthListener {
	return &agentHealthListener{
		instanceID: instanceID,
		config:     config,
		client:     client,
		memory:     memory,
		poweroff:   poweroff,
	}
}

func (l *agentHealthListener) Type() string {
	return "agent-health"
}

func (l *agentHealthListener) Start(ctx context.Context, notices chan<- TerminationNotice, log *logrus.Entry) error {
	startedAt := time.Now()
	tracker := healthFailureTracker{
		startedAt:         startedAt,
		startupGrace:      l.config.StartupGrace,
		unhealthyDuration: l.config.UnhealthyDuration,
	}
	var (
		lastState      agentHealthState
		lastEvidenceAt time.Time
		lastEvidence   string
		pressureSince  time.Time
		pressureLogged bool
	)

	ticker := time.NewTicker(l.config.CheckInterval)
	defer ticker.Stop()

	for {
		result := l.probe(ctx)
		if result.state != lastState {
			log.WithFields(logrus.Fields{
				"state":   result.state.String(),
				"healthy": result.healthyWorkers,
				"workers": l.config.ExpectedWorkers,
				"reason":  result.reason,
			}).Info("Buildkite agent health changed")
			lastState = result.state
		}

		observation, err := l.memory.Observe()
		now := time.Now()
		if err != nil {
			log.WithError(err).Debug("Failed to read Buildkite agent memory pressure")
		}
		if observation.oom {
			lastEvidenceAt = now
			lastEvidence = observation.reason
			log.WithField("source", observation.reason).Warn("Observed Buildkite agent OOM evidence")
		}
		if observation.fullPressure >= l.config.FullPressureThreshold {
			if pressureSince.IsZero() {
				pressureSince = now
			}
			if now.Sub(pressureSince) >= l.config.FullPressureDuration {
				lastEvidenceAt = now
				lastEvidence = "memory.pressure"
				if !pressureLogged {
					log.WithField("fullAvg10", observation.fullPressure).Warn("Observed sustained Buildkite agent memory pressure")
					pressureLogged = true
				}
			}
		} else {
			pressureSince = time.Time{}
			pressureLogged = false
		}

		hasRecentMemoryEvidence := !lastEvidenceAt.IsZero() && now.Sub(lastEvidenceAt) <= l.config.MemoryEvidenceWindow
		actionable := result.state == agentHealthLocalFailure || result.state == agentHealthUnhealthy && hasRecentMemoryEvidence
		if tracker.Observe(now, actionable) {
			reason := result.reason
			if result.state == agentHealthUnhealthy {
				reason = fmt.Sprintf("%s; recent local evidence: %s", reason, lastEvidence)
			}
			notice := &agentHealthTerminationNotice{
				instanceID: l.instanceID,
				reason:     reason,
				poweroff:   l.poweroff,
			}
			select {
			case notices <- notice:
				return nil
			case <-ctx.Done():
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type agentHealthState int

const (
	agentHealthUnknown agentHealthState = iota
	agentHealthHealthy
	agentHealthDegraded
	agentHealthUnhealthy
	agentHealthLocalFailure
)

func (s agentHealthState) String() string {
	switch s {
	case agentHealthHealthy:
		return "healthy"
	case agentHealthDegraded:
		return "degraded"
	case agentHealthUnhealthy:
		return "unhealthy"
	case agentHealthLocalFailure:
		return "local-failure"
	default:
		return "unknown"
	}
}

type agentHealthResult struct {
	state          agentHealthState
	healthyWorkers int
	reason         string
}

func (l *agentHealthListener) probe(ctx context.Context) agentHealthResult {
	spawnIndexes, err := l.workerIndexes(ctx)
	if err != nil {
		return agentHealthResult{state: agentHealthLocalFailure, reason: err.Error()}
	}
	if len(spawnIndexes) != l.config.ExpectedWorkers {
		return agentHealthResult{
			state:  agentHealthLocalFailure,
			reason: fmt.Sprintf("status reported %d workers, expected %d", len(spawnIndexes), l.config.ExpectedWorkers),
		}
	}

	healthy := 0
	for _, spawnIndex := range spawnIndexes {
		status, err := l.get(ctx, fmt.Sprintf("/agent/%d", spawnIndex), nil)
		if err != nil {
			return agentHealthResult{state: agentHealthLocalFailure, healthyWorkers: healthy, reason: err.Error()}
		}
		switch status {
		case http.StatusOK:
			healthy++
		case http.StatusInternalServerError:
			// The agent uses 500 for a failed Buildkite API heartbeat. This is
			// not independently actionable because a network or API outage can
			// affect every instance in a fleet at once.
		default:
			return agentHealthResult{
				state:          agentHealthLocalFailure,
				healthyWorkers: healthy,
				reason:         fmt.Sprintf("agent %d health returned HTTP %d", spawnIndex, status),
			}
		}
	}

	switch healthy {
	case l.config.ExpectedWorkers:
		return agentHealthResult{state: agentHealthHealthy, healthyWorkers: healthy, reason: "all workers are healthy"}
	case 0:
		return agentHealthResult{state: agentHealthUnhealthy, reason: "all workers report failed heartbeats"}
	default:
		return agentHealthResult{
			state:          agentHealthDegraded,
			healthyWorkers: healthy,
			reason:         fmt.Sprintf("%d of %d workers are healthy", healthy, l.config.ExpectedWorkers),
		}
	}
}

func (l *agentHealthListener) workerIndexes(ctx context.Context) ([]int, error) {
	var status struct {
		Workers []struct {
			SpawnIndex int `json:"spawn_index"`
		} `json:"workers"`
	}
	httpStatus, err := l.get(ctx, "/status.json", &status)
	if err != nil {
		return nil, err
	}
	if httpStatus != http.StatusOK {
		return nil, fmt.Errorf("agent status returned HTTP %d", httpStatus)
	}

	seen := make(map[int]struct{}, len(status.Workers))
	indexes := make([]int, 0, len(status.Workers))
	for _, worker := range status.Workers {
		if worker.SpawnIndex <= 0 {
			return nil, fmt.Errorf("agent status returned invalid spawn index %d", worker.SpawnIndex)
		}
		if _, ok := seen[worker.SpawnIndex]; ok {
			return nil, fmt.Errorf("agent status returned duplicate spawn index %d", worker.SpawnIndex)
		}
		seen[worker.SpawnIndex] = struct{}{}
		indexes = append(indexes, worker.SpawnIndex)
	}
	return indexes, nil
}

func (l *agentHealthListener) get(ctx context.Context, path string, dst any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(l.config.URL, "/")+path, nil)
	if err != nil {
		return 0, fmt.Errorf("create agent health request: %w", err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request agent health: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if dst == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAgentHealthResponseSize))
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAgentHealthResponseSize)).Decode(dst); err != nil {
		return resp.StatusCode, fmt.Errorf("decode agent health response: %w", err)
	}
	return resp.StatusCode, nil
}

type healthFailureTracker struct {
	startedAt         time.Time
	startupGrace      time.Duration
	unhealthyDuration time.Duration
	unhealthySince    time.Time
}

func (t *healthFailureTracker) Observe(now time.Time, actionable bool) bool {
	if now.Sub(t.startedAt) < t.startupGrace || !actionable {
		t.unhealthySince = time.Time{}
		return false
	}
	if t.unhealthySince.IsZero() {
		t.unhealthySince = now
		return false
	}
	return now.Sub(t.unhealthySince) >= t.unhealthyDuration
}

type memoryObservation struct {
	oom          bool
	fullPressure float64
	reason       string
}

type memoryObserver interface {
	Observe() (memoryObservation, error)
}

type cgroupMemoryObserver struct {
	eventsPath   string
	pressurePath string
	initialized  bool
	lastOOM      uint64
	lastOOMKill  uint64
}

func newCgroupMemoryObserver(cgroupPath string) *cgroupMemoryObserver {
	return &cgroupMemoryObserver{
		eventsPath:   filepath.Join(cgroupPath, "memory.events.local"),
		pressurePath: filepath.Join(cgroupPath, "memory.pressure"),
	}
}

func (o *cgroupMemoryObserver) Observe() (memoryObservation, error) {
	var observation memoryObservation
	events, eventsErr := os.ReadFile(o.eventsPath)
	if eventsErr == nil {
		values, err := parseFlatKeyValues(string(events))
		if err != nil {
			eventsErr = fmt.Errorf("parse %s: %w", o.eventsPath, err)
		} else {
			oom, oomKill := values["oom"], values["oom_kill"]
			if o.initialized && (oom > o.lastOOM || oomKill > o.lastOOMKill) {
				observation.oom = true
				observation.reason = "memory.events.local"
			}
			o.lastOOM, o.lastOOMKill = oom, oomKill
			o.initialized = true
		}
	}

	pressure, pressureErr := os.ReadFile(o.pressurePath)
	if pressureErr == nil {
		observation.fullPressure, pressureErr = parseFullPressureAvg10(string(pressure))
		if pressureErr != nil {
			pressureErr = fmt.Errorf("parse %s: %w", o.pressurePath, pressureErr)
		}
	}
	return observation, errors.Join(eventsErr, pressureErr)
}

func parseFlatKeyValues(input string) (map[string]uint64, error) {
	values := make(map[string]uint64)
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid line %q", scanner.Text())
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", fields[0], err)
		}
		values[fields[0]] = value
	}
	return values, scanner.Err()
}

func parseFullPressureAvg10(input string) (float64, error) {
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "full" {
			continue
		}
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok || key != "avg10" {
				continue
			}
			return strconv.ParseFloat(value, 64)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("full avg10 is missing")
}

type poweroff interface {
	PowerOff() error
}

type agentHealthTerminationNotice struct {
	instanceID string
	reason     string
	poweroff   poweroff
}

func (n *agentHealthTerminationNotice) Type() string {
	return "agent-health"
}

func (n *agentHealthTerminationNotice) Handle(ctx context.Context, _ Handler, _ *logrus.Entry) error {
	return n.HandleEmergency(ctx)
}

func (n *agentHealthTerminationNotice) HandleEmergency(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrDrainInterrupted, err)
	}
	_, _ = fmt.Fprintf(os.Stderr, "lifecycled: powering off unhealthy instance %s: %s\n", n.instanceID, n.reason)
	if err := n.poweroff.PowerOff(); err != nil {
		return fmt.Errorf("power off instance: %w", err)
	}
	return errors.New("power off returned without shutting down")
}
