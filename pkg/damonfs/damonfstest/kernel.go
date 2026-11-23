package damonfstest

import (
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/sodiqscript111/sader/pkg/damonfs"
)

type (
	Kernel struct {
		mu      sync.Mutex
		files   map[string]string
		dirs    map[string]bool
		kids    map[string]map[string]bool
		regions map[int][]damonfs.Region
		memory  map[int][]Chunk
		commits map[int]int
		reads   int
		omit    []string
		nextPID int
	}

	kv struct{ name, val string }

	Chunk struct {
		Start, End uint64
		Memcg      string
		Young      bool
	}

	filterSpec struct {
		typ      string
		matching bool
		allow    bool
		hasAllow bool
		memcg    string
		start    uint64
		end      uint64
	}
)

var _ damonfs.Backend = (*Kernel)(nil)

func New() *Kernel {
	k := &Kernel{
		files:   map[string]string{},
		dirs:    map[string]bool{".": true},
		kids:    map[string]map[string]bool{},
		regions: map[int][]damonfs.Region{},
		memory:  map[int][]Chunk{},
		commits: map[int]int{},
		nextPID: 4000,
	}
	k.addFile("kdamonds/nr_kdamonds", "0")
	return k
}

func (k *Kernel) Without(names ...string) *Kernel {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.omit = append(k.omit, names...)
	return k
}

func (k *Kernel) omitted(rel string) bool {
	for _, o := range k.omit {
		if rel == o || strings.HasPrefix(rel, o+"/") {
			return true
		}
	}
	return false
}

func (k *Kernel) SetRegions(i int, regions []damonfs.Region) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.regions[i] = slices.Clone(regions)
}

func (k *Kernel) SetMemory(i int, chunks []Chunk) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.memory[i] = slices.Clone(chunks)
}

func (k *Kernel) File(name string) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.files[name]
	return v, ok
}

func (k *Kernel) RemoveFile(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.removePath(name)
}

func (k *Kernel) Commits(i int) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.commits[i]
}

func (k *Kernel) Reads() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.reads
}

func (k *Kernel) ReadFile(name string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.reads++
	v, ok := k.files[name]
	if !ok {
		return nil, pathErr("open", name, fs.ErrNotExist)
	}
	return []byte(v + "\n"), nil
}

func (k *Kernel) ReadDir(name string) ([]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.dirs[name] {
		return nil, pathErr("open", name, fs.ErrNotExist)
	}
	out := make([]string, 0, len(k.kids[name]))
	for n := range k.kids[name] {
		out = append(out, n)
	}
	slices.Sort(out)
	return out, nil
}

func (k *Kernel) Exists(name string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.files[name]
	return ok || k.dirs[name]
}

func (k *Kernel) WriteFile(name string, data []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.files[name]; !ok {
		return pathErr("open", name, fs.ErrNotExist)
	}
	if readOnly(name) {
		return pathErr("open", name, syscall.EACCES)
	}
	val := strings.TrimSpace(string(data))
	base := path.Base(name)

	if numeric(base) {
		if _, err := strconv.ParseUint(val, 10, 64); err != nil {
			return pathErr("write", name, syscall.EINVAL)
		}
	}
	switch {
	case base == "state":
		return k.command(name, val)
	case strings.HasPrefix(base, "nr_"):
		return k.resize(name, val)
	case base == "operations":
		avail := strings.Fields(k.files[path.Dir(name)+"/avail_operations"])
		if !slices.Contains(avail, val) {
			return pathErr("write", name, syscall.EINVAL)
		}
	case base == "action":
		if !slices.Contains(actions, val) {
			return pathErr("write", name, syscall.EINVAL)
		}
	case base == "type":
		if !slices.Contains(filterTypes, val) {
			return pathErr("write", name, syscall.EINVAL)
		}
	case base == "matching" || base == "allow":
		if val != "Y" && val != "N" {
			return pathErr("write", name, syscall.EINVAL)
		}
	}
	k.files[name] = val
	return nil
}

