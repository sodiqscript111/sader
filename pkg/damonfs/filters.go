package damonfs

import (
	"fmt"
	"path"
	"strconv"
)

type (
	FilterType string

	FilterLayout string

	Filter struct {
		Type      FilterType
		Matching  bool
		Allow     bool
		MemcgPath string
		AddrStart uint64
		AddrEnd   uint64
		Min       uint64
		Max       uint64
		TargetIdx int
	}
)

const (
	FilterAnon         FilterType = "anon"
	FilterActive       FilterType = "active"
	FilterMemcg        FilterType = "memcg"
	FilterYoung        FilterType = "young"
	FilterHugepageSize FilterType = "hugepage_size"
	FilterUnmapped     FilterType = "unmapped"
	FilterAddr         FilterType = "addr"
	FilterTarget       FilterType = "target"
)

const (
	LayoutNone    FilterLayout = ""
	LayoutCoreOps FilterLayout = "core_filters+ops_filters"
	LayoutLegacy  FilterLayout = "filters"
)

func (t FilterType) core() bool { return t == FilterAddr || t == FilterTarget }

func OnlyMemcg(memcgPath string) Filter {
	return Filter{Type: FilterMemcg, Matching: false, Allow: false, MemcgPath: memcgPath}
}

func OnlyYoung() Filter {
	return Filter{Type: FilterYoung, Matching: false, Allow: false}
}

func (f Filter) Validate() error {
	switch f.Type {
	case FilterAnon, FilterActive, FilterYoung, FilterUnmapped:
	case FilterMemcg:
		if f.MemcgPath == "" || f.MemcgPath[0] != '/' {
			return fmt.Errorf("damonfs: memcg filter needs a path starting with /, got %q", f.MemcgPath)
		}
	case FilterAddr:
		if f.AddrStart >= f.AddrEnd {
			return fmt.Errorf("damonfs: addr filter start %#x must be below end %#x", f.AddrStart, f.AddrEnd)
		}
	case FilterHugepageSize:
		if f.Min > f.Max {
			return fmt.Errorf("damonfs: hugepage_size filter min %d exceeds max %d", f.Min, f.Max)
		}
	case FilterTarget:
		if f.TargetIdx < 0 {
			return fmt.Errorf("damonfs: negative target index %d", f.TargetIdx)
		}
	default:
		return fmt.Errorf("damonfs: unknown filter type %q", f.Type)
	}
	return nil
}

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

func (k *Kdamond) filterLayout(schemeDir string) FilterLayout {
	b := k.fs.b
	switch {
	case b.Exists(schemeDir + "/core_filters"):
		return LayoutCoreOps
	case b.Exists(schemeDir + "/filters"):
		return LayoutLegacy
	}
	return LayoutNone
}

func (k *Kdamond) writeFilters(schemeDir string, fs []Filter) error {
	switch k.filterLayout(schemeDir) {
	case LayoutCoreOps:
		var core, ops []Filter
		for _, f := range fs {
			if f.Type.core() {
				core = append(core, f)
			} else {
				ops = append(ops, f)
			}
		}
		if err := k.writeFilterDir(schemeDir+"/core_filters", core); err != nil {
			return err
		}
		return k.writeFilterDir(schemeDir+"/ops_filters", ops)
	case LayoutLegacy:
		return k.writeFilterDir(schemeDir+"/filters", fs)
	}
	if len(fs) > 0 {
		return fmt.Errorf("%w: scheme filters", ErrUnsupportedFeature)
	}
	return nil
}

func (k *Kdamond) writeFilterDir(dir string, fs []Filter) error {
	if err := k.fs.writeUint(dir+"/nr_filters", uint64(len(fs))); err != nil {
		return err
	}
	for i, f := range fs {
		fd := path.Join(dir, strconv.Itoa(i))
		if err := k.fs.write(fd+"/type", string(f.Type)); err != nil {
			return err
		}
		if err := k.fs.write(fd+"/matching", yn(f.Matching)); err != nil {
			return err
		}
		if k.fs.b.Exists(fd + "/allow") {
			if err := k.fs.write(fd+"/allow", yn(f.Allow)); err != nil {
				return err
			}
		} else if f.Allow {
			return fmt.Errorf("%w: allow filters", ErrUnsupportedFeature)
		}
		if err := k.writeFilterArgs(fd, f); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kdamond) writeFilterArgs(fd string, f Filter) error {
	switch f.Type {
	case FilterMemcg:
		return k.fs.write(fd+"/memcg_path", f.MemcgPath)
	case FilterAddr:
		if err := k.fs.writeUint(fd+"/addr_start", f.AddrStart); err != nil {
			return err
		}
		return k.fs.writeUint(fd+"/addr_end", f.AddrEnd)
	case FilterHugepageSize:
		if err := k.fs.writeUint(fd+"/min", f.Min); err != nil {
			return err
		}
		return k.fs.writeUint(fd+"/max", f.Max)
	case FilterTarget:
		name := fd + "/damon_target_idx"
		if !k.fs.b.Exists(name) {
			name = fd + "/target_idx"
		}
		return k.fs.writeUint(name, uint64(f.TargetIdx))
	}
	return nil
}
