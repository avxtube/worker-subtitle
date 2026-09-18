package subtitle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"time"
)

type Segment struct {
	Backend string  `json:"backend,omitempty"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker"`
	Text    string  `json:"text"`
}
type Result struct {
	Backend             string    `json:"backend,omitempty"`
	Language            string    `json:"language,omitempty"`
	MossText            string    `json:"moss_text,omitempty"`
	MossGeneratedTokens int       `json:"moss_generated_tokens,omitempty"`
	Text                string    `json:"text"`
	Segments            []Segment `json:"segments"`
	GeneratedTokens     int       `json:"generated_tokens"`
	Error               string    `json:"error,omitempty"`
	Retryable           bool      `json:"retryable,omitempty"`
}
type transcriber interface {
	Transcribe(context.Context, string, string, int) (Result, error)
}
type service struct {
	observe func(RequestEvent)
	cmd     *exec.Cmd
	input   io.WriteCloser
	encoder *json.Encoder
	decoder *json.Decoder
	cancel  context.CancelFunc
}

// Runtime is a warm MOSS process, reused by the desktop worker across test jobs.
type Runtime interface {
	Transcribe(context.Context, string, string, int) (Result, error)
	Ping(context.Context) error
	Close()
}

func StartRuntime(ctx context.Context, opts Options, output io.Writer) (Runtime, error) {
	return startService(ctx, opts, output)
}

func (s *service) Ping(ctx context.Context) error {
	var response struct {
		Ready bool `json:"ready"`
	}
	if err := s.exchange(ctx, map[string]string{"op": "ping"}, &response); err != nil {
		return err
	}
	if !response.Ready {
		return fmt.Errorf("MOSS is not ready")
	}
	return nil
}

// A private JSON-lines pipe keeps the model loaded across chunks without
// exposing an HTTP port. Only this worker owns and terminates this process.
func startService(ctx context.Context, opts Options, logFile io.Writer) (*service, error) {
	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, opts.Python, "-u", opts.MossScript, "--model-dir", opts.ModelDir)
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	cmd.Stderr = logFile
	cmd.WaitDelay = 5 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		return nil, err
	}
	s := &service{cmd: cmd, input: input, encoder: json.NewEncoder(input), decoder: json.NewDecoder(output), cancel: cancel, observe: opts.Observe}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = input.Close()
		return nil, err
	}
	readyCtx, readyCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer readyCancel()
	var ready struct {
		Ready bool   `json:"ready"`
		Error string `json:"error"`
	}
	if err := s.exchange(readyCtx, nil, &ready); err != nil {
		s.Close()
		return nil, fmt.Errorf("MOSS startup (see moss.log): %w", err)
	}
	if !ready.Ready {
		s.Close()
		return nil, fmt.Errorf("MOSS startup: %s", ready.Error)
	}
	return s, nil
}
func (s *service) exchange(ctx context.Context, request, response any) (exchangeErr error) {
	event := RequestEvent{ID: requestSequence.Add(1), StartedAt: time.Now(), Operation: "transcribe", Transport: "JSON pipe", Status: "pending", Request: eventJSON(request)}
	if request == nil {
		event.Operation = "startup"
	}
	if req, ok := request.(map[string]string); ok && req["op"] != "" {
		event.Operation = req["op"]
	}
	if s.observe != nil {
		s.observe(event)
		defer func() {
			event.DurationMS = time.Since(event.StartedAt).Milliseconds()
			event.Status = "success"
			event.Response = eventJSON(response)
			if exchangeErr != nil {
				event.Error = exchangeErr.Error()
			} else {
				data, _ := json.Marshal(response)
				var outcome struct {
					Error string `json:"error"`
					Ready *bool  `json:"ready"`
				}
				_ = json.Unmarshal(data, &outcome)
				event.Error = outcome.Error
				if outcome.Ready != nil && !*outcome.Ready && event.Error == "" {
					event.Error = "MOSS is not ready"
				}
			}
			if event.Error != "" {
				event.Status = "error"
			}
			s.observe(event)
		}()
	}
	done := make(chan error, 1)
	go func() {
		if request != nil {
			if err := s.encoder.Encode(request); err != nil {
				done <- err
				return
			}
		}
		done <- s.decoder.Decode(response)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		s.cancel()
		<-done
		return ctx.Err()
	}
}
func (s *service) Transcribe(ctx context.Context, audio, prompt string, tokens int) (Result, error) {
	var result Result
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	err := s.exchange(callCtx, map[string]any{"audio": audio, "prompt": prompt, "max_new_tokens": tokens}, &result)
	return result, err
}
func (s *service) Close() { _ = s.input.Close(); s.cancel(); _ = s.cmd.Wait() }

var requestSequence atomic.Uint64

type RequestEvent struct {
	ID         uint64    `json:"id"`
	StartedAt  time.Time `json:"startedAt"`
	Operation  string    `json:"operation"`
	Transport  string    `json:"transport"`
	Status     string    `json:"status"`
	DurationMS int64     `json:"durationMs"`
	Request    string    `json:"request"`
	Response   string    `json:"response"`
	Error      string    `json:"error,omitempty"`
}

func eventJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "Unable to serialize payload"
	}
	runes := []rune(string(data))
	if len(runes) > 32768 {
		return string(runes[:32768]) + "\n… [preview truncated]"
	}
	return string(data)
}
