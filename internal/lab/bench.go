package lab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// BenchResult est le résultat d'un banc d'essai (test de charge).
type BenchResult struct {
	When        time.Time
	Image       string
	Preset      Preset
	Concurrency int
	Duration    time.Duration
	Total, OK   int
	Codes       map[int]int
	RPS         float64 // requêtes réussies par seconde
	P50, P95    float64 // ms
	P99, Max    float64 // ms
	MBps        float64 // débit sortant
	Bytes       int64   // octets reçus (réponses réussies)
	Err         string
}

// SuccessRate : part des requêtes réussies (0..1).
func (r *BenchResult) SuccessRate() float64 {
	if r == nil || r.Total == 0 {
		return 0
	}
	return float64(r.OK) / float64(r.Total)
}

var ErrBenchBusy = errors.New("un banc d'essai est déjà en cours, réessaie dans quelques secondes")

const (
	MaxBenchConcurrency = 64
	MaxBenchDuration    = 30 * time.Second
)

// Bench envoie des requêtes en boucle pendant dur avec conc clients simultanés.
// Un seul banc d'essai à la fois ; concurrence et durée sont bornées.
func (l *Lab) Bench(ctx context.Context, img Image, p Preset, conc int, dur time.Duration) (*BenchResult, error) {
	if !l.benchMu.TryLock() {
		return nil, ErrBenchBusy
	}
	defer l.benchMu.Unlock()
	return l.runBench(ctx, img, p, conc, dur), nil
}

// runBench exécute le test (l'appelant détient benchMu).
func (l *Lab) runBench(ctx context.Context, img Image, p Preset, conc int, dur time.Duration) *BenchResult {
	conc = max(1, min(conc, MaxBenchConcurrency))
	dur = max(10*time.Millisecond, min(dur, MaxBenchDuration)) // l'interface propose 5 à 30 s

	url := l.cfg.ProxyInternal + l.Path(p.Opts, img.Rel)
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: conc, MaxConnsPerHost: conc, DisableCompression: true},
	}
	ctx, cancel := context.WithTimeout(ctx, dur)
	defer cancel()

	type workerStats struct {
		lat   []float64
		codes map[int]int
		bytes int64
		total int
	}
	stats := make([]workerStats, conc)
	start := time.Now()
	var wg sync.WaitGroup
	for i := range conc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &stats[i]
			s.codes = map[int]int{}
			for ctx.Err() == nil {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				t0 := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					if ctx.Err() == nil {
						s.codes[0]++
						s.total++
					}
					continue
				}
				n, _ := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if ctx.Err() != nil {
					break // requête coupée par la fin du test : non comptée
				}
				s.total++
				s.codes[resp.StatusCode]++
				if resp.StatusCode == http.StatusOK {
					s.lat = append(s.lat, float64(time.Since(t0).Microseconds())/1000)
					s.bytes += n
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	client.CloseIdleConnections()

	r := &BenchResult{When: time.Now(), Image: img.Rel, Preset: p, Concurrency: conc, Duration: dur, Codes: map[int]int{}}
	var lat []float64
	var bytes int64
	for _, s := range stats {
		lat = append(lat, s.lat...)
		bytes += s.bytes
		r.Total += s.total
		for c, n := range s.codes {
			r.Codes[c] += n
		}
	}
	sort.Float64s(lat)
	r.OK = len(lat)
	r.Bytes = bytes
	if elapsed > 0 {
		r.RPS = float64(r.OK) / elapsed
		r.MBps = float64(bytes) / elapsed / 1e6
	}
	if len(lat) > 0 {
		pct := func(p float64) float64 { return lat[min(len(lat)-1, int(p*float64(len(lat))))] }
		r.P50, r.P95, r.P99, r.Max = pct(0.50), pct(0.95), pct(0.99), lat[len(lat)-1]
	}

	l.histMu.Lock()
	l.history = append([]*BenchResult{r}, l.history...)
	if len(l.history) > 30 {
		l.history = l.history[:30]
	}
	l.histMu.Unlock()
	benchRuns.Inc()
	benchRPS.Set(r.RPS)
	benchP95.Set(r.P95 / 1000)
	return r
}

func (l *Lab) History() []*BenchResult {
	l.histMu.Lock()
	defer l.histMu.Unlock()
	return append([]*BenchResult(nil), l.history...)
}
