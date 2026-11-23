package cgroup_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sodiqscript111/sader/pkg/damonfs"
	"github.com/sodiqscript111/sader/pkg/damonfs/damonfstest"
	"github.com/sodiqscript111/sader/pkg/measure/cgroup"
	"github.com/sodiqscript111/sader/pkg/schedule"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

const mib = 1 << 20

func newMonitor(t *testing.T, k *damonfstest.Kernel, cfg cgroup.Config) *cgroup.Monitor {
	t.Helper()
	f := damonfs.New(k)
	if err := f.Reserve(1); err != nil {
		t.Fatal(err)
	}
	return cgroup.New(f.Kdamond(0), cfg)
}

var (
	appA = cgroup.Container{ID: "a", MemcgPath: "/kubepods.slice/pod-a"}
	appB = cgroup.Container{ID: "b", MemcgPath: "/kubepods.slice/pod-b"}
)

func scenario(k *damonfstest.Kernel) {
	k.SetRegions(0, []damonfs.Region{
		{Start: 0, End: 100 * mib, NrAccesses: 4, Age: 2},
		{Start: 100 * mib, End: 200 * mib, NrAccesses: 0, Age: 300},
		{Start: 200 * mib, End: 400 * mib, NrAccesses: 0, Age: 3000},
		{Start: 400 * mib, End: 1024 * mib, NrAccesses: 0, Age: 100000},
	})
	k.SetMemory(0, []damonfstest.Chunk{
		{Start: 0, End: 60 * mib, Memcg: appA.MemcgPath, Young: true},
		{Start: 60 * mib, End: 100 * mib, Memcg: appB.MemcgPath, Young: true},
		{Start: 100 * mib, End: 200 * mib, Memcg: appA.MemcgPath},
		{Start: 200 * mib, End: 300 * mib, Memcg: appB.MemcgPath},
		{Start: 400 * mib, End: 1000 * mib, Memcg: appA.MemcgPath},
	})
}

func TestPerContainerUsage(t *testing.T) {
	k := damonfstest.New()
	m := newMonitor(t, k, cgroup.DefaultConfig())
	if err := m.Start([]cgroup.Container{appA, appB}); err != nil {
		t.Fatal(err)
	}
	scenario(k)
	got, err := m.Sample()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ bytes, hot, w1m, w5m, w1h uint64 }{
		"a": {760 * mib, 60 * mib, 160 * mib, 160 * mib, 160 * mib},
		"b": {140 * mib, 40 * mib, 40 * mib, 140 * mib, 140 * mib},
	}
	for _, u := range got {
		w := want[u.ID]
		if u.Bytes != w.bytes || u.HotBytes != w.hot ||
			u.Windows[time.Minute] != w.w1m || u.Windows[5*time.Minute] != w.w5m || u.Windows[time.Hour] != w.w1h {
			t.Errorf("%s: bytes=%d hot=%d 1m=%d 5m=%d 1h=%d MiB, want %d/%d/%d/%d/%d", u.ID,
				u.Bytes/mib, u.HotBytes/mib, u.Windows[time.Minute]/mib, u.Windows[5*time.Minute]/mib, u.Windows[time.Hour]/mib,
				w.bytes/mib, w.hot/mib, w.w1m/mib, w.w5m/mib, w.w1h/mib)
		}
	}
}

func TestPageLevelUsesYoungPages(t *testing.T) {
	k := damonfstest.New()
	cfg := cgroup.DefaultConfig()
	cfg.PageLevel = true
	m := newMonitor(t, k, cfg)
	if err := m.Start([]cgroup.Container{appA, appB}); err != nil {
		t.Fatal(err)
	}
	scenario(k)
	got, err := m.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if got[0].HotBytes != 60*mib || got[1].HotBytes != 40*mib {
		t.Fatalf("page-level hot = %d / %d MiB, want 60 / 40", got[0].HotBytes/mib, got[1].HotBytes/mib)
	}
}

func TestSetContainersCommitsOnline(t *testing.T) {
	k := damonfstest.New()
	m := newMonitor(t, k, cgroup.DefaultConfig())
	if err := m.Start([]cgroup.Container{appA}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetContainers([]cgroup.Container{appA, appB}); err != nil {
		t.Fatal(err)
	}
	if k.Commits(0) != 1 {
		t.Fatalf("commits = %d, want 1", k.Commits(0))
	}
	scenario(k)
	got, err := m.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].ID != "b" || got[1].HotBytes != 40*mib {
		t.Fatalf("after adding b: %+v", got)
	}
}

func TestStartWithNoContainers(t *testing.T) {
	k := damonfstest.New()
	m := newMonitor(t, k, cgroup.DefaultConfig())
	if err := m.Start(nil); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Sample(); err != nil || got != nil {
		t.Fatalf("Sample with no containers = %v, %v", got, err)
	}
	if err := m.SetContainers([]cgroup.Container{appA}); err != nil {
		t.Fatal(err)
	}
}

func TestOldKernelRefused(t *testing.T) {
	k := damonfstest.New().Without("stats/sz_ops_filter_passed")
	m := newMonitor(t, k, cgroup.DefaultConfig())
	if err := m.Start([]cgroup.Container{appA}); !errors.Is(err, cgroup.ErrKernelTooOld) {
		t.Fatalf("got %v, want ErrKernelTooOld", err)
	}
}

