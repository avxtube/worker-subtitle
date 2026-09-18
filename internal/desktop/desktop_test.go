package desktop

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"worker-subtitle/internal/config"
	"worker-subtitle/internal/subtitle"
	"worker-subtitle/moss"
)

type fakeRuntime struct{ closed atomic.Int32 }

func (f *fakeRuntime) Transcribe(context.Context, string, string, int) (subtitle.Result, error) {
	return subtitle.Result{}, nil
}
func (f *fakeRuntime) Ping(context.Context) error { return nil }
func (f *fakeRuntime) Close()                     { f.closed.Add(1) }
func waitPhase(t *testing.T, m *Manager, wanted string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.snapshot().Phase == wanted {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("wanted %s, got %+v", wanted, m.snapshot())
}

func TestReadyOnlyAfterModelLoadAndRetry(t *testing.T) {
	m := newManager(config.Config{LogDir: t.TempDir()}, subtitle.Options{}, "subtitle_test@1")
	gate := make(chan struct{})
	calls := 0
	r := &fakeRuntime{}
	m.bootstrap = func(ctx context.Context, _ config.Config, _ subtitle.Options, report func(string), _ io.Writer) (subtitle.Runtime, error) {
		calls++
		report("Loading GPU")
		if calls == 1 {
			return nil, errors.New("installation interrupted")
		}
		select {
		case <-gate:
			return r, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.run(ctx); close(done) }()
	waitPhase(t, m, "error")
	if m.snapshot().Ready {
		t.Fatal("failed installation reported ready")
	}
	if err := m.submit(nil); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, "installing")
	if m.snapshot().Ready {
		t.Fatal("model loading reported ready")
	}
	close(gate)
	waitPhase(t, m, "ready")
	if !m.snapshot().Ready {
		t.Fatal("loaded model not ready")
	}
	cancel()
	<-done
	if r.closed.Load() != 1 {
		t.Fatal("owned model not closed exactly once")
	}
}

func TestHeartbeatCapacityAndStatus(t *testing.T) {
	ready := heartbeatFields(State{Phase: "ready", Ready: true, QueueEnabled: true}, "test", time.Now())
	if ready["maxJobs"] != 1 || ready["runtime"].(bson.M)["uploadEnabled"] != true {
		t.Fatal("production test capacity or upload contract incorrect")
	}
	for _, tt := range []struct {
		phase  string
		ready  bool
		status string
	}{{"installing", false, "paused"}, {"error", false, "paused"}, {"ready", true, "idle"}, {"busy", true, "busy"}, {"offline", false, "offline"}} {
		fields := heartbeatFields(State{Phase: tt.phase, Ready: tt.ready}, "test", time.Now())
		if fields["status"] != tt.status || fields["maxJobs"] != 0 || fields["type"] != "subtitle" {
			t.Fatalf("wrong heartbeat: %v", fields)
		}
		if _, ok := fields["enable"]; ok {
			t.Fatal("heartbeat would overwrite admin enable switch")
		}
		if fields["runtime"].(bson.M)["ready"] != tt.ready {
			t.Fatal("wrong readiness")
		}
	}
}

func TestExistingDependenciesDoNotReinstall(t *testing.T) {
	cfg := config.Config{RuntimeDir: t.TempDir()}
	calls := []string{}
	run := func(_ context.Context, name string, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	if err := ensurePackages(context.Background(), cfg, "python", run); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		if strings.Contains(call, "pip install") {
			t.Fatal("working environment reinstalled")
		}
	}
}

func TestHeartbeatErrorDoesNotExposeCredentials(t *testing.T) {
	if got := heartbeatError(context.DeadlineExceeded); !strings.Contains(got, "timed out") {
		t.Fatalf("timeout not identified: %s", got)
	}
	if got := heartbeatError(errors.New("mongodb://user:secret@host")); strings.Contains(got, "secret") {
		t.Fatal("heartbeat exposed credentials")
	}
}
func TestMissingPackagesInstallAndVerify(t *testing.T) {
	cfg := config.Config{RuntimeDir: t.TempDir(), TorchIndex: "https://example.invalid/torch"}
	checks := 0
	installed := 0
	run := func(_ context.Context, name string, args ...string) error {
		if args[0] == "-c" {
			checks++
			if checks <= 2 {
				return errors.New("missing package")
			}
		}
		if len(args) > 2 && args[0] == "-m" && args[1] == "pip" {
			installed++
		}
		return nil
	}
	if err := ensurePackages(context.Background(), cfg, "python", run); err != nil {
		t.Fatal(err)
	}
	if installed != 2 || checks != 3 {
		t.Fatalf("installation/verification missing: installs=%d checks=%d", installed, checks)
	}
}
func TestBundledSourceCanPopulateEmptyFolder(t *testing.T) {
	dir := t.TempDir()
	if err := moss.Install(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"server.py", "download_model.py", "requirements.txt", "moss_transcribe_diarize/__init__.py", "moss_transcribe_diarize/app/model_runner.py"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}
func TestDashboardGuardsAndBusyGate(t *testing.T) {
	m := newManager(config.Config{}, subtitle.Options{ChunkSeconds: 180, MaxTokens: 2048}, "subtitle_test@1")
	handler := m.handler("127.0.0.1:8887", "secret")
	request := func(method, path, host, token, body string) int {
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		r.Header.Set("X-Worker-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("GET", "/api/state", "attacker.test:8887", "", ""); got != 403 {
		t.Fatal("DNS rebinding allowed")
	}
	if got := request("POST", "/api/retry", "127.0.0.1:8887", "", ""); got != 403 {
		t.Fatal("cross-site mutation allowed")
	}
	if got := request("POST", "/api/test", "127.0.0.1:8887", "secret", `{"audioFile":"a.wav"}`); got != 409 {
		t.Fatal("test allowed before ready")
	}
	m.update(func(s *State) { s.Phase = "ready"; s.Ready = true })
	if got := request("POST", "/api/test", "127.0.0.1:8887", "secret", `{"audioFile":"a.wav"}`); got != http.StatusAccepted {
		t.Fatalf("start: %d", got)
	}
	if got := request("POST", "/api/test", "127.0.0.1:8887", "secret", `{"audioFile":"a.wav"}`); got != 409 {
		t.Fatal("concurrent GPU jobs accepted")
	}
}

func TestManagedPythonStaysInsideBinaryRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".venv", "Scripts", "python.exe")
	var calls []string
	run := func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	if err := ensureWindowsPython(context.Background(), root, target, run); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || !strings.Contains(calls[0], "--target="+filepath.Join(root, "python")) || !strings.Contains(calls[2], "-m venv "+filepath.Join(root, ".venv")) {
		t.Fatal(calls)
	}
	if err := os.MkdirAll(filepath.Join(root, "python"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "python", "python.exe"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	calls = nil
	if err := ensureWindowsPython(context.Background(), root, target, run); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || strings.Contains(calls[0], "install") {
		t.Fatal("existing runtime downloaded again", calls)
	}
	calls = nil
	if err := ensureWindowsPython(context.Background(), root, filepath.Join(t.TempDir(), "python.exe"), run); err == nil || len(calls) != 0 {
		t.Fatal("external target accepted")
	}
}

func TestMOSSRequestHistoryUpdatesAndBounds(t *testing.T) {
	m := newManager(config.Config{}, subtitle.Options{}, "worker")
	for i := uint64(1); i <= 101; i++ {
		m.observeRequest(subtitle.RequestEvent{ID: i, Status: "pending"})
	}
	m.observeRequest(subtitle.RequestEvent{ID: 101, Status: "success", Response: "result"})
	items := m.requestSnapshot()
	if len(items) != 100 || items[0].ID != 2 || items[99].Status != "success" {
		t.Fatal("incorrect request history")
	}
	items[0].Status = "modified"
	if m.requestSnapshot()[0].Status != "pending" {
		t.Fatal("snapshot mutates history")
	}
	r := httptest.NewRecorder()
	m.handler("127.0.0.1:8887", "token").ServeHTTP(r, httptest.NewRequest("GET", "http://127.0.0.1:8887/api/moss-requests", nil))
	if r.Code != 200 || !strings.Contains(r.Body.String(), "result") {
		t.Fatal("request endpoint unavailable")
	}
}
