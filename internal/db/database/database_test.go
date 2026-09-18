package database

import (
	"errors"
	"sync"
	"testing"
)

func TestConnectionReusedAcrossConcurrentJobs(t *testing.T) {
	opens, closes := 0, 0
	c := connection{open: func(string) error { opens++; return nil }, close: func() error { closes++; return nil }}
	var jobs sync.WaitGroup
	for i := 0; i < 20; i++ {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			if err := c.connect("test"); err != nil {
				t.Error(err)
			}
		}()
	}
	jobs.Wait()
	if opens != 1 || closes != 0 {
		t.Fatalf("opens=%d closes=%d", opens, closes)
	}
	if err := c.disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := c.disconnect(); err != nil {
		t.Fatal(err)
	}
	if closes != 1 {
		t.Fatalf("closed %d times", closes)
	}
}

func TestFailedConnectionCanRetry(t *testing.T) {
	opens := 0
	c := connection{open: func(string) error {
		opens++
		if opens == 1 {
			return errors.New("offline")
		}
		return nil
	}, close: func() error { return nil }}
	if c.connect("test") == nil {
		t.Fatal("expected failure")
	}
	if err := c.connect("test"); err != nil {
		t.Fatal(err)
	}
	if err := c.connect("test"); err != nil {
		t.Fatal(err)
	}
	if opens != 2 {
		t.Fatalf("opens=%d", opens)
	}
}
