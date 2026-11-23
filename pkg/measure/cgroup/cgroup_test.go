package cgroup_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sodiqscript111/sader/pkg/damonfs"
	"github.com/sodiqscript111/sader/pkg/damonfs/damonfstest"
	"github.com/sodiqscript111/sader/pkg/measure/cgroup"
)

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
