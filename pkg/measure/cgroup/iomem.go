package cgroup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/sodiqscript111/sader/pkg/damonfs"
)

func ReadSystemRAM() ([]damonfs.AddrRange, error) {
	f, err := os.Open("/proc/iomem")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseSystemRAM(f)
}

func ParseSystemRAM(r io.Reader) ([]damonfs.AddrRange, error) {
	var out []damonfs.AddrRange
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, " ") {
			continue
		}
		span, name, ok := strings.Cut(line, " : ")
		if !ok || strings.TrimSpace(name) != "System RAM" {
			continue
		}
		lo, hi, ok := strings.Cut(strings.TrimSpace(span), "-")
		if !ok {
			continue
		}
		start, err1 := strconv.ParseUint(lo, 16, 64)
		end, err2 := strconv.ParseUint(hi, 16, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("cgroup: bad /proc/iomem line %q", line)
		}
		if start == 0 && end == 0 {
			return nil, errors.New("cgroup: /proc/iomem shows zero addresses; read it as root")
		}
		out = append(out, damonfs.AddrRange{Start: start, End: end + 1})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("cgroup: no System RAM ranges in /proc/iomem")
	}
	return out, nil
}
