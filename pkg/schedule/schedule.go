package schedule

import (
	"errors"
	"slices"
	"time"
)

type (
	Config struct {
		Min          time.Duration
		Max          time.Duration
		RelChange    float64
		AbsChange    uint64
		BackoffRatio float64
	}

	entry struct {
		last     uint64
		interval time.Duration
		next     time.Time
		seen     bool
	}

	Scheduler struct {
		cfg     Config
		entries map[string]*entry
	}
)

func DefaultConfig() Config {
	return Config{
		Min:          10 * time.Second,
		Max:          5 * time.Minute,
		RelChange:    0.05,
		AbsChange:    8 << 20,
		BackoffRatio: 2,
	}
}

func (c Config) Validate() error {
	if c.Min <= 0 || c.Max < c.Min {
		return errors.New("schedule: need 0 < Min <= Max")
	}
	if c.RelChange < 0 {
		return errors.New("schedule: negative RelChange")
	}
	if c.BackoffRatio < 1 {
		return errors.New("schedule: BackoffRatio must be at least 1")
	}
	return nil
}

func New(cfg Config) (*Scheduler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Scheduler{cfg: cfg, entries: map[string]*entry{}}, nil
}

func (s *Scheduler) Due(now time.Time, ids []string) []string {
	keep := make(map[string]bool, len(ids))
	var due []string
	for _, id := range ids {
		keep[id] = true
		e, ok := s.entries[id]
		if !ok {
			e = &entry{interval: s.cfg.Min}
			s.entries[id] = e
		}
		if !e.seen || !now.Before(e.next) {
			due = append(due, id)
		}
	}
	for id := range s.entries {
		if !keep[id] {
			delete(s.entries, id)
		}
	}
	slices.Sort(due)
	return due
}

func (s *Scheduler) Observe(now time.Time, id string, value uint64) time.Duration {
	e, ok := s.entries[id]
	if !ok {
		e = &entry{interval: s.cfg.Min}
		s.entries[id] = e
	}
	switch {
	case !e.seen || s.changed(e.last, value):
		e.interval = s.cfg.Min
	default:
		e.interval = min(time.Duration(float64(e.interval)*s.cfg.BackoffRatio), s.cfg.Max)
	}
	e.last, e.seen, e.next = value, true, now.Add(e.interval)
	return e.interval
}

func (s *Scheduler) Interval(id string) time.Duration {
	if e, ok := s.entries[id]; ok {
		return e.interval
	}
	return s.cfg.Min
}

func (s *Scheduler) NextDue() (time.Time, bool) {
	var next time.Time
	found := false
	for _, e := range s.entries {
		if !e.seen {
			return time.Time{}, true
		}
		if !found || e.next.Before(next) {
			next, found = e.next, true
		}
	}
	return next, found
}

func (s *Scheduler) changed(old, cur uint64) bool {
	diff := cur - old
	if cur < old {
		diff = old - cur
	}
	return diff > max(s.cfg.AbsChange, uint64(float64(old)*s.cfg.RelChange))
}
