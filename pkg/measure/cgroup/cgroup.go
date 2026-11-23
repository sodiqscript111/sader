package cgroup

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sodiqscript111/sader/pkg/damonfs"
)

type (
	Container struct {
		ID        string
		MemcgPath string
	}

	Config struct {
		Intervals damonfs.Intervals
		NrRegions damonfs.Range
		Windows   []time.Duration
		PageLevel bool
	}

	Usage struct {
		ID       string
		Bytes    uint64
		HotBytes uint64
		Windows  map[time.Duration]uint64
	}

	Monitor struct {
		kd         *damonfs.Kdamond
		cfg        Config
		containers []Container
	}
)

var ErrKernelTooOld = errors.New("cgroup: kernel lacks DAMOS filter-passed stats needed for per-container measurement")

func DefaultConfig() Config {
	return Config{
		Intervals: damonfs.DefaultIntervals,
		NrRegions: damonfs.Range{Min: 10, Max: 10000},
		Windows:   []time.Duration{time.Minute, 5 * time.Minute, time.Hour},
	}
}

func New(kd *damonfs.Kdamond, cfg Config) *Monitor {
	return &Monitor{kd: kd, cfg: cfg}
}

func (m *Monitor) Start(cs []Container) error {
	if err := validate(cs); err != nil {
		return err
	}
	m.containers = slices.Clone(cs)
	ctx := damonfs.Context{
		Operations: damonfs.OpsPaddr,
		Intervals:  m.cfg.Intervals,
		NrRegions:  m.cfg.NrRegions,
		Targets:    []damonfs.Target{{}},
		Schemes:    m.schemes(),
	}
	if len(ctx.Schemes) == 0 {
		ctx.Schemes = []damonfs.Scheme{damonfs.StatScheme(damonfs.MatchAll())}
	}
	if err := m.kd.Configure(ctx); err != nil {
		return err
	}
	ft, err := m.kd.Features()
	if err != nil {
		return err
	}
	if ft.FilterLayout == damonfs.LayoutNone || !ft.OpsFilterPassed {
		return fmt.Errorf("%w (filters=%q, sz_ops_filter_passed=%t)", ErrKernelTooOld, ft.FilterLayout, ft.OpsFilterPassed)
	}
	if len(m.containers) == 0 {
		if err := m.kd.UpdateSchemes(nil); err != nil {
			return err
		}
	}
	return m.kd.Start()
}

func (m *Monitor) SetContainers(cs []Container) error {
	if err := validate(cs); err != nil {
		return err
	}
	m.containers = slices.Clone(cs)
	return m.kd.UpdateSchemes(m.schemes())
}

func (m *Monitor) Containers() []Container { return slices.Clone(m.containers) }

func (m *Monitor) Stop() error { return m.kd.Stop() }

func (m *Monitor) Sample() ([]Usage, error) {
	if len(m.containers) == 0 {
		return nil, nil
	}
	if err := m.kd.UpdateTriedRegions(); err != nil {
		return nil, err
	}
	aggr := m.cfg.Intervals.Aggr
	out := make([]Usage, len(m.containers))
	for i, c := range m.containers {
		regions, err := m.kd.ReadFilteredTriedRegions(i)
		if err != nil {
			return nil, fmt.Errorf("container %s: %w", c.ID, err)
		}
		out[i] = m.usage(c.ID, regions, aggr)
	}
	return out, nil
}

func (m *Monitor) usage(id string, regions []damonfs.Region, aggr time.Duration) Usage {
	u := Usage{ID: id, Windows: make(map[time.Duration]uint64, len(m.cfg.Windows))}
	for _, w := range m.cfg.Windows {
		u.Windows[w] = 0
	}
	for _, r := range regions {
		b := r.SzFilterPassed
		if m.cfg.PageLevel {
			u.HotBytes += b
			continue
		}
		u.Bytes += b
		if r.NrAccesses > 0 {
			u.HotBytes += b
		}
		for _, w := range m.cfg.Windows {
			if r.NrAccesses > 0 || r.Age <= uint64(w/aggr) {
				u.Windows[w] += b
			}
		}
	}
	return u
}

func (m *Monitor) schemes() []damonfs.Scheme {
	out := make([]damonfs.Scheme, len(m.containers))
	for i, c := range m.containers {
		s := damonfs.StatScheme(damonfs.MatchAll())
		s.Filters = []damonfs.Filter{damonfs.OnlyMemcg(c.MemcgPath)}
		if m.cfg.PageLevel {
			s.Filters = append(s.Filters, damonfs.OnlyYoung())
		}
		out[i] = s
	}
	return out
}

func validate(cs []Container) error {
	ids := make(map[string]bool, len(cs))
	for _, c := range cs {
		if c.ID == "" {
			return errors.New("cgroup: container with empty ID")
		}
		if ids[c.ID] {
			return fmt.Errorf("cgroup: duplicate container ID %q", c.ID)
		}
		ids[c.ID] = true
		if !strings.HasPrefix(c.MemcgPath, "/") {
			return fmt.Errorf("cgroup: container %s: memcg path %q must start with /", c.ID, c.MemcgPath)
		}
	}
	return nil
}