var filterTypes = []string{"anon", "active", "memcg", "young", "hugepage_size", "unmapped", "addr", "target"}

var actions = []string{"willneed", "cold", "pageout", "hugepage", "nohugepage", "lru_prio", "lru_deprio", "migrate_hot", "migrate_cold", "stat"}

func numeric(base string) bool {
	switch base {
	case "min", "max", "sample_us", "aggr_us", "update_us", "pid_target", "start", "end",
		"apply_interval_us", "ms", "bytes", "reset_interval_ms",
		"addr_start", "addr_end", "damon_target_idx", "target_idx":
		return true
	}
	return strings.HasPrefix(base, "nr_")
}

func readOnly(name string) bool {
	base := path.Base(name)
	return base == "pid" || base == "avail_operations" ||
		strings.Contains(name, "/stats/") || strings.Contains(name, "/tried_regions/")
}

var templates = map[string][]kv{
	"nr_kdamonds": {{"state", "off"}, {"pid", "-1"}, {"contexts/nr_contexts", "0"}},
	"nr_contexts": {
		{"avail_operations", "fvaddr paddr vaddr"},
		{"operations", "vaddr"},
		{"monitoring_attrs/intervals/sample_us", "5000"},
		{"monitoring_attrs/intervals/aggr_us", "100000"},
		{"monitoring_attrs/intervals/update_us", "1000000"},
		{"monitoring_attrs/intervals/intervals_goal/access_bp", "0"},
		{"monitoring_attrs/nr_regions/min", "10"},
		{"monitoring_attrs/nr_regions/max", "1000"},
		{"targets/nr_targets", "0"},
		{"schemes/nr_schemes", "0"},
	},
	"nr_targets": {{"pid_target", "0"}, {"regions/nr_regions", "0"}},
	"nr_regions": {{"start", "0"}, {"end", "0"}},
	"nr_schemes": {
		{"action", "stat"},
		{"apply_interval_us", "0"},
		{"access_pattern/sz/min", "0"}, {"access_pattern/sz/max", "0"},
		{"access_pattern/nr_accesses/min", "0"}, {"access_pattern/nr_accesses/max", "0"},
		{"access_pattern/age/min", "0"}, {"access_pattern/age/max", "0"},
		{"quotas/ms", "0"}, {"quotas/bytes", "0"}, {"quotas/reset_interval_ms", "0"},
		{"watermarks/metric", "none"},
		{"core_filters/nr_filters", "0"},
		{"ops_filters/nr_filters", "0"},
		{"filters/nr_filters", "0"},
		{"stats/nr_tried", "0"}, {"stats/sz_tried", "0"},
		{"stats/nr_applied", "0"}, {"stats/sz_applied", "0"},
		{"stats/qt_exceeds", "0"},
		{"stats/sz_ops_filter_passed", "0"},
		{"stats/nr_snapshots", "0"},
		{"tried_regions/total_bytes", "0"},
	},
	"nr_filters": {
		{"type", "anon"}, {"matching", "N"}, {"allow", "N"},
		{"memcg_path", ""}, {"addr_start", "0"}, {"addr_end", "0"},
		{"min", "0"}, {"max", "0"}, {"damon_target_idx", "0"},
	},
}

func (k *Kernel) resize(name, val string) error {
	n, _ := strconv.Atoi(val)
	base, parent := path.Base(name), path.Dir(name)
	switch base {
	case "nr_kdamonds":
		for i := range k.count("kdamonds/nr_kdamonds") {
			if k.files["kdamonds/"+strconv.Itoa(i)+"/state"] == "on" {
				return pathErr("write", name, syscall.EBUSY)
			}
		}
	case "nr_contexts":
		if n > 1 {
			return pathErr("write", name, syscall.EINVAL)
		}
	}
	k.removeChildren(parent)
	k.files[name] = val
	for i := range n {
		dir := path.Join(parent, strconv.Itoa(i))
		k.addDir(dir)
		for _, f := range templates[base] {
			if k.omitted(f.name) {
				continue
			}
			k.addFile(path.Join(dir, f.name), f.val)
		}
	}
	return nil
}

