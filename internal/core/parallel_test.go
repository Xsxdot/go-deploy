package core

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFilterHealthy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dc := NewDeployContext(ctx, nil, nil, nil, nil, "")

	h1 := &HostTarget{ResourceID: "h1", Addr: "192.168.1.1", User: "root"}
	h2 := &HostTarget{ResourceID: "h2", Addr: "192.168.1.2", User: "root"}
	h3 := &HostTarget{ResourceID: "h3", Addr: "192.168.1.3", User: "root"}

	targets := []Target{h1, h2, h3}
	healthy := dc.FilterHealthy(targets)
	if len(healthy) != 3 {
		t.Errorf("FilterHealthy: expected 3, got %d", len(healthy))
	}

	dc.MarkDead("h2")
	healthy = dc.FilterHealthy(targets)
	if len(healthy) != 2 {
		t.Errorf("FilterHealthy after MarkDead(h2): expected 2, got %d", len(healthy))
	}
	ids := make(map[string]bool)
	for _, tgt := range healthy {
		ids[tgt.ID()] = true
	}
	if ids["h2"] {
		t.Error("FilterHealthy: h2 should be filtered out")
	}
}

func TestRunParallel_EmptyTargets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dc := NewDeployContext(ctx, nil, nil, nil, nil, "")

	noop := func(ctx context.Context, t Target) error { return nil }

	err := RunParallel(dc, nil, ParallelOptions{}, noop)
	if err != nil {
		t.Errorf("RunParallel empty: expected nil, got %v", err)
	}

	// All dead - FilterHealthy returns empty
	dc.MarkDead("h1")
	targets := []Target{&HostTarget{ResourceID: "h1", Addr: "1.1.1.1", User: "root"}}
	err = RunParallel(dc, targets, ParallelOptions{}, noop)
	if err != nil {
		t.Errorf("RunParallel all dead: expected nil, got %v", err)
	}
}

func TestRunParallel_BatchSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dc := NewDeployContext(ctx, nil, nil, nil, nil, "")

	var maxConcurrent int32
	var current int32
	targets := make([]Target, 5)
	for i := 0; i < 5; i++ {
		targets[i] = &HostTarget{ResourceID: string(rune('a' + i)), Addr: "1.1.1.1", User: "root"}
	}

	fn := func(ctx context.Context, t Target) error {
		c := atomic.AddInt32(&current, 1)
		defer atomic.AddInt32(&current, -1)
		for {
			m := atomic.LoadInt32(&maxConcurrent)
			if c > m && atomic.CompareAndSwapInt32(&maxConcurrent, m, c) {
				break
			}
			if m >= c {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		return nil
	}

	opts := ParallelOptions{BatchSize: 2}
	err := RunParallel(dc, targets, opts, fn)
	if err != nil {
		t.Fatalf("RunParallel: %v", err)
	}
	if maxConcurrent > 2 {
		t.Errorf("BatchSize=2: maxConcurrent=%d, expected <=2", maxConcurrent)
	}
}

func TestRunParallel_Retries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dc := NewDeployContext(ctx, nil, nil, nil, nil, "")

	h := &HostTarget{ResourceID: "h1", Addr: "1.1.1.1", User: "root"}
	targets := []Target{h}

	var attempts int32
	fn := func(ctx context.Context, t Target) error {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return errors.New("transient")
		}
		return nil
	}

	opts := ParallelOptions{Retries: 3, RetryDelay: time.Millisecond}
	err := RunParallel(dc, targets, opts, fn)
	if err != nil {
		t.Fatalf("RunParallel with retries: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestRunParallel_TolerateFailures_Percent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dc := NewDeployContext(ctx, nil, nil, nil, nil, "")

	targets := make([]Target, 10)
	for i := 0; i < 10; i++ {
		targets[i] = &HostTarget{ResourceID: string(rune('a' + i)), Addr: "1.1.1.1", User: "root"}
	}

	// 10%, 10 targets -> allow 1 failure
	fn := func(ctx context.Context, t Target) error {
		if t.ID() == "a" {
			return errors.New("failed")
		}
		return nil
	}

	opts := ParallelOptions{TolerateFailures: 0.1}
	err := RunParallel(dc, targets, opts, fn)
	if err != nil {
		t.Fatalf("TolerateFailures 10%%: expected nil, got %v", err)
	}
}

func TestRunParallel_TolerateFailures_Exceeded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dc := NewDeployContext(ctx, nil, nil, nil, nil, "")

	targets := make([]Target, 5)
	for i := 0; i < 5; i++ {
		targets[i] = &HostTarget{ResourceID: string(rune('a' + i)), Addr: "1.1.1.1", User: "root"}
	}

	// 5 targets, 10% = 0.5 floor = 0 allowed, any failure triggers abort
	fn := func(ctx context.Context, t Target) error {
		return errors.New("failed")
	}

	opts := ParallelOptions{TolerateFailures: 0.1}
	err := RunParallel(dc, targets, opts, fn)
	if err == nil {
		t.Error("TolerateFailures exceeded: expected error, got nil")
	}
	if err != nil {
		if !strings.Contains(err.Error(), "tolerance exceeded:") {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

func TestParseParallelOptions(t *testing.T) {
	// tolerate_failures "5%"
	step := Step{TolerateFailures: "5%"}
	opts := ParseParallelOptions(step)
	if opts.TolerateFailures != 0.05 {
		t.Errorf("tolerate_failures 5%%: expected 0.05, got %f", opts.TolerateFailures)
	}

	// tolerate_failures "2" (absolute)
	step = Step{TolerateFailures: "2"}
	opts = ParseParallelOptions(step)
	if opts.TolerateFailures != 2 {
		t.Errorf("tolerate_failures 2: expected 2, got %f", opts.TolerateFailures)
	}

	// strategy rolling -> batch_size 1
	step = Step{BatchSize: 10, With: map[string]interface{}{"strategy": "rolling"}}
	opts = ParseParallelOptions(step)
	if opts.BatchSize != 1 {
		t.Errorf("strategy rolling: expected BatchSize 1, got %d", opts.BatchSize)
	}

	// 未配置时默认 5%
	step = Step{}
	opts = ParseParallelOptions(step)
	if opts.TolerateFailures != 0.05 {
		t.Errorf("tolerate_failures unset: expected default 0.05, got %f", opts.TolerateFailures)
	}

	// 显式配置 "0" 时仍为 0
	step = Step{TolerateFailures: "0"}
	opts = ParseParallelOptions(step)
	if opts.TolerateFailures != 0 {
		t.Errorf("tolerate_failures 0: expected 0, got %f", opts.TolerateFailures)
	}
}
