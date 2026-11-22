package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"damon-wss-exporter/pkg/damonfs"
)

func main() {
	root := flag.String("root", damonfs.DefaultRoot, "DAMON sysfs admin directory")
	pid := flag.Int("pid", 0, "monitor this PID and print its working set")
	interval := flag.Duration("interval", time.Second, "time between samples")
	count := flag.Int("count", 10, "number of samples; 0 runs until interrupted")
	flag.Parse()

	if err := run(*root, *pid, *interval, *count); err != nil {
		fmt.Fprintln(os.Stderr, "damon-probe:", err)
		if errors.Is(err, damonfs.ErrNotSupported) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(root string, pid int, interval time.Duration, count int) error {
	fsys, err := damonfs.Open(root)
	if err != nil {
		return err
	}
	n, err := fsys.NrKdamonds()
	if err != nil {
		return err
	}
	fmt.Printf("DAMON sysfs at %s, %d kdamond(s)\n", root, n)
	for i := range n {
		kd := fsys.Kdamond(i)
		st, _ := kd.State()
		p, _ := kd.PID()
		fmt.Printf("  kdamond %d: state=%s pid=%d\n", i, st, p)
		if st == damonfs.StateOn {
			if pid != 0 {
				return fmt.Errorf("kdamond %d is running (damo or another agent?); stop it first", i)
			}
		}
	}
	if pid == 0 {
		return nil
	}
	if n > 0 {
		fmt.Println("note: adding a slot recreates all kdamond dirs; existing (stopped) configs are discarded")
	}

	slot := n
	if err := fsys.Reserve(n + 1); err != nil {
		return err
	}

	defer fsys.SetNrKdamonds(n)

	kd := fsys.Kdamond(slot)
	aggr := damonfs.DefaultIntervals.Aggr
	cfg := damonfs.Context{
		Operations: damonfs.OpsVaddr,
		Intervals:  damonfs.DefaultIntervals,
		NrRegions:  damonfs.DefaultNrRegions,
		Targets:    []damonfs.Target{{PID: pid}},
		Schemes:    []damonfs.Scheme{damonfs.StatScheme(damonfs.MatchAll())},
	}
	if err := kd.Configure(cfg); err != nil {
		return err
	}
	if ft, err := kd.Features(); err == nil {
		fmt.Printf("features: ops=%v apply_interval=%t tried_bytes=%t interval_tuning=%t\n",
			ft.Operations, ft.SchemeApplyInterval, ft.TriedBytes, ft.IntervalsAutoTuning)
	}
	if err := kd.Start(); err != nil {
		return err
	}
	defer kd.Stop()
	kpid, _ := kd.PID()
	fmt.Printf("monitoring pid %d with kdamond %d (kernel thread %d)\n", pid, slot, kpid)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	tick := time.NewTicker(interval)
	defer tick.Stop()

	fmt.Printf("%-8s %7s %12s %12s %12s %12s\n", "time", "regions", "monitored", "hot(now)", "ws(1m)", "ws(5m)")
	for i := 0; count == 0 || i < count; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		regions, err := kd.SnapshotTriedRegions(0)
		if err != nil {
			if on, _ := kd.Running(); !on {
				return fmt.Errorf("kdamond stopped; did pid %d exit?", pid)
			}
			return err
		}
		fmt.Printf("%-8s %7d %12s %12s %12s %12s\n",
			time.Now().Format("15:04:05"), len(regions),
			human(damonfs.TotalBytes(regions)),
			human(damonfs.HotBytes(regions)),
			human(damonfs.WorkingSetBytes(regions, time.Minute, aggr)),
			human(damonfs.WorkingSetBytes(regions, 5*time.Minute, aggr)))
	}
	return nil
}

func human(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2fGiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%dKiB", b>>10)
	}
}
