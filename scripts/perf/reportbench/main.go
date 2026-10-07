// Command reportbench measures the admin yearly report under concurrency
// at the query layer: every "request" runs the query set of one
// /admin/reports page view (sales by day, product and point, brands,
// categories, point names) for a one-year range, the way
// admin.buildReportsData does, and reports per-request latency for
// reports.Repo (uncached) and reports.CachedRepo (cold burst, then warm).
//
// Read-only (SELECTs only). Point it at a perf database, never cozy_dev:
//
//	go run ./scripts/perf/reportbench -db "postgres://$(whoami)@localhost:5432/cozy_perf?sslmode=disable"
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Nikemas/cozy_backend/internal/reports"
)

func main() {
	dsn := flag.String("db", os.Getenv("DATABASE_URL"), "Postgres DSN (a perf database)")
	concurrency := flag.Int("c", 20, "concurrent requests per round")
	rounds := flag.Int("rounds", 3, "rounds per mode")
	flag.Parse()

	if *dsn == "" || strings.Contains(*dsn, "cozy_dev") {
		log.Fatal("reportbench: pass -db with a perf database (cozy_dev is refused)")
	}
	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(*concurrency)

	now := time.Now().In(reports.Location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, reports.Location)
	from, to := today.AddDate(-1, 0, 0), today.AddDate(0, 0, 1)

	repo := reports.NewRepo(db)
	fmt.Printf("range %s .. %s, c=%d, rounds=%d\n", from.Format("2006-01-02"), to.Format("2006-01-02"), *concurrency, *rounds)

	for r := range *rounds {
		report(fmt.Sprintf("uncached  round %d", r+1), burst(repo, from, to, *concurrency))
	}
	cached := reports.NewCachedRepo(repo, reports.CacheConfig{})
	report("cached    cold burst", burst(cached, from, to, *concurrency))
	for r := range *rounds {
		report(fmt.Sprintf("cached    warm %d", r+1), burst(cached, from, to, *concurrency))
	}
}

// burst runs n concurrent page-view query sets and returns their latencies.
func burst(src reports.Source, from, to time.Time, n int) []time.Duration {
	lat := make([]time.Duration, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			if err := pageView(context.Background(), src, from, to); err != nil {
				log.Fatal(err)
			}
			lat[i] = time.Since(start)
		}()
	}
	wg.Wait()
	slices.Sort(lat)
	return lat
}

// pageView mirrors admin.buildReportsData's queries.
func pageView(ctx context.Context, src reports.Source, from, to time.Time) error {
	for _, g := range []reports.GroupBy{reports.GroupByDay, reports.GroupByProduct} {
		if _, err := src.Sales(ctx, from, to, g, nil); err != nil {
			return err
		}
	}
	if _, err := src.BrandSales(ctx, from, to); err != nil {
		return err
	}
	if _, err := src.CategorySales(ctx, from, to); err != nil {
		return err
	}
	names, err := src.PointNames(ctx)
	if err != nil {
		return err
	}
	_, err = src.Sales(ctx, from, to, reports.GroupByPoint, names)
	return err
}

func report(label string, lat []time.Duration) {
	pct := func(p float64) time.Duration { return lat[int(p*float64(len(lat)-1))] }
	fmt.Printf("%-22s p50 %8.1f ms   p95 %8.1f ms   max %8.1f ms\n", label,
		ms(pct(0.5)), ms(pct(0.95)), ms(lat[len(lat)-1]))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
