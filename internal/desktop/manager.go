package desktop

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"worker-subtitle/internal/config"
	"worker-subtitle/internal/jobqueue"
	"worker-subtitle/internal/subtitle"
)

type State struct {
	System         SystemMetrics `json:"system"`
	FileID         string        `json:"fileId"`
	Slug           string        `json:"slug"`
	JobStage       string        `json:"jobStage"`
	JobStatus      string        `json:"jobStatus"`
	OverallPercent float64       `json:"overallPercent"`
	QueueEnabled   bool          `json:"queueEnabled"`
	QueueStatus    string        `json:"queueStatus"`
	JobID          string        `json:"jobId"`
	Phase          string        `json:"phase"`
	Step           string        `json:"step"`
	StepNumber     int           `json:"stepNumber"`
	TotalSteps     int           `json:"totalSteps"`
	Ready          bool          `json:"ready"`
	Error          string        `json:"error"`
	ResultDir      string        `json:"resultDir"`
	Logs           []string      `json:"logs"`
	Heartbeat      string        `json:"heartbeat"`
	HeartbeatAt    *time.Time    `json:"heartbeatAt,omitempty"`
	WorkerID       string        `json:"workerId"`
	Mode           string        `json:"mode"`
}
type testRequest struct {
	AudioURL  string `json:"audioUrl"`
	AudioFile string `json:"audioFile"`
	MediaID   string `json:"mediaId"`
	Prompt    string `json:"prompt"`
}
type Manager struct {
	requests  []subtitle.RequestEvent
	jobLog    *os.File
	claiming  bool
	mu        sync.Mutex
	state     State
	actions   chan *testRequest // nil means setup/retry
	changed   chan struct{}
	cfg       config.Config
	opts      subtitle.Options
	bootstrap func(context.Context, config.Config, subtitle.Options, func(string), io.Writer) (subtitle.Runtime, error)
}

