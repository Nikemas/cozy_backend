// Command loadtest is a tiny closed-loop HTTP load generator for
// docs/performance.md: for every target it runs -c workers hammering one
// URL for -d, then prints a Markdown row with p50/p95/p99 latency.
//
// Usage:
//
//	go run ./scripts/perf/loadtest -base http://localhost:8096 \
//	    -targets scripts/perf/targets.txt -cookie "staff_session=..." -c 20 -d 20s
//
// targets file: one "name<whitespace>path" per line, '#' comments allowed.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type target struct {
	name string
	path string
}

type result struct {
	latencies []time.Duration
	failures  int
	elapsed   time.Duration
}

func main() {
	base := flag.String("base", "http://localhost:8096", "server origin")
	targetsFile := flag.String("targets", "scripts/perf/targets.txt", "targets file")
	cookie := flag.String("cookie", "", "Cookie header sent with every request (admin pages)")
	concurrency := flag.Int("c", 20, "concurrent workers")
	duration := flag.Duration("d", 20*time.Second, "duration per target")
	only := flag.String("only", "", "run only the target with exactly this name")
	flag.Parse()

	targets, err := readTargets(*targetsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency * 2,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	fmt.Println("| endpoint | req | rps | p50 ms | p95 ms | p99 ms | non-2xx |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|")
	for _, t := range targets {
		if *only != "" && t.name != *only {
			continue
		}
		res := run(client, *base+t.path, *cookie, *concurrency, *duration)
		printRow(t.name, res)
	}
}

func readTargets(path string) ([]target, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []target
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("bad target line %q", line)
		}
		out = append(out, target{name: fields[0], path: fields[1]})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no targets")
	}
	return out, nil
}

func run(client *http.Client, url, cookie string, workers int, d time.Duration) result {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	var mu sync.Mutex
	var res result
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []time.Duration
			failures := 0
			for ctx.Err() == nil {
				lat, ok := once(client, url, cookie)
				if ctx.Err() != nil {
					break // don't count a request cut short by the deadline
				}
				local = append(local, lat)
				if !ok {
					failures++
				}
			}
			mu.Lock()
			res.latencies = append(res.latencies, local...)
			res.failures += failures
			mu.Unlock()
		}()
	}
	wg.Wait()
	res.elapsed = time.Since(start)
	return res
}

func once(client *http.Client, url, cookie string) (time.Duration, bool) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return time.Since(start), false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return time.Since(start), resp.StatusCode >= 200 && resp.StatusCode < 300
}

func printRow(name string, res result) {
	n := len(res.latencies)
	if n == 0 {
		fmt.Printf("| %s | 0 | 0 | - | - | - | %d |\n", name, res.failures)
		return
	}
	sort.Slice(res.latencies, func(i, j int) bool { return res.latencies[i] < res.latencies[j] })
	pct := func(p float64) float64 {
		idx := int(p * float64(n-1))
		return float64(res.latencies[idx].Microseconds()) / 1000
	}
	fmt.Printf("| %s | %d | %.0f | %.1f | %.1f | %.1f | %d |\n",
		name, n, float64(n)/res.elapsed.Seconds(), pct(0.50), pct(0.95), pct(0.99), res.failures)
}