func (k *Kernel) command(name, cmd string) error {
	kdir := path.Dir(name)
	idx, _ := strconv.Atoi(path.Base(kdir))
	running := k.files[name] == "on"
	switch cmd {
	case "on":
		if running {
			return pathErr("write", name, syscall.EBUSY)
		}
		if !k.startable(kdir) {
			return pathErr("write", name, syscall.EINVAL)
		}
		k.files[name] = "on"
		k.files[kdir+"/pid"] = strconv.Itoa(k.nextPID)
		k.nextPID++
		return nil
	case "off", "commit", "update_schemes_stats", "update_schemes_tried_regions",
		"update_schemes_tried_bytes", "clear_schemes_tried_regions":
		if !running {
			return pathErr("write", name, syscall.EINVAL)
		}
	default:
		return pathErr("write", name, syscall.EINVAL)
	}

	ctx := kdir + "/contexts/0"
	switch cmd {
	case "off":
		k.files[name] = "off"
		k.files[kdir+"/pid"] = "-1"
	case "commit":
		k.commits[idx]++
	default:
		for s := range k.count(ctx + "/schemes/nr_schemes") {
			k.applyScheme(idx, path.Join(ctx, "schemes", strconv.Itoa(s)), cmd)
		}
	}
	return nil
}

func (k *Kernel) startable(kdir string) bool {
	if k.files[kdir+"/contexts/nr_contexts"] != "1" {
		return false
	}
	ctx := kdir + "/contexts/0"
	nt := k.count(ctx + "/targets/nr_targets")
	if nt == 0 {
		return false
	}
	if ops := k.files[ctx+"/operations"]; ops == "vaddr" || ops == "fvaddr" {
		for i := range nt {
			if k.files[ctx+"/targets/"+strconv.Itoa(i)+"/pid_target"] == "0" {
				return false
			}
		}
	}
	return true
}

func (k *Kernel) applyScheme(idx int, sdir, cmd string) {
	var matched []damonfs.Region
	ctx := path.Dir(path.Dir(sdir))
	if k.files[ctx+"/operations"] == "paddr" && k.count(ctx+"/targets/0/regions/nr_regions") == 0 {
		k.regions[idx] = nil
	}
	for _, r := range k.regions[idx] {
		if k.inRange(sdir, "sz", r.Size()) && k.inRange(sdir, "nr_accesses", r.NrAccesses) && k.inRange(sdir, "age", r.Age) {
			matched = append(matched, r)
		}
	}
	filters := k.filters(sdir)
	passed := make([]uint64, len(matched))
	var total, totalPassed uint64
	for i, r := range matched {
		total += r.Size()
		passed[i] = k.passedBytes(idx, r, filters)
		totalPassed += passed[i]
	}
	td := sdir + "/tried_regions"
	switch cmd {
	case "update_schemes_tried_regions":
		k.removeChildren(td)
		for i, r := range matched {
			rd := path.Join(td, strconv.Itoa(i))
			k.addDir(rd)
			k.addFile(rd+"/start", u(r.Start))
			k.addFile(rd+"/end", u(r.End))
			k.addFile(rd+"/nr_accesses", u(r.NrAccesses))
			k.addFile(rd+"/age", u(r.Age))
			if !k.omitted("sz_filter_passed") {
				k.addFile(rd+"/sz_filter_passed", u(passed[i]))
			}
		}
		k.files[td+"/total_bytes"] = u(total)
	case "update_schemes_tried_bytes":
		if _, ok := k.files[td+"/total_bytes"]; ok {
			k.files[td+"/total_bytes"] = u(total)
		}
	case "clear_schemes_tried_regions":
		k.removeChildren(td)
		k.files[td+"/total_bytes"] = "0"
	case "update_schemes_stats":
		if _, ok := k.files[sdir+"/stats/sz_ops_filter_passed"]; ok {
			k.files[sdir+"/stats/sz_ops_filter_passed"] = u(k.uint(sdir+"/stats/sz_ops_filter_passed") + totalPassed)
		}
		k.files[sdir+"/stats/nr_tried"] = u(k.uint(sdir+"/stats/nr_tried") + uint64(len(matched)))
		k.files[sdir+"/stats/sz_tried"] = u(k.uint(sdir+"/stats/sz_tried") + total)
	}
}

