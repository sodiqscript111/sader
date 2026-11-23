package damonfs

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

type (
	State string

	Kdamond struct {
		fs  *FS
		idx int
		dir string
	}

	SchemeStats struct {
		NrTried, SzTried     uint64
		NrApplied, SzApplied uint64
		QtExceeds            uint64
		SzOpsFilterPassed    uint64
	}

	Features struct {
		Operations          []string
		SchemeApplyInterval bool
		TriedBytes          bool
		IntervalsAutoTuning bool
		FilterLayout        FilterLayout
		OpsFilterPassed     bool
	}
)

const (
	StateOn  State = "on"
	StateOff State = "off"
)

func (k *Kdamond) Index() int { return k.idx }

func (k *Kdamond) ctxDir() string { return path.Join(k.dir, "contexts", "0") }

func (k *Kdamond) schemeDir(i int) string {
	return path.Join(k.ctxDir(), "schemes", strconv.Itoa(i))
}

func (k *Kdamond) State() (State, error) {
	s, err := k.fs.read(k.dir + "/state")
	return State(s), err
}

func (k *Kdamond) Running() (bool, error) {
	s, err := k.State()
	return s == StateOn, err
}

func (k *Kdamond) PID() (int, error) {
	s, err := k.fs.read(k.dir + "/pid")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

func (k *Kdamond) Start() error { return k.command("on") }

func (k *Kdamond) Stop() error { return k.command("off") }

func (k *Kdamond) Commit() error { return k.command("commit") }

func (k *Kdamond) UpdateSchemesStats() error { return k.command("update_schemes_stats") }

func (k *Kdamond) UpdateTriedRegions() error { return k.command("update_schemes_tried_regions") }

func (k *Kdamond) UpdateTriedBytes() error { return k.command("update_schemes_tried_bytes") }

func (k *Kdamond) ClearTriedRegions() error { return k.command("clear_schemes_tried_regions") }

func (k *Kdamond) command(cmd string) error { return k.fs.write(k.dir+"/state", cmd) }

func (k *Kdamond) AvailableOperations() ([]string, error) {
	name := k.ctxDir() + "/avail_operations"
	if !k.fs.b.Exists(name) {
		return nil, nil
	}
	s, err := k.fs.read(name)
	if err != nil {
		return nil, err
	}
	return strings.Fields(s), nil
}

func (k *Kdamond) Configure(c Context) error {
	if err := c.Validate(); err != nil {
		return err
	}
	running, err := k.Running()
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("damonfs: configure kdamond %d: %w (use UpdateTargets for online changes)", k.idx, ErrRunning)
	}

	if err := k.fs.writeUint(k.dir+"/contexts/nr_contexts", 1); err != nil {
		return err
	}
	avail, err := k.AvailableOperations()
	if err != nil {
		return err
	}
	if avail != nil && !slices.Contains(avail, string(c.Operations)) {
		return fmt.Errorf("%w: operations %q (kernel offers %v)", ErrUnsupportedFeature, c.Operations, avail)
	}

	ctx := k.ctxDir()
	iv := ctx + "/monitoring_attrs/intervals/"
	nr := ctx + "/monitoring_attrs/nr_regions/"
	writes := []struct {
		name string
		val  uint64
	}{
		{iv + "sample_us", micros(c.Intervals.Sample)},
		{iv + "aggr_us", micros(c.Intervals.Aggr)},
		{iv + "update_us", micros(c.Intervals.Update)},
		{nr + "min", c.NrRegions.Min},
		{nr + "max", c.NrRegions.Max},
	}
	if err := k.fs.write(ctx+"/operations", string(c.Operations)); err != nil {
		return err
	}
	for _, w := range writes {
		if err := k.fs.writeUint(w.name, w.val); err != nil {
			return err
		}
	}
	if err := k.writeTargets(c.Targets); err != nil {
		return err
	}
	return k.writeSchemes(c.Schemes)
}

func (k *Kdamond) UpdateSchemes(schemes []Scheme) error {
	for i, s := range schemes {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("scheme %d: %w", i, err)
		}
	}
	if err := k.writeSchemes(schemes); err != nil {
		return err
	}
	running, err := k.Running()
	if err != nil {
		return err
	}
	if running {
		return k.Commit()
	}
	return nil
}

