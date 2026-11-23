package damonfs_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sodiqscript111/sader/pkg/damonfs"
	"github.com/sodiqscript111/sader/pkg/damonfs/damonfstest"
)

const mib = 1 << 20

func vaddrContext(pids ...int) damonfs.Context {
	ts := make([]damonfs.Target, len(pids))
	for i, p := range pids {
		ts[i] = damonfs.Target{PID: p}
	}
	return damonfs.Context{
		Operations: damonfs.OpsVaddr,
		Intervals:  damonfs.DefaultIntervals,
		NrRegions:  damonfs.DefaultNrRegions,
		Targets:    ts,
		Schemes:    []damonfs.Scheme{damonfs.StatScheme(damonfs.MatchAll())},
	}
}

func setup(t *testing.T, slots int) (*damonfstest.Kernel, *damonfs.FS) {
	t.Helper()
	k := damonfstest.New()
	f := damonfs.New(k)
	if err := f.Reserve(slots); err != nil {
		t.Fatalf("Reserve(%d): %v", slots, err)
	}
	return k, f
}

func mustFile(t *testing.T, k *damonfstest.Kernel, name, want string) {
	t.Helper()
	got, ok := k.File(name)
	if !ok {
		t.Fatalf("%s does not exist", name)
	}
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func TestOpenWithoutDAMON(t *testing.T) {
	_, err := damonfs.Open(t.TempDir())
	if !errors.Is(err, damonfs.ErrNotSupported) {
		t.Fatalf("Open on empty dir: got %v, want ErrNotSupported", err)
	}
}

func TestReserveOnlyGrows(t *testing.T) {
	k, f := setup(t, 3)
	if err := f.Reserve(2); err != nil {
		t.Fatal(err)
	}
	mustFile(t, k, "kdamonds/nr_kdamonds", "3")
}

func TestConfigureWritesSysfs(t *testing.T) {
	k, f := setup(t, 2)
	kd := f.Kdamond(1)
	if err := kd.Configure(vaddrContext(101, 202)); err != nil {
		t.Fatal(err)
	}
	ctx := "kdamonds/1/contexts/0/"
	for name, want := range map[string]string{
		"kdamonds/1/contexts/nr_contexts":                "1",
		ctx + "operations":                               "vaddr",
		ctx + "monitoring_attrs/intervals/sample_us":     "5000",
		ctx + "monitoring_attrs/intervals/aggr_us":       "100000",
		ctx + "monitoring_attrs/intervals/update_us":     "1000000",
		ctx + "monitoring_attrs/nr_regions/min":          "10",
		ctx + "monitoring_attrs/nr_regions/max":          "1000",
		ctx + "targets/nr_targets":                       "2",
		ctx + "targets/0/pid_target":                     "101",
		ctx + "targets/1/pid_target":                     "202",
		ctx + "schemes/nr_schemes":                       "1",
		ctx + "schemes/0/action":                         "stat",
		ctx + "schemes/0/access_pattern/sz/max":          "18446744073709551615",
		ctx + "schemes/0/access_pattern/nr_accesses/max": "4294967295",
	} {
		mustFile(t, k, name, want)
	}

	mustFile(t, k, "kdamonds/0/contexts/nr_contexts", "0")
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*damonfs.Context){
		"unknown ops":      func(c *damonfs.Context) { c.Operations = "bogus" },
		"sample > aggr":    func(c *damonfs.Context) { c.Intervals.Sample = time.Second },
		"zero interval":    func(c *damonfs.Context) { c.Intervals.Update = 0 },
		"too few regions":  func(c *damonfs.Context) { c.NrRegions = damonfs.Range{Min: 2, Max: 10} },
		"min > max":        func(c *damonfs.Context) { c.NrRegions = damonfs.Range{Min: 100, Max: 10} },
		"no targets":       func(c *damonfs.Context) { c.Targets = nil },
		"missing pid":      func(c *damonfs.Context) { c.Targets = []damonfs.Target{{PID: 0}} },
		"duplicate pid":    func(c *damonfs.Context) { c.Targets = []damonfs.Target{{PID: 7}, {PID: 7}} },
		"bad scheme range": func(c *damonfs.Context) { c.Schemes[0].AccessPattern.Age = damonfs.Range{Min: 5, Max: 1} },
		"empty action":     func(c *damonfs.Context) { c.Schemes[0].Action = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := vaddrContext(1)
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid context")
			}
		})
	}
	if err := vaddrContext(1).Validate(); err != nil {
		t.Fatalf("valid context rejected: %v", err)
	}
}

func TestStartStop(t *testing.T) {
	_, f := setup(t, 1)
	kd := f.Kdamond(0)
	if err := kd.Start(); err == nil {
		t.Fatal("Start succeeded without a context")
	}
	if err := kd.Configure(vaddrContext(42)); err != nil {
		t.Fatal(err)
	}
	if err := kd.Start(); err != nil {
		t.Fatal(err)
	}
	if on, _ := kd.Running(); !on {
		t.Fatal("not running after Start")
	}
	if pid, _ := kd.PID(); pid <= 0 {
		t.Fatalf("PID = %d while running", pid)
	}
	if err := kd.Configure(vaddrContext(43)); !errors.Is(err, damonfs.ErrRunning) {
		t.Fatalf("Configure while running: got %v, want ErrRunning", err)
	}
	if err := kd.Stop(); err != nil {
		t.Fatal(err)
	}
	if pid, _ := kd.PID(); pid != -1 {
		t.Fatalf("PID = %d after Stop, want -1", pid)
	}
}

