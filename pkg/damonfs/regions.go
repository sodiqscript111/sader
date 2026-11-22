package damonfs

import (
	"path"
	"sort"
	"strconv"
	"time"
)

type (
	Region struct {
		Start, End uint64
		NrAccesses uint64
		Age        uint64
	}
)

func (r Region) Size() uint64 {
	if r.End < r.Start {
		return 0
	}
	return r.End - r.Start
}

func (k *Kdamond) ReadTriedRegions(i int) ([]Region, error) {
	dir := k.schemeDir(i) + "/tried_regions"
	names, err := k.fs.b.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var idx []int
	for _, n := range names {
		if v, err := strconv.Atoi(n); err == nil {
			idx = append(idx, v)
		}
	}

	sort.Ints(idx)

	regions := make([]Region, 0, len(idx))
	for _, v := range idx {
		rd := path.Join(dir, strconv.Itoa(v)) + "/"
		var r Region
		for _, f := range []struct {
			name string
			dst  *uint64
		}{{"start", &r.Start}, {"end", &r.End}, {"nr_accesses", &r.NrAccesses}, {"age", &r.Age}} {
			val, err := k.fs.readUint(rd + f.name)
			if err != nil {
				return nil, err
			}
			*f.dst = val
		}
		regions = append(regions, r)
	}
	return regions, nil
}

func (k *Kdamond) SnapshotTriedRegions(i int) ([]Region, error) {
	if err := k.UpdateTriedRegions(); err != nil {
		return nil, err
	}
	return k.ReadTriedRegions(i)
}

func (k *Kdamond) TriedBytes(i int) (uint64, error) {
	return k.fs.readUint(k.schemeDir(i) + "/tried_regions/total_bytes")
}

func TotalBytes(regions []Region) uint64 {
	var sum uint64
	for _, r := range regions {
		sum += r.Size()
	}
	return sum
}

func HotBytes(regions []Region) uint64 {
	var sum uint64
	for _, r := range regions {
		if r.NrAccesses > 0 {
			sum += r.Size()
		}
	}
	return sum
}

func WorkingSetBytes(regions []Region, window, aggr time.Duration) uint64 {
	maxAge := uint64(window / aggr)
	var sum uint64
	for _, r := range regions {
		if r.NrAccesses > 0 || r.Age <= maxAge {
			sum += r.Size()
		}
	}
	return sum
}
