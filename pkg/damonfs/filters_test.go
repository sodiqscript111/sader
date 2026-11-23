package damonfs_test

import (
	"errors"
	"testing"

	"github.com/sodiqscript111/sader/pkg/damonfs"
	"github.com/sodiqscript111/sader/pkg/damonfs/damonfstest"
)

func paddrContext(schemes ...damonfs.Scheme) damonfs.Context {
	return damonfs.Context{
		Operations: damonfs.OpsPaddr,
		Intervals:  damonfs.DefaultIntervals,
		NrRegions:  damonfs.DefaultNrRegions,
		Targets:    []damonfs.Target{{Regions: []damonfs.AddrRange{{Start: 0, End: 1 << 40}}}},
		Schemes:    schemes,
	}
}

func memcgScheme(memcg string, extra ...damonfs.Filter) damonfs.Scheme {
	s := damonfs.StatScheme(damonfs.MatchAll())
	s.Filters = append([]damonfs.Filter{damonfs.OnlyMemcg(memcg)}, extra...)
	return s
}

func setupKernel(t *testing.T, k *damonfstest.Kernel) *damonfs.Kdamond {
	t.Helper()
	f := damonfs.New(k)
	if err := f.Reserve(1); err != nil {
		t.Fatal(err)
	}
	return f.Kdamond(0)
}

func TestFiltersCoreOpsLayout(t *testing.T) {
	k := damonfstest.New()
	kd := setupKernel(t, k)
	addr := damonfs.Filter{Type: damonfs.FilterAddr, Matching: true, Allow: true, AddrStart: 0x1000, AddrEnd: 0x2000}
	if err := kd.Configure(paddrContext(memcgScheme("/a.slice", damonfs.OnlyYoung(), addr))); err != nil {
		t.Fatal(err)
	}
	s := "kdamonds/0/contexts/0/schemes/0/"
	for name, want := range map[string]string{
		s + "core_filters/nr_filters":   "1",
		s + "core_filters/0/type":       "addr",
		s + "core_filters/0/matching":   "Y",
		s + "core_filters/0/allow":      "Y",
		s + "core_filters/0/addr_start": "4096",
		s + "core_filters/0/addr_end":   "8192",
		s + "ops_filters/nr_filters":    "2",
		s + "ops_filters/0/type":        "memcg",
		s + "ops_filters/0/memcg_path":  "/a.slice",
		s + "ops_filters/0/matching":    "N",
		s + "ops_filters/0/allow":       "N",
		s + "ops_filters/1/type":        "young",
		s + "filters/nr_filters":        "0",
	} {
		mustFile(t, k, name, want)
	}
	ft, err := kd.Features()
	if err != nil {
		t.Fatal(err)
	}
	if ft.FilterLayout != damonfs.LayoutCoreOps || !ft.OpsFilterPassed {
		t.Fatalf("features = %+v", ft)
	}
}

func TestFiltersLegacyLayout(t *testing.T) {
	k := damonfstest.New().Without("core_filters", "ops_filters")
	kd := setupKernel(t, k)
	if err := kd.Configure(paddrContext(memcgScheme("/b.slice"))); err != nil {
		t.Fatal(err)
	}
	mustFile(t, k, "kdamonds/0/contexts/0/schemes/0/filters/nr_filters", "1")
	mustFile(t, k, "kdamonds/0/contexts/0/schemes/0/filters/0/memcg_path", "/b.slice")
	if ft, _ := kd.Features(); ft.FilterLayout != damonfs.LayoutLegacy {
		t.Fatalf("layout = %q", ft.FilterLayout)
	}
}

func TestFiltersWithoutAllowFile(t *testing.T) {
	k := damonfstest.New().Without("core_filters", "ops_filters", "allow")
	kd := setupKernel(t, k)
	if err := kd.Configure(paddrContext(memcgScheme("/c.slice"))); err != nil {
		t.Fatalf("reject-style filter on old kernel: %v", err)
	}
	s := damonfs.StatScheme(damonfs.MatchAll())
	s.Filters = []damonfs.Filter{{Type: damonfs.FilterAnon, Matching: true, Allow: true}}
	if err := kd.Configure(paddrContext(s)); !errors.Is(err, damonfs.ErrUnsupportedFeature) {
		t.Fatalf("allow filter on old kernel: got %v, want ErrUnsupportedFeature", err)
	}
}