func (k *Kdamond) writeSchemes(schemes []Scheme) error {
	if err := k.fs.writeUint(k.ctxDir()+"/schemes/nr_schemes", uint64(len(schemes))); err != nil {
		return err
	}
	for i, s := range schemes {
		if err := k.writeScheme(i, s); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kdamond) UpdateTargets(ts []Target) error {
	ops, err := k.fs.read(k.ctxDir() + "/operations")
	if err != nil {
		return err
	}
	if err := validateTargets(Operations(ops), ts); err != nil {
		return err
	}
	if err := k.writeTargets(ts); err != nil {
		return err
	}
	running, err := k.Running()
	if err != nil {
		return err
	}
	if running {
		return k.Commit()
	}
	return nil
}

func (k *Kdamond) writeTargets(ts []Target) error {
	dir := k.ctxDir() + "/targets"
	if err := k.fs.writeUint(dir+"/nr_targets", uint64(len(ts))); err != nil {
		return err
	}
	for i, t := range ts {
		td := path.Join(dir, strconv.Itoa(i))
		if t.PID > 0 {
			if err := k.fs.writeUint(td+"/pid_target", uint64(t.PID)); err != nil {
				return err
			}
		}
		if len(t.Regions) == 0 {
			continue
		}
		if err := k.fs.writeUint(td+"/regions/nr_regions", uint64(len(t.Regions))); err != nil {
			return err
		}
		for j, r := range t.Regions {
			rd := path.Join(td, "regions", strconv.Itoa(j))
			if err := k.fs.writeUint(rd+"/start", r.Start); err != nil {
				return err
			}
			if err := k.fs.writeUint(rd+"/end", r.End); err != nil {
				return err
			}
		}
	}
	return nil
}

func (k *Kdamond) writeScheme(i int, s Scheme) error {
	dir := k.schemeDir(i)
	if err := k.fs.write(dir+"/action", string(s.Action)); err != nil {
		return err
	}
	ap := s.AccessPattern
	for _, r := range []struct {
		name string
		rng  Range
	}{{"sz", ap.Size}, {"nr_accesses", ap.NrAccesses}, {"age", ap.Age}} {
		base := path.Join(dir, "access_pattern", r.name)
		if err := k.fs.writeUint(base+"/min", r.rng.Min); err != nil {
			return err
		}
		if err := k.fs.writeUint(base+"/max", r.rng.Max); err != nil {
			return err
		}
	}
	if s.ApplyInterval > 0 {
		name := dir + "/apply_interval_us"
		if !k.fs.b.Exists(name) {
			return fmt.Errorf("%w: scheme apply interval", ErrUnsupportedFeature)
		}
		if err := k.fs.writeUint(name, micros(s.ApplyInterval)); err != nil {
			return err
		}
	}
	return k.writeFilters(dir, s.Filters)
}

func (k *Kdamond) SchemeStats(i int) (SchemeStats, error) {
	dir := k.schemeDir(i) + "/stats/"
	var st SchemeStats
	for _, f := range []struct {
		name string
		dst  *uint64
	}{
		{"nr_tried", &st.NrTried}, {"sz_tried", &st.SzTried},
		{"nr_applied", &st.NrApplied}, {"sz_applied", &st.SzApplied},
		{"qt_exceeds", &st.QtExceeds},
	} {
		v, err := k.fs.readUint(dir + f.name)
		if err != nil {
			return SchemeStats{}, err
		}
		*f.dst = v
	}
	if name := dir + "sz_ops_filter_passed"; k.fs.b.Exists(name) {
		v, err := k.fs.readUint(name)
		if err != nil {
			return SchemeStats{}, err
		}
		st.SzOpsFilterPassed = v
	}
	return st, nil
}

func (k *Kdamond) Features() (Features, error) {
	nc, err := k.fs.readUint(k.dir + "/contexts/nr_contexts")
	if err != nil {
		return Features{}, err
	}
	if nc == 0 {
		return Features{}, fmt.Errorf("damonfs: kdamond %d has no context; call Configure first", k.idx)
	}
	ops, err := k.AvailableOperations()
	if err != nil {
		return Features{}, err
	}
	b := k.fs.b
	s0 := k.schemeDir(0)
	return Features{
		Operations:          ops,
		SchemeApplyInterval: b.Exists(s0 + "/apply_interval_us"),
		TriedBytes:          b.Exists(s0 + "/tried_regions/total_bytes"),
		IntervalsAutoTuning: b.Exists(k.ctxDir() + "/monitoring_attrs/intervals/intervals_goal"),
		FilterLayout:        k.filterLayout(s0),
		OpsFilterPassed:     b.Exists(s0 + "/stats/sz_ops_filter_passed"),
	}, nil
}
