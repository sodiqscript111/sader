package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sodiqscript111/sader/pkg/damonfs"
	"github.com/sodiqscript111/sader/pkg/measure/cgroup"
)

func runCgroups(fsys *damonfs.FS, n int, list string, pageLevel bool, interval time.Duration, count int) error {
	var cs []cgroup.Container
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			cs = append(cs, cgroup.Container{ID: p, MemcgPath: p})
		}
	}
	if err := fsys.Reserve(n + 1); err != nil {
		return err
	}
	defer fsys.SetNrKdamonds(n)

	cfg := cgroup.DefaultConfig()
	cfg.PageLevel = pageLevel
	regions, err := cgroup.ReadSystemRAM()
	if err != nil {
		return err
	}
	cfg.Regions = regions
	kd := fsys.Kdamond(n)
	m := cgroup.New(kd, cfg)
	if err := m.Start(cs); err != nil {
		return err
	}
	defer m.Stop()
	kpid, _ := kd.PID()
	fmt.Printf("Way 2: %d cgroup(s), one paddr watcher (kernel thread %d), page-level=%t\n", len(cs), kpid, pageLevel)
	cpu0, t0 := cpuTicks(kpid), time.Now()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	tick := time.NewTicker(interval)
	defer tick.Stop()

	fmt.Printf("%-8s %-24s %12s %12s %12s\n", "time", "cgroup", "seen", "hot(now)", "ws(1m)")
	for i := 0; count == 0 || i < count; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		usages, err := m.Sample()
		if err != nil {
			return err
		}
		for _, u := range usages {
			fmt.Printf("%-8s %-24s %12s %12s %12s\n", time.Now().Format("15:04:05"), u.ID,
				human(u.Bytes), human(u.HotBytes), human(u.Windows[time.Minute]))
		}
	}
	if secs := time.Since(t0).Seconds(); secs > 0 {
		fmt.Printf("kdamond CPU: %.1f%% of one core over %.0fs\n", float64(cpuTicks(kpid)-cpu0)/100/secs*100, secs)
	}
	return nil
}

func cpuTicks(pid int) uint64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(fields) < 13 {
		return 0
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	return utime + stime
}
