package desktop

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"log"
	"net/url"
	"os"
	"strings"
	"time"
)

func heartbeatFields(s State, version string, now time.Time) bson.M {
	status := "paused"
	active := 0
	if s.Ready && s.Phase == "ready" {
		status = "idle"
	}
	if s.Ready && s.Phase == "busy" {
		status = "busy"
		active = 1
	}
	if s.Phase == "offline" {
		status = "offline"
	}
	hostname, _ := os.Hostname()
	capacity := 0
	if s.QueueEnabled && s.Ready {
		capacity = 1
	}
	return bson.M{"type": "subtitle", "hostname": hostname, "pid": os.Getpid(), "version": version,
		"status": status, "activeJobs": active, "maxJobs": capacity, "mode": "production",
		"capabilities": []string{"subtitle", "moss", "test"}, "heartbeatAt": now, "updatedAt": now,
		"runtime": bson.M{"ready": s.Ready, "phase": s.Phase, "step": s.Step, "stepNumber": s.StepNumber, "totalSteps": s.TotalSteps, "error": s.Error, "queueEnabled": s.QueueEnabled, "jobId": s.JobID, "uploadEnabled": true}}
}
func (m *Manager) heartbeat(ctx context.Context) {
	if m.cfg.HeartbeatURI == "" {
		m.mu.Lock()
		m.state.Heartbeat = "Not configured — set DATABASE_URL in .env to publish heartbeat"
		m.mu.Unlock()
		<-ctx.Done()
		return
	}
	var client *mongo.Client
	lastMessage := ""
	defer func() {
		if client != nil {
			c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = client.Disconnect(c)
		}
	}()
	publish := func(s State) {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		u, err := url.Parse(m.cfg.HeartbeatURI)
		database := ""
		if err == nil {
			database = strings.TrimPrefix(u.Path, "/")
		}
		if database == "" {
			err = fmt.Errorf("DATABASE_URL must include a database name")
		}
		if err == nil && client == nil {
			client, err = mongo.Connect(c, options.Client().ApplyURI(m.cfg.HeartbeatURI).SetServerSelectionTimeout(4*time.Second))
		}
		if err == nil {
			now := time.Now()
			_, err = client.Database(database).Collection("workers").UpdateOne(c, bson.M{"workerId": s.WorkerID}, bson.M{
				"$set":         heartbeatFields(s, m.cfg.WorkerVersion, now),
				"$setOnInsert": bson.M{"_id": uuid.NewString(), "workerId": s.WorkerID, "enable": true, "createdAt": now},
			}, options.Update().SetUpsert(true))
		}
		message := "Heartbeat connected"
		if err != nil {
			message = "Heartbeat disconnected — " + heartbeatError(err)
		}
		m.mu.Lock()
		m.state.Heartbeat = message
		if err == nil {
			now := time.Now()
			m.state.HeartbeatAt = &now
		}
		m.mu.Unlock()
		// The log writer takes m.mu too; never log while holding it.
		if message != lastMessage {
			log.Printf("[heartbeat] %s", message)
			lastMessage = message
		}
	}
	publish(m.snapshot())
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s := m.snapshot()
			s.Phase = "offline"
			s.Ready = false
			publish(s)
			return
		case <-m.changed:
			publish(m.snapshot())
		case <-ticker.C:
			publish(m.snapshot())
		}
	}
}

// Do not expose connection strings or server replies that may contain credentials.
func heartbeatError(err error) string {
	var command mongo.CommandError
	if errors.As(err, &command) {
		return fmt.Sprintf("MongoDB command failed (code %d)", command.Code)
	}
	if mongo.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return "MongoDB connection/write timed out"
	}
	return "MongoDB configuration, connection or write failed"
}