func TestReserveWhileRunningIsBusy(t *testing.T) {
	_, f := setup(t, 1)
	kd := f.Kdamond(0)
	if err := kd.Configure(vaddrContext(42)); err != nil {
		t.Fatal(err)
	}
	if err := kd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := f.Reserve(2); !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("Reserve while running: got %v, want EBUSY", err)
	}
}

func TestUpdateTargetsCommitsWhenRunning(t *testing.T) {
	k, f := setup(t, 1)
	kd := f.Kdamond(0)
	if err := kd.Configure(vaddrContext(1)); err != nil {
		t.Fatal(err)
	}

	if err := kd.UpdateTargets([]damonfs.Target{{PID: 2}}); err != nil {
		t.Fatal(err)
	}
	if n := k.Commits(0); n != 0 {
		t.Fatalf("commits while off = %d, want 0", n)
	}
	if err := kd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := kd.UpdateTargets([]damonfs.Target{{PID: 3}, {PID: 4}}); err != nil {
		t.Fatal(err)
	}
	if n := k.Commits(0); n != 1 {
		t.Fatalf("commits while on = %d, want 1", n)
	}
	mustFile(t, k, "kdamonds/0/contexts/0/targets/nr_targets", "2")
	mustFile(t, k, "kdamonds/0/contexts/0/targets/1/pid_target", "4")
	if err := kd.UpdateTargets(nil); err == nil {
		t.Fatal("UpdateTargets accepted an empty target list")
	}
}

