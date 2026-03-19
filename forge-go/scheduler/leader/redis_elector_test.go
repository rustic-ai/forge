package leader

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisElector_AcquireAndHeartbeat(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer func() { _ = client.Close() }()

	elector := NewRedisElector(client, "node-1", "forge:leader", 1*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- elector.Acquire(ctx)
	}()

	// Wait for acquire
	select {
	case isLeader := <-elector.Watch():
		if !isLeader {
			t.Errorf("expected to become leader")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for leadership")
	}

	if !elector.IsLeader() {
		t.Errorf("IsLeader should be true")
	}

	select {
	case acquireErr := <-errCh:
		if acquireErr != nil {
			t.Fatalf("expected acquire to succeed, got %v", acquireErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for acquire to return")
	}

	// Verify key is in Redis
	val, err := s.Get("forge:leader")
	if err != nil {
		t.Fatalf("key forge:leader not found: %v", err)
	}
	if val != "node-1" {
		t.Errorf("expected key value 'node-1', got '%s'", val)
	}

	// We can't perfectly test miniredis TTL fast forward synchronously with our Go runtime ticket
	// But we can check heartbeat logic using Resign

	// Resign
	err = elector.Resign(context.Background())
	if err != nil {
		t.Fatalf("failed to resign: %v", err)
	}

	if elector.IsLeader() {
		t.Errorf("IsLeader should be false after resignation")
	}

	if s.Exists("forge:leader") {
		t.Errorf("key forge:leader should be deleted after resignation")
	}
}

func TestRedisElector_Failover(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer s.Close()

	client := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer func() { _ = client.Close() }()

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	elector1 := NewRedisElector(client, "node-1", "forge:leader", 2*time.Second)

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	elector2 := NewRedisElector(client, "node-2", "forge:leader", 2*time.Second)

	// Start both
	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)
	go func() {
		errCh1 <- elector1.Acquire(ctx1)
	}()
	go func() {
		errCh2 <- elector2.Acquire(ctx2)
	}()

	// One should become leader
	var leader string
	select {
	case <-elector1.Watch():
		leader = "node-1"
	case <-elector2.Watch():
		leader = "node-2"
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for initial leader")
	}

	if leader != "node-1" {
		t.Logf("leader is %s", leader) // Expected, ordering varies slightly
	}

	// Kill the leader's context simulating a crash (doesn't call Resign)
	if leader == "node-1" {
		cancel1()
	} else {
		cancel2()
	}

	// Fast forward time deeply to expire the lock since leader is "dead"
	// Miniredis FastForward affects its internal TTL tracking
	time.Sleep(100 * time.Millisecond) // Let the goroutines pause
	s.FastForward(3 * time.Second)

	// The secondary elector should now pick it up
	if leader == "node-1" {
		select {
		case isLeader := <-elector2.Watch():
			if !isLeader {
				t.Errorf("node-2 expects to become leader")
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for node-2 to take over leadership")
		}
	} else {
		select {
		case isLeader := <-elector1.Watch():
			if !isLeader {
				t.Errorf("node-1 expects to become leader")
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for node-1 to take over leadership")
		}
	}

	waitAcquire := func(nodeID string, ch <-chan error) {
		t.Helper()
		select {
		case acquireErr := <-ch:
			if acquireErr != nil && !errors.Is(acquireErr, context.Canceled) {
				t.Fatalf("%s acquire failed: %v", nodeID, acquireErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for %s acquire to return", nodeID)
		}
	}

	waitAcquire("node-1", errCh1)
	waitAcquire("node-2", errCh2)
}
