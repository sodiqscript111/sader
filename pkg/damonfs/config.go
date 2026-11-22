package damonfs

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type (
	Operations string

	Intervals struct {
		Sample time.Duration
		Aggr   time.Duration
		Update time.Duration
	}

	Range struct{ Min, Max uint64 }

	Target struct{ PID int }

	Context struct {
		Operations Operations
		Intervals  Intervals
		NrRegions  Range
		Targets    []Target
		Schemes    []Scheme
	}

	Action string

	AccessPattern struct {
		Size       Range
		NrAccesses Range
		Age        Range
	}

	Scheme struct {
		Action        Action
		AccessPattern AccessPattern
		ApplyInterval time.Duration
	}
)

const (
	OpsVaddr  Operations = "vaddr"
	OpsFvaddr Operations = "fvaddr"
	OpsPaddr  Operations = "paddr"
)

func (o Operations) needsPID() bool { return o == OpsVaddr || o == OpsFvaddr }

var DefaultIntervals = Intervals{
	Sample: 5 * time.Millisecond,
	Aggr:   100 * time.Millisecond,
	Update: time.Second,
}

var DefaultNrRegions = Range{Min: 10, Max: 1000}

func (c Context) Validate() error {
	switch c.Operations {
	case OpsVaddr, OpsFvaddr, OpsPaddr:
	default:
		return fmt.Errorf("damonfs: unknown operations %q", c.Operations)
	}
	iv := c.Intervals
	if iv.Sample < time.Microsecond || iv.Aggr < time.Microsecond || iv.Update < time.Microsecond {
		return errors.New("damonfs: intervals must be at least 1µs")
	}
	if iv.Sample > iv.Aggr {
		return fmt.Errorf("damonfs: sample interval %v exceeds aggregation interval %v", iv.Sample, iv.Aggr)
	}
	if c.NrRegions.Min < 3 {
		return fmt.Errorf("damonfs: min regions %d is below the kernel minimum of 3", c.NrRegions.Min)
	}
	if c.NrRegions.Min > c.NrRegions.Max {
		return fmt.Errorf("damonfs: min regions %d exceeds max %d", c.NrRegions.Min, c.NrRegions.Max)
	}
	if err := validateTargets(c.Operations, c.Targets); err != nil {
		return err
	}
	for i, s := range c.Schemes {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("scheme %d: %w", i, err)
		}
	}
	return nil
}

func validateTargets(ops Operations, ts []Target) error {
	if len(ts) == 0 {
		return errors.New("damonfs: at least one target is required")
	}
	if !ops.needsPID() {
		return nil
	}
	seen := make(map[int]bool, len(ts))
	for _, t := range ts {
		if t.PID <= 0 {
			return fmt.Errorf("damonfs: %s target needs a PID, got %d", ops, t.PID)
		}
		if seen[t.PID] {
			return fmt.Errorf("damonfs: duplicate target PID %d", t.PID)
		}
		seen[t.PID] = true
	}
	return nil
}

const ActionStat Action = "stat"

const maxCount = math.MaxUint32

func MatchAll() AccessPattern {
	return AccessPattern{
		Size:       Range{0, math.MaxUint64},
		NrAccesses: Range{0, maxCount},
		Age:        Range{0, maxCount},
	}
}

func HotPattern() AccessPattern {
	p := MatchAll()
	p.NrAccesses = Range{1, maxCount}
	return p
}

func RecentlyIdlePattern(maxIdle, aggr time.Duration) AccessPattern {
	p := MatchAll()
	p.NrAccesses = Range{0, 0}
	p.Age = Range{0, uint64(maxIdle / aggr)}
	return p
}

func StatScheme(p AccessPattern) Scheme { return Scheme{Action: ActionStat, AccessPattern: p} }

func (s Scheme) Validate() error {
	if s.Action == "" {
		return errors.New("damonfs: scheme action is empty")
	}
	for name, r := range map[string]Range{
		"sz": s.AccessPattern.Size, "nr_accesses": s.AccessPattern.NrAccesses, "age": s.AccessPattern.Age,
	} {
		if r.Min > r.Max {
			return fmt.Errorf("damonfs: %s min %d exceeds max %d", name, r.Min, r.Max)
		}
	}
	if s.ApplyInterval < 0 {
		return errors.New("damonfs: negative apply interval")
	}
	return nil
}

func micros(d time.Duration) uint64 { return uint64(d / time.Microsecond) }
