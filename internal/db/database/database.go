package database

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"worker-subtitle/internal/config"

	"github.com/zergolf1994/goose"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// One ODM pool per process. Failed initial connections remain retryable.
type connection struct {
	mu        sync.Mutex
	connected bool
	open      func(string) error
	close     func() error
}

var shared = connection{open: openODM, close: closeODM}
var odmClient *mongo.Client // protected by shared.mu

func openODM(uri string) error {
	u, err := url.Parse(uri)
	if err != nil || strings.Trim(u.Path, "/") == "" {
		return fmt.Errorf("DATABASE_URL must include a database name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return err
	}
	if err := c.Ping(ctx, nil); err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = c.Disconnect(cleanup)
		return err
	}
	odmClient = c
	goose.SetDB(c.Database(strings.Trim(u.Path, "/")))
	log.Println("✅ Media/storage MongoDB connection established (reused across jobs)")
	return nil
}

func closeODM() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := odmClient.Disconnect(ctx); err != nil {
		return err
	}
	odmClient = nil
	goose.SetDB(nil)
	return nil
}

func (c *connection) connect(uri string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected {
		return nil
	}
	if err := c.open(uri); err != nil {
		return err
	}
	c.connected = true
	return nil
}

func (c *connection) disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return nil
	}
	if err := c.close(); err != nil {
		return err
	}
	c.connected = false
	log.Println("🔌 Media/storage MongoDB connection closed at shutdown")
	return nil
}

// Connect establishes the MongoDB connection via goose ODM.
// DB name comes from the connection string (DATABASE_URL).
//
// Indexes are NOT managed here — the platform (mongoose) side owns
// all index definitions for shared collections. Creating them from two
// codebases is how stale-index bugs happen.
func Connect() error {
	return shared.connect(config.AppConfig.MongoURI)
}

// Disconnect closes the MongoDB connection.
func Disconnect() {
	if err := shared.disconnect(); err != nil {
		log.Printf("⚠️ Error disconnecting from MongoDB: %v", err)
	}
}
