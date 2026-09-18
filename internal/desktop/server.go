package desktop

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"worker-subtitle/internal/config"
	"worker-subtitle/internal/core/utils"
	"worker-subtitle/internal/db/database"
	"worker-subtitle/internal/subtitle"
)

//go:embed index.html
var page string

func Run(ctx context.Context, cfg config.Config, opts subtitle.Options, open bool) error {
	id := utils.GenerateWorkerID()
	if !strings.HasPrefix(id, "subtitle_") {
		hostname, _ := os.Hostname()
		id = "subtitle_" + hostname + "@1"
	}
	release, err := utils.AcquireInstanceLock(id)
	if err != nil {
		return err
	}
	defer release()
	releaseRuntime, err := utils.AcquireInstanceLock("runtime:" + cfg.RuntimeDir)
	if err != nil {
		return err
	}
	defer releaseRuntime()
	// Run waits for the job loop to exit before closing its shared ODM pool.
	defer database.Disconnect()
	host, _, err := net.SplitHostPort(cfg.DashboardAddr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("DASHBOARD_ADDR must use a loopback IP, for example 127.0.0.1:8887")
	}
	listener, err := net.Listen("tcp", cfg.DashboardAddr)
	if err != nil {
		return fmt.Errorf("dashboard: %w", err)
	}
	m := newManager(cfg, opts, id)
	previousLogOutput := log.Writer()
	log.SetOutput(io.MultiWriter(previousLogOutput, m))
	defer log.SetOutput(previousLogOutput)
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		_ = listener.Close()
		return err
	}
	token := hex.EncodeToString(random)
	address := listener.Addr().String()
	server := &http.Server{Handler: m.handler(address, token), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		sampler := newMetricSampler(cfg.WorkDir)
		for {
			metrics := sampler.sample()
			m.update(func(s *State) { s.System = metrics })
			select {
			case <-life.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
	finished := make(chan struct{})
	go func() { defer close(finished); m.run(life) }()
	heartbeatDone := make(chan struct{})
	go func() { defer close(heartbeatDone); m.heartbeat(life) }()
	dashboardURL := "http://" + address
	log.Printf("Subtitle dashboard: %s — production subtitle worker", dashboardURL)
	if open {
		openBrowser(dashboardURL)
	}
	select {
	case <-ctx.Done():
	case err = <-serveErrors:
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_ = server.Shutdown(shutdown)
	<-finished
	<-heartbeatDone
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
func openBrowser(address string) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	} else if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", address)
	} else {
		cmd = exec.Command("xdg-open", address)
	}
	hideWindow(cmd)
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}
func (m *Manager) handler(address, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Host validation blocks DNS rebinding; POST token prevents cross-site actions.
		if r.Host != address {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method == http.MethodGet {
			if r.URL.Path == "/log-history" {
				serveLogHistory(m.cfg.LogDir)(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/log/") {
				serveProcessLogBySlug(m.cfg.LogDir)(w, r)
				return
			}
			switch r.URL.Path {
			case "/":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(strings.ReplaceAll(page, "__TOKEN__", token)))
			case "/api/state":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(m.snapshot())
			case "/api/moss-requests":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(m.requestSnapshot())
			default:
				http.NotFound(w, r)
			}
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Worker-Token")), []byte(token)) != 1 {
			http.Error(w, "invalid token", 403)
			return
		}
		var err error
		switch r.URL.Path {
		case "/api/retry":
			err = m.submit(nil)
		case "/api/test":
			var req testRequest
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&req); err != nil {
				http.Error(w, "invalid test request", 400)
				return
			}
			err = m.submit(&req)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}