func (k *Kernel) filters(sdir string) []filterSpec {
	dirs := []string{sdir + "/filters"}
	if k.dirs[sdir+"/core_filters"] {
		dirs = []string{sdir + "/core_filters", sdir + "/ops_filters"}
	}
	var out []filterSpec
	for _, d := range dirs {
		for i := range k.count(d + "/nr_filters") {
			fd := path.Join(d, strconv.Itoa(i))
			_, hasAllow := k.files[fd+"/allow"]
			out = append(out, filterSpec{
				typ:      k.files[fd+"/type"],
				matching: k.files[fd+"/matching"] == "Y",
				allow:    k.files[fd+"/allow"] == "Y",
				hasAllow: hasAllow,
				memcg:    k.files[fd+"/memcg_path"],
				start:    k.uint(fd + "/addr_start"),
				end:      k.uint(fd + "/addr_end"),
			})
		}
	}
	return out
}

func (k *Kernel) passedBytes(idx int, r damonfs.Region, filters []filterSpec) uint64 {
	if len(filters) == 0 {
		return r.Size()
	}
	var sum uint64
	covered := uint64(0)
	for _, c := range k.memory[idx] {
		lo, hi := max(c.Start, r.Start), min(c.End, r.End)
		if lo >= hi {
			continue
		}
		covered += hi - lo
		if allowed(filters, c, lo) {
			sum += hi - lo
		}
	}
	if rest := r.Size() - covered; rest > 0 && allowed(filters, Chunk{}, r.Start) {
		sum += rest
	}
	return sum
}

func allowed(filters []filterSpec, c Chunk, addr uint64) bool {
	for _, f := range filters {
		var is bool
		switch f.typ {
		case "memcg":
			is = c.Memcg == f.memcg
		case "young":
			is = c.Young
		case "addr":
			is = addr >= f.start && addr < f.end
		}
		if is != f.matching {
			continue
		}
		if f.hasAllow {
			return f.allow
		}
		return false
	}
	last := filters[len(filters)-1]
	if last.hasAllow {
		return !last.allow
	}
	return true
}

func (k *Kernel) inRange(sdir, field string, v uint64) bool {
	base := path.Join(sdir, "access_pattern", field)
	return v >= k.uint(base+"/min") && v <= k.uint(base+"/max")
}

func (k *Kernel) uint(name string) uint64 {
	v, _ := strconv.ParseUint(k.files[name], 10, 64)
	return v
}

func (k *Kernel) count(name string) int { return int(k.uint(name)) }

func (k *Kernel) removeChildren(dir string) {
	for c := range k.kids[dir] {
		if _, err := strconv.Atoi(c); err == nil {
			k.removePath(dir + "/" + c)
		}
	}
}

func (k *Kernel) removePath(p string) {
	for c := range k.kids[p] {
		k.removePath(p + "/" + c)
	}
	delete(k.kids, p)
	delete(k.files, p)
	delete(k.dirs, p)
	delete(k.kids[path.Dir(p)], path.Base(p))
}

func (k *Kernel) addDir(d string) {
	k.dirs[d] = true
	k.link(d)
}

func (k *Kernel) addFile(name, val string) {
	k.files[name] = val
	k.link(name)
}

func (k *Kernel) link(p string) {
	for p != "." {
		parent, base := path.Dir(p), path.Base(p)
		if k.kids[parent] == nil {
			k.kids[parent] = map[string]bool{}
		}
		if k.kids[parent][base] {
			return
		}
		k.kids[parent][base] = true
		k.dirs[parent] = true
		p = parent
	}
}

func u(v uint64) string { return strconv.FormatUint(v, 10) }

func pathErr(op, name string, err error) error { return &fs.PathError{Op: op, Path: name, Err: err} }