func newManager(cfg config.Config, opts subtitle.Options, id string) *Manager {
	return &Manager{cfg: cfg, opts: opts, actions: make(chan *testRequest, 1), changed: make(chan struct{}, 1), bootstrap: setup, state: State{QueueEnabled: cfg.QueueEnabled, QueueStatus: "Waiting for MOSS readiness", Phase: "checking", Step: "Starting", TotalSteps: 7, WorkerID: id, Mode: "production", Logs: []string{}, Heartbeat: "Waiting for first heartbeat"}}
}
func (m *Manager) snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	s.Logs = append([]string(nil), s.Logs...)
	return s
}
func (m *Manager) update(fn func(*State)) {
	m.mu.Lock()
	fn(&m.state)
	m.mu.Unlock()
	select {
	case m.changed <- struct{}{}:
	default:
	}
}
func (m *Manager) Write(p []byte) (int, error) {
	text := strings.TrimSpace(strings.ReplaceAll(string(p), "\r", "\n"))
	if len(text) > 8000 {
		text = text[len(text)-8000:]
	}
	if text != "" {
		m.mu.Lock()
		if m.jobLog != nil {
			_, _ = fmt.Fprintf(m.jobLog, "%s %s\n", time.Now().Format(time.RFC3339), text)
		}
		m.state.Logs = append(m.state.Logs, text)
		if len(m.state.Logs) > 120 {
			m.state.Logs = m.state.Logs[len(m.state.Logs)-120:]
		}
		m.mu.Unlock()
	}
	return len(p), nil
}
func (m *Manager) submit(req *testRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req == nil {
		if m.state.Phase != "error" {
			return fmt.Errorf("retry is available after a setup/runtime error")
		}
		m.state.Phase = "checking"
		m.state.Ready = false
	} else {
		if m.state.Phase != "ready" || !m.state.Ready || m.claiming {
			return fmt.Errorf("MOSS is not ready or a test is already running")
		}
		opts := m.testOptions(*req)
		if err := opts.Validate(); err != nil {
			return err
		}
		m.state.Phase = "busy"
		m.state.Step = "Running subtitle test"
		m.state.Error = ""
		m.state.ResultDir = ""
	}
	m.actions <- req
	select {
	case m.changed <- struct{}{}:
	default:
	}
	return nil
}
func (m *Manager) testOptions(req testRequest) subtitle.Options {
	opts := m.opts
	opts.AudioURL = req.AudioURL
	opts.AudioFile = req.AudioFile
	opts.MediaID = req.MediaID
	opts.Prompt = req.Prompt
	return opts
}
func (m *Manager) fail(err error) {
	m.update(func(s *State) { s.Phase = "error"; s.Ready = false; s.Error = err.Error() })
}
func (m *Manager) run(ctx context.Context) {
	var runner subtitle.Runtime
	var productionQueue *jobqueue.Queue
	defer func() {
		if productionQueue != nil {
			productionQueue.Close()
		}
	}()
	defer func() {
		if runner != nil {
			runner.Close()
		}
		m.update(func(s *State) { s.Phase = "offline"; s.Ready = false })
	}()
	install := func() {
		if runner != nil {
			runner.Close()
			runner = nil
		}
		m.update(func(s *State) {
			s.Phase = "checking"
			s.Ready = false
			s.Error = ""
			s.StepNumber = 0
			s.ResultDir = ""
		})
		if err := os.MkdirAll(m.cfg.LogDir, 0755); err != nil {
			m.fail(err)
			return
		}
		logFile, err := os.OpenFile(filepath.Join(m.cfg.LogDir, "setup.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			m.fail(err)
			return
		}
		// Keep this handle alive while the model is running; stderr continues here.
		runtimeOutput := &runtimeLog{Writer: io.MultiWriter(m, logFile), file: logFile}
		opts := m.opts
		opts.Observe = m.observeRequest
		r, err := m.bootstrap(ctx, m.cfg, opts, func(step string) { m.update(func(s *State) { s.Phase = "installing"; s.Step = step; s.StepNumber++ }) }, runtimeOutput)
		if err != nil {
			_ = logFile.Close()
			m.fail(err)
			return
		}
		runner = &loggedRuntime{Runtime: r, log: runtimeOutput}
		m.update(func(s *State) {
			s.Phase = "ready"
			s.Step = "MOSS loaded — ready for production jobs"
			s.Ready = true
			s.Error = ""
		})
	}
	install()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-m.actions:
			if req == nil {
				install()
				continue
			}
			if runner == nil {
				m.fail(fmt.Errorf("MOSS runtime unavailable"))
				continue
			}
			finishLog, logErr := m.startJobLog("manual-" + time.Now().Format("20060102-150405"))
			if logErr != nil {
				_, _ = fmt.Fprintf(m, "[log] %v\n", logErr)
			}
			dir, err := subtitle.RunWithRuntime(ctx, m.testOptions(*req), runner)
			_, _ = fmt.Fprintf(m, "[subtitle] Result: output=%s error=%v uploaded=false\n", dir, err)
			finishLog()
			if ctx.Err() != nil {
				return
			}
			m.update(func(s *State) {
				s.ResultDir = dir
				if err != nil {
					s.Error = err.Error()
				}
			})
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			pingErr := runner.Ping(pingCtx)
			cancel()
			if pingErr != nil {
				m.fail(pingErr)
				runner.Close()
				runner = nil
				continue
			}
			m.update(func(s *State) {
				s.Phase = "ready"
				s.Ready = true
				s.Step = "Test finished — files retained for review"
			})
		case <-ticker.C:
			if runner == nil {
				continue
			}
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := runner.Ping(pingCtx)
			cancel()
			if err != nil {
				m.fail(fmt.Errorf("MOSS stopped responding: %w", err))
				runner.Close()
				runner = nil
				continue
			}
			if !m.cfg.QueueEnabled {
				continue
			}
			m.mu.Lock()
			canClaim := m.state.Phase == "ready"
			if canClaim {
				m.claiming = true
			}
			m.mu.Unlock()
			if !canClaim {
				continue
			}
			func() {
				defer func() { m.mu.Lock(); m.claiming = false; m.mu.Unlock() }()
				if m.cfg.HeartbeatURI == "" {
					m.update(func(s *State) { s.QueueStatus = "Set DATABASE_URL to receive production subtitle jobs" })
					return
				}
				if productionQueue == nil {
					var e error
					productionQueue, e = jobqueue.Open(ctx, m.cfg.HeartbeatURI)
					if e != nil {
						m.update(func(s *State) { s.QueueStatus = "Queue connection failed; check DATABASE_URL" })
						return
					}
				}
				claimCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				job, settings, reason, e := productionQueue.Claim(claimCtx, m.snapshot().WorkerID)
				stop()
				if e != nil {
					m.update(func(s *State) { s.QueueStatus = "Queue query failed; check database connectivity/configuration" })
					return
				}
				m.update(func(s *State) { s.QueueStatus = reason })
				if job == nil {
					return
				}
				m.update(func(s *State) {
					s.Phase = "busy"
					s.Step = "Production job: generating subtitles"
					s.JobID = job.ID
					s.FileID = ""
					if job.FileID != nil {
						s.FileID = *job.FileID
					}
					s.Slug = ""
					s.JobStage = "prepare"
					s.JobStatus = "processing"
					s.OverallPercent = 0
					s.Error = ""
					s.ResultDir = ""
				})
				_, _ = fmt.Fprintf(m, "[queue] Claimed subtitle job %s; VTT will be uploaded as enabled media\n", job.ID)
				logCtx, logCancel := context.WithTimeout(ctx, 5*time.Second)
				logName := productionQueue.LogName(logCtx, job)
				logCancel()
				m.update(func(s *State) { s.Slug = logName })
				finishLog, logErr := m.startJobLog(logName)
				if logErr != nil {
					m.update(func(s *State) { s.Error = logErr.Error() })
				}
				defer finishLog()
				fileID := ""
				if job.FileID != nil {
					fileID = *job.FileID
				}
				_, _ = fmt.Fprintf(m, "[queue] Job %s file %s: resolving source\n", job.ID, fileID)
				noSpeech := false
				dir, runErr := productionQueue.Run(ctx, job, m.snapshot().WorkerID, settings, m.opts, func(jobCtx context.Context, options subtitle.Options) (string, error) {
					completed := options.OnComplete
					options.OnComplete = func(empty bool) {
						noSpeech = empty
						completed(empty)
						if !empty {
							m.update(func(s *State) { s.JobStage = "upload"; s.Step = "Uploading VTT and saving enabled media" })
						}
					}
					queueProgress := options.Progress
					options.Progress = func(stage string, percent float64) {
						queueProgress(stage, percent)
						m.update(func(s *State) {
							s.Step = fmt.Sprintf("%s — %.0f%%", stage, percent)
							s.JobStage = stage
							s.OverallPercent = percent
						})
						_, _ = fmt.Fprintf(m, "[subtitle] %s — %.0f%%\n", stage, percent)
					}
					return subtitle.RunWithRuntime(jobCtx, options, runner)
				})
				if runErr != nil {
					_, _ = fmt.Fprintf(m, "[subtitle] Failed: %v\n", runErr)
				} else {
					_, _ = fmt.Fprintf(m, "[subtitle] Job completed; noSpeech=%v; uploaded=%v; media enabled=true; output=%s\n", noSpeech, !noSpeech, dir)
				}
				m.update(func(s *State) {
					s.ResultDir = dir
					s.QueueStatus = "VTT uploaded; enabled media saved; file subtitle status completed"
					s.JobStatus = "completed"
					s.JobStage = "completed"
					if noSpeech {
						s.JobStage = "no_speech"
						s.QueueStatus = "No speech detected; file marked no_speech; no upload"
					}
					if runErr != nil {
						s.JobStage = "error"
						s.JobStatus = "stopped"
						s.Error = runErr.Error()
						s.QueueStatus = "Job stopped/failed; see error and retained files"
					}
				})
				if ctx.Err() != nil {
					return
				}
				pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				e = runner.Ping(pingCtx)
				stop()
				if e != nil {
					m.fail(e)
					runner.Close()
					runner = nil
					return
				}
				m.update(func(s *State) { s.Phase = "ready"; s.Step = "Ready for next production job"; s.Ready = true })
			}()
		}
	}
}

type runtimeLog struct {
	io.Writer
	file *os.File
}
type loggedRuntime struct {
	subtitle.Runtime
	log *runtimeLog
}

func (r *loggedRuntime) Close() { r.Runtime.Close(); _ = r.log.file.Close() }

func (m *Manager) observeRequest(event subtitle.RequestEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.requests {
		if m.requests[i].ID == event.ID {
			m.requests[i] = event
			return
		}
	}
	m.requests = append(m.requests, event)
	if len(m.requests) > 100 {
		m.requests = append([]subtitle.RequestEvent(nil), m.requests[len(m.requests)-100:]...)
	}
}

func (m *Manager) requestSnapshot() []subtitle.RequestEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]subtitle.RequestEvent{}, m.requests...)
}
