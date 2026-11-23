package schedule_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sodiqscript111/sader/pkg/schedule"
)

const mib = 1 << 20

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newScheduler(t *testing.T) *schedule.Scheduler {
	t.Helper()
	s, err := schedule.New(schedule.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewContainersAreDueImmediately(t *testing.T) {
	s := newScheduler(t)
	if due := s.Due(t0, []string{"b", "a"}); !slices.Equal(due, []string{"a", "b"}) {
		t.Fatalf("due = %v", due)
	}
}

func TestQuietContainerBacksOff(t *testing.T) {
	s := newScheduler(t)
	now := t0
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := s.Observe(now, "a", 200*mib); got != w {
			t.Fatalf("observation %d: interval %v, want %v", i, got, w)
		}
		now = now.Add(w)
	}
}

func TestChangeResetsToFastChecks(t *testing.T) {
	s := newScheduler(t)
	now := t0
	for range 4 {
		now = now.Add(s.Observe(now, "a", 200*mib))
	}
	if s.Interval("a") != 80*time.Second {
		t.Fatalf("backed-off interval = %v", s.Interval("a"))
	}
	if got := s.Observe(now, "a", 400*mib); got != 10*time.Second {
		t.Fatalf("after a jump: interval %v, want 10s", got)
	}
}

func TestSmallWobbleDoesNotCountAsChange(t *testing.T) {
	s := newScheduler(t)
	s.Observe(t0, "a", 200*mib)
	if got := s.Observe(t0.Add(10*time.Second), "a", 204*mib); got != 20*time.Second {
		t.Fatalf("4 MiB wobble reset the interval: %v", got)
	}
	if got := s.Observe(t0.Add(30*time.Second), "a", 240*mib); got != 10*time.Second {
		t.Fatalf("40 MiB change did not reset: %v", got)
	}
}

func TestDueRespectsIntervalsAndForgetsRemoved(t *testing.T) {
	s := newScheduler(t)
	ids := []string{"busy", "quiet"}
	s.Due(t0, ids)
	s.Observe(t0, "busy", 100*mib)
	s.Observe(t0, "quiet", 100*mib)
	s.Observe(t0.Add(10*time.Second), "quiet", 100*mib)

	if due := s.Due(t0.Add(10*time.Second), ids); !slices.Equal(due, []string{"busy"}) {
		t.Fatalf("at 10s: due = %v, want [busy]", due)
	}
	if due := s.Due(t0.Add(30*time.Second), ids); !slices.Equal(due, []string{"busy", "quiet"}) {
		t.Fatalf("at 30s: due = %v", due)
	}
	s.Due(t0.Add(30*time.Second), []string{"busy"})
	if s.Interval("quiet") != schedule.DefaultConfig().Min {
		t.Fatal("removed container was not forgotten")
	}
}

func TestNextDue(t *testing.T) {
	s := newScheduler(t)
	if _, ok := s.NextDue(); ok {
		t.Fatal("empty scheduler reported a next time")
	}
	s.Due(t0, []string{"a", "b"})
	s.Observe(t0, "a", 1)
	if next, ok := s.NextDue(); !ok || !next.IsZero() {
		t.Fatalf("unseen container should be due now, got %v %v", next, ok)
	}
	s.Observe(t0, "b", 1)
	if next, _ := s.NextDue(); !next.Equal(t0.Add(10 * time.Second)) {
		t.Fatalf("next = %v", next)
	}
}

func TestConfigValidate(t *testing.T) {
	for name, c := range map[string]schedule.Config{
		"zero min":     {Min: 0, Max: time.Second, BackoffRatio: 2},
		"max < min":    {Min: time.Minute, Max: time.Second, BackoffRatio: 2},
		"ratio below1": {Min: time.Second, Max: time.Minute, BackoffRatio: 0.5},
	} {
		if _, err := schedule.New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