func TestMissingFilterPassedIsAnErrorNotZero(t *testing.T) {
	k := damonfstest.New().Without("sz_filter_passed")
	m := newMonitor(t, k, cgroup.DefaultConfig())
	if err := m.Start([]cgroup.Container{appA, appB}); err != nil {
		t.Fatal(err)
	}
	scenario(k)
	got, err := m.Sample()
	if !errors.Is(err, damonfs.ErrUnsupportedFeature) {
		t.Fatalf("got %+v, %v; want ErrUnsupportedFeature instead of silent zeros", got, err)
	}
}

func TestSampleIDsReadsOnlyThoseContainers(t *testing.T) {
	k := damonfstest.New()
	m := newMonitor(t, k, cgroup.DefaultConfig())
	var cs []cgroup.Container
	for i := range 40 {
		cs = append(cs, cgroup.Container{ID: fmt.Sprintf("c%02d", i), MemcgPath: fmt.Sprintf("/c%02d", i)})
	}
	if err := m.Start(cs); err != nil {
		t.Fatal(err)
	}
	var regions []damonfs.Region
	for i := range 500 {
		regions = append(regions, damonfs.Region{Start: uint64(i) * mib, End: uint64(i+1) * mib, NrAccesses: uint64(i % 2)})
	}
	k.SetRegions(0, regions)

	before := k.Reads()
	if _, err := m.Sample(); err != nil {
		t.Fatal(err)
	}
	all := k.Reads() - before

	before = k.Reads()
	got, err := m.SampleIDs([]string{"c07"})
	if err != nil {
		t.Fatal(err)
	}
	one := k.Reads() - before
	if len(got) != 1 || got[0].ID != "c07" {
		t.Fatalf("SampleIDs returned %+v", got)
	}
	t.Logf("file reads: all 40 containers = %d, one container = %d", all, one)
	if one*30 > all {
		t.Fatalf("sampling one container cost %d reads vs %d for all 40", one, all)
	}
	if _, err := m.SampleIDs([]string{"nope"}); err == nil {
		t.Fatal("unknown container accepted")
	}
}

func TestAdaptiveCheckingSavesReads(t *testing.T) {
	k := damonfstest.New()
	m := newMonitor(t, k, cgroup.DefaultConfig())
	var cs []cgroup.Container
	var ids []string
	for i := range 40 {
		id := fmt.Sprintf("c%02d", i)
		ids = append(ids, id)
		cs = append(cs, cgroup.Container{ID: id, MemcgPath: "/" + id})
	}
	if err := m.Start(cs); err != nil {
		t.Fatal(err)
	}
	var regions []damonfs.Region
	for i := range 400 {
		regions = append(regions, damonfs.Region{Start: uint64(i) * mib, End: uint64(i+1) * mib, NrAccesses: 1})
	}
	k.SetRegions(0, regions)
	memory := func(busyMiB uint64) {
		chunks := []damonfstest.Chunk{{Start: 0, End: busyMiB * mib, Memcg: "/c00"}}
		for i := 1; i < 40; i++ {
			start := uint64(100+i*5) * mib
			chunks = append(chunks, damonfstest.Chunk{Start: start, End: start + 5*mib, Memcg: fmt.Sprintf("/c%02d", i)})
		}
		k.SetMemory(0, chunks)
	}

	sched, err := schedule.New(schedule.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	start := k.Reads()
	checks := map[string]int{}
	for step := 0; step <= 60; step++ {
		now := t0.Add(time.Duration(step) * 10 * time.Second)
		memory([]uint64{50, 99}[step%2])
		due := sched.Due(now, ids)
		usages, err := m.SampleIDs(due)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range usages {
			checks[u.ID]++
			sched.Observe(now, u.ID, u.Windows[time.Minute])
		}
	}
	adaptive := k.Reads() - start

	start = k.Reads()
	for range 61 {
		if _, err := m.Sample(); err != nil {
			t.Fatal(err)
		}
	}
	everything := k.Reads() - start

	t.Logf("10 simulated minutes, 40 containers: check-all = %d reads, adaptive = %d reads (busy checked %d times, a quiet one %d times)",
		everything, adaptive, checks["c00"], checks["c01"])
	if checks["c00"] != 61 {
		t.Errorf("busy container checked %d times, want every time (61)", checks["c00"])
	}
	if adaptive*5 > everything {
		t.Errorf("adaptive used %d reads vs %d: less than a 5x saving", adaptive, everything)
	}
}

func TestLegacyFilterLayoutWorks(t *testing.T) {
	k := damonfstest.New().Without("core_filters", "ops_filters")
	m := newMonitor(t, k, cgroup.DefaultConfig())
	if err := m.Start([]cgroup.Container{appA, appB}); err != nil {
		t.Fatal(err)
	}
	scenario(k)
	got, err := m.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if got[0].HotBytes != 60*mib {
		t.Fatalf("legacy layout hot = %d MiB, want 60", got[0].HotBytes/mib)
	}
}

func TestInvalidContainers(t *testing.T) {
	k := damonfstest.New()
	m := newMonitor(t, k, cgroup.DefaultConfig())
	for name, cs := range map[string][]cgroup.Container{
		"empty id":      {{ID: "", MemcgPath: "/x"}},
		"duplicate id":  {appA, appA},
		"relative path": {{ID: "x", MemcgPath: "x"}},
	} {
		if err := m.Start(cs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