func TestFiltersUnsupportedKernel(t *testing.T) {
	k := damonfstest.New().Without("core_filters", "ops_filters", "filters")
	kd := setupKernel(t, k)
	if err := kd.Configure(paddrContext(damonfs.StatScheme(damonfs.MatchAll()))); err != nil {
		t.Fatalf("scheme without filters: %v", err)
	}
	if err := kd.Configure(paddrContext(memcgScheme("/d.slice"))); !errors.Is(err, damonfs.ErrUnsupportedFeature) {
		t.Fatalf("got %v, want ErrUnsupportedFeature", err)
	}
}

func TestFilterValidate(t *testing.T) {
	for name, f := range map[string]damonfs.Filter{
		"memcg without path":  {Type: damonfs.FilterMemcg},
		"memcg relative path": {Type: damonfs.FilterMemcg, MemcgPath: "a.slice"},
		"unknown type":        {Type: "bogus"},
		"empty addr range":    {Type: damonfs.FilterAddr, AddrStart: 10, AddrEnd: 10},
		"hugepage min > max":  {Type: damonfs.FilterHugepageSize, Min: 5, Max: 1},
		"negative target idx": {Type: damonfs.FilterTarget, TargetIdx: -1},
	} {
		if err := f.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFilterPassedPerMemcg(t *testing.T) {
	k := damonfstest.New()
	kd := setupKernel(t, k)
	if err := kd.Configure(paddrContext(memcgScheme("/a"), memcgScheme("/b"))); err != nil {
		t.Fatal(err)
	}
	if err := kd.Start(); err != nil {
		t.Fatal(err)
	}
	k.SetRegions(0, []damonfs.Region{
		{Start: 0, End: 4 * mib, NrAccesses: 2},
		{Start: 4 * mib, End: 8 * mib},
	})
	k.SetMemory(0, []damonfstest.Chunk{
		{Start: 0, End: 3 * mib, Memcg: "/a"},
		{Start: 3 * mib, End: 6 * mib, Memcg: "/b"},
		{Start: 6 * mib, End: 8 * mib, Memcg: "/a"},
	})
	if err := kd.UpdateTriedRegions(); err != nil {
		t.Fatal(err)
	}
	for scheme, want := range map[int][2]uint64{0: {3 * mib, 2 * mib}, 1: {1 * mib, 2 * mib}} {
		regions, err := kd.ReadTriedRegions(scheme)
		if err != nil {
			t.Fatal(err)
		}
		if regions[0].SzFilterPassed != want[0] || regions[1].SzFilterPassed != want[1] {
			t.Errorf("scheme %d passed = %d,%d MiB, want %d,%d MiB", scheme,
				regions[0].SzFilterPassed/mib, regions[1].SzFilterPassed/mib, want[0]/mib, want[1]/mib)
		}
	}
	if err := kd.UpdateSchemesStats(); err != nil {
		t.Fatal(err)
	}
	st, err := kd.SchemeStats(0)
	if err != nil {
		t.Fatal(err)
	}
	if st.SzOpsFilterPassed != 5*mib {
		t.Errorf("sz_ops_filter_passed = %d MiB, want 5", st.SzOpsFilterPassed/mib)
	}
}

func TestUpdateSchemesCommitsWhenRunning(t *testing.T) {
	k := damonfstest.New()
	kd := setupKernel(t, k)
	if err := kd.Configure(paddrContext(memcgScheme("/a"))); err != nil {
		t.Fatal(err)
	}
	if err := kd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := kd.UpdateSchemes([]damonfs.Scheme{memcgScheme("/a"), memcgScheme("/b")}); err != nil {
		t.Fatal(err)
	}
	if n := k.Commits(0); n != 1 {
		t.Fatalf("commits = %d, want 1", n)
	}
	mustFile(t, k, "kdamonds/0/contexts/0/schemes/nr_schemes", "2")
	mustFile(t, k, "kdamonds/0/contexts/0/schemes/1/ops_filters/0/memcg_path", "/b")
	if err := kd.UpdateSchemes([]damonfs.Scheme{memcgScheme("relative")}); err == nil {
		t.Fatal("invalid scheme accepted")
	}
}
