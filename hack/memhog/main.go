package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

const page = 4096

func main() {
	total := flag.Int("total", 1024, "MiB to allocate and fill")
	hot := flag.Int("hot", 200, "MiB to keep touching")
	sweep := flag.Duration("sweep", 0, "if set, touch all memory once per this interval")
	addr := flag.String("addr", ":8080", "HTTP address for /hot?mib=N; empty disables")
	flag.Parse()

	if *hot > *total {
		log.Fatalf("hot %d MiB exceeds total %d MiB", *hot, *total)
	}
	buf := make([]byte, *total<<20)
	touch(buf)
	var hotBytes atomic.Int64
	hotBytes.Store(int64(*hot) << 20)
	log.Printf("pid %d: filled %d MiB, touching %d MiB", os.Getpid(), *total, *hot)

	if *addr != "" {
		http.HandleFunc("/hot", func(w http.ResponseWriter, r *http.Request) {
			mib, err := strconv.Atoi(r.URL.Query().Get("mib"))
			if err != nil || mib < 0 || mib > *total {
				http.Error(w, fmt.Sprintf("mib must be 0..%d", *total), http.StatusBadRequest)
				return
			}
			hotBytes.Store(int64(mib) << 20)
			log.Printf("now touching %d MiB", mib)
			fmt.Fprintf(w, "touching %d MiB\n", mib)
		})
		go func() { log.Println(http.ListenAndServe(*addr, nil)) }()
	}

	lastSweep := time.Now()
	for {
		touch(buf[:hotBytes.Load()])
		if *sweep > 0 && time.Since(lastSweep) >= *sweep {
			touch(buf)
			lastSweep = time.Now()
			log.Printf("swept all %d MiB", *total)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func touch(b []byte) {
	for i := 0; i < len(b); i += page {
		b[i]++
	}
}