func TestSnapshotTriedRegions(t *testing.T) {
	k, f := setup(t, 1)
	kd := f.Kdamond(0)
	if err := kd.Configure(vaddrContext(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := kd.SnapshotTriedRegions(0); err == nil {
		t.Fatal("snapshot succeeded while off")
	}
	if err := kd.Start(); err != nil {
		t.Fatal(err)
	}

	var want []damonfs.Region
	for i := range 12 {
		var acc uint64
		if i%2 == 0 {
			acc = uint64(i + 1)
		}
		start := uint64(i) * mib
		want = append(want, damonfs.Region{Start: start, End: start + mib, NrAccesses: acc, Age: uint64(i)})
	}
	k.SetRegions(0, want)

	got, err := kd.SnapshotTriedRegions(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d regions, want %d", len(got), len(want))
	}
	for i := range want {
		w := want[i]
		w.SzFilterPassed = w.Size()
		if got[i] != w {
			t.Errorf("region %d = %+v, want %+v", i, got[i], w)
		}
	}
	if b := damonfs.TotalBytes(got); b != 12*mib {
		t.Errorf("TotalBytes = %d, want %d", b, 12*mib)
	}
	if b := damonfs.HotBytes(got); b != 6*mib {
		t.Errorf("HotBytes = %d, want %d", b, 6*mib)
	}
	if b, _ := kd.TriedBytes(0); b != 12*mib {
		t.Errorf("TriedBytes = %d, want %d", b, 12*mib)
	}
}

func TestWorkingSetBytes(t *testing.T) {
	aggr := 100 * time.Millisecond
	regions := []damonfs.Region{
		{Start: 0, End: 100 * mib, NrAccesses: 5, Age: 3},
		{Start: 100 * mib, End: 150 * mib, NrAccesses: 0, Age: 300},
		{Start: 150 * mib, End: 400 * mib, NrAccesses: 0, Age: 6000},
		{Start: 400 * mib, End: 1024 * mib, NrAccesses: 0, Age: 100000},
	}
	for _, tc := range []struct {
		window time.Duration
		want   uint64
	}{
		{time.Second, 100 * mib},
		{time.Minute, 150 * mib},
		{time.Hour, 400 * mib},
		{24 * time.Hour, 1024 * mib},
	} {
		if got := damonfs.WorkingSetBytes(regions, tc.window, aggr); got != tc.want {
			t.Errorf("window %v: got %d MiB, want %d MiB", tc.window, got/mib, tc.want/mib)
		}
	}
}

func TestPatternSchemesMatchRegionMath(t *testing.T) {
	k, f := setup(t, 1)
	kd := f.Kdamond(0)
	aggr := damonfs.DefaultIntervals.Aggr
	ctx := vaddrContext(1)
	ctx.Schemes = []damonfs.Scheme{
		damonfs.StatScheme(damonfs.MatchAll()),
		damonfs.StatScheme(damonfs.HotPattern()),
		damonfs.StatScheme(damonfs.RecentlyIdlePattern(time.Minute, aggr)),
		damonfs.StatScheme(damonfs.RecentlyIdlePattern(time.Hour, aggr)),
	}
	if err := kd.Configure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := kd.Start(); err != nil {
		t.Fatal(err)
	}
	regions := []damonfs.Region{
		{Start: 0, End: 200 * mib, NrAccesses: 3, Age: 1},
		{Start: 200 * mib, End: 300 * mib, NrAccesses: 0, Age: 50},
		{Start: 300 * mib, End: 600 * mib, NrAccesses: 0, Age: 3000},
		{Start: 600 * mib, End: 1024 * mib, NrAccesses: 0, Age: 90000},
	}
	k.SetRegions(0, regions)
	if err := kd.UpdateTriedBytes(); err != nil {
		t.Fatal(err)
	}
	read := func(i int) uint64 {
		b, err := kd.TriedBytes(i)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if got := read(0); got != 1024*mib {
		t.Errorf("all = %d MiB", got/mib)
	}
	hot := read(1)
	if hot != damonfs.HotBytes(regions) {
		t.Errorf("hot scheme = %d MiB, region math = %d MiB", hot/mib, damonfs.HotBytes(regions)/mib)
	}
	for i, w := range map[int]time.Duration{2: time.Minute, 3: time.Hour} {
		got, want := hot+read(i), damonfs.WorkingSetBytes(regions, w, aggr)
		if got != want {
			t.Errorf("window %v: schemes = %d MiB, region math = %d MiB", w, got/mib, want/mib)
		}
	}
}

func TestSchemeStatsAccumulate(t *testing.T) {
	k, f := setup(t, 1)
	kd := f.Kdamond(0)
	if err := kd.Configure(vaddrContext(1)); err != nil {
		t.Fatal(err)
	}
	if err := kd.Start(); err != nil {
		t.Fatal(err)
	}
	k.SetRegions(0, []damonfs.Region{{Start: 0, End: mib}})
	for range 3 {
		if err := kd.UpdateSchemesStats(); err != nil {
			t.Fatal(err)
		}
	}
	st, err := kd.SchemeStats(0)
	if err != nil {
		t.Fatal(err)
	}
	if st.NrTried != 3 || st.SzTried != 3*mib {
		t.Fatalf("stats = %+v, want 3 tried / 3 MiB", st)
	}
}

func TestFeatures(t *testing.T) {
	_, f := setup(t, 1)
	kd := f.Kdamond(0)
	if _, err := kd.Features(); err == nil {
		t.Fatal("Features succeeded before Configure")
	}
	if err := kd.Configure(vaddrContext(1)); err != nil {
		t.Fatal(err)
	}
	ft, err := kd.Features()
	if err != nil {
		t.Fatal(err)
	}
	if !ft.SchemeApplyInterval || !ft.TriedBytes || !ft.IntervalsAutoTuning || len(ft.Operations) != 3 {
		t.Fatalf("full fake: %+v", ft)
	}

}

func TestOlderKernel(t *testing.T) {
	k := damonfstest.New().Without("apply_interval_us", "tried_regions/total_bytes", "monitoring_attrs/intervals/intervals_goal")
	f := damonfs.New(k)
	if err := f.Reserve(1); err != nil {
		t.Fatal(err)
	}
	kd := f.Kdamond(0)
	if err := kd.Configure(vaddrContext(1)); err != nil {
		t.Fatal(err)
	}
	ft, err := kd.Features()
	if err != nil {
		t.Fatal(err)
	}
	if ft.SchemeApplyInterval || ft.TriedBytes || ft.IntervalsAutoTuning {
		t.Fatalf("older kernel reported features: %+v", ft)
	}
	ctx := vaddrContext(1)
	ctx.Schemes[0].ApplyInterval = time.Second
	if err := kd.Configure(ctx); !errors.Is(err, damonfs.ErrUnsupportedFeature) {
		t.Fatalf("got %v, want ErrUnsupportedFeature", err)
	}
}

func TestConfigurePaddr(t *testing.T) {
	_, f := setup(t, 1)
	ctx := vaddrContext(1)
	ctx.Operations = damonfs.OpsPaddr
	ctx.Targets = []damonfs.Target{{}}
	if err := f.Kdamond(0).Configure(ctx); err != nil {
		t.Fatalf("paddr: %v", err)
	}
}

func TestOSBackend(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "kdamonds")
	if err := os.MkdirAll(filepath.Join(dir, "0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nr_kdamonds"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := damonfs.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.NrKdamonds()
	if err != nil || n != 1 {
		t.Fatalf("NrKdamonds = %d, %v", n, err)
	}
	if err := f.SetNrKdamonds(4); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.NrKdamonds(); n != 4 {
		t.Fatalf("after write: %d", n)
	}
	b := damonfs.OSBackend(root)
	if err := b.WriteFile("kdamonds/0/state", []byte("on")); err == nil {
		t.Fatal("WriteFile created a missing file")
	}
	names, err := b.ReadDir("kdamonds")
	if err != nil || len(names) != 2 || names[0] != "0" || names[1] != "nr_kdamonds" {
		t.Fatalf("ReadDir = %v, %v", names, err)
	}
}
