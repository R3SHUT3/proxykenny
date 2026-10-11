// Package metrics expose les métriques Prometheus du service.
package metrics

import (
	"github.com/davidbyttow/govips/v2/vips"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const ns = "proxykenny"

var (
	Registry = prometheus.NewRegistry()

	Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "requests_total", Help: "Requêtes par code HTTP.",
	}, []string{"code"})

	Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "duration_seconds", Help: "Durée par étape (download, queue, process, total).",
		Buckets: []float64{.002, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 20},
	}, []string{"stage"})

	Images = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "images_total", Help: "Images traitées par format source et de sortie.",
	}, []string{"src", "out"})

	SrcBytes = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: ns, Name: "source_bytes", Help: "Taille des fichiers source.",
		Buckets: prometheus.ExponentialBuckets(4<<10, 4, 9), // 4 Ko .. 256 Mo
	})
	OutBytes = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: ns, Name: "output_bytes", Help: "Taille des images produites.",
		Buckets: prometheus.ExponentialBuckets(1<<10, 4, 9), // 1 Ko .. 64 Mo
	})

	InFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "processing", Help: "Traitements en cours.",
	})
	Waiting = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns, Name: "queue_waiting", Help: "Requêtes en attente d'un worker.",
	})
	Rejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: ns, Name: "rejected_total", Help: "Requêtes refusées par motif.",
	}, []string{"reason"})
	Faces = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: ns, Name: "faces_detected_total", Help: "Visages détectés.",
	})
	FaceDetect = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: ns, Name: "face_detect_seconds", Help: "Durée d'une détection de visages, par moteur.",
		Buckets: []float64{.002, .005, .01, .02, .035, .05, .075, .1, .15, .25, .5, 1},
	}, []string{"engine"})
	FaceEngine = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns, Name: "face_engine_info", Help: "Moteur de détection de visages actif (1).",
	}, []string{"engine"})
)

func Init(version string, workers, maxQueue int) {
	vipsStat := func(f func(*vips.MemoryStats) int64) func() float64 {
		return func() float64 {
			var s vips.MemoryStats
			vips.ReadVipsMemStats(&s)
			return float64(f(&s))
		}
	}
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		Requests, Duration, Images, SrcBytes, OutBytes, InFlight, Waiting, Rejected, Faces, FaceDetect, FaceEngine,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: ns, Name: "vips_memory_bytes", Help: "Mémoire allouée par libvips."},
			vipsStat(func(s *vips.MemoryStats) int64 { return s.Mem })),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: ns, Name: "vips_memory_high_bytes", Help: "Pic de mémoire libvips."},
			vipsStat(func(s *vips.MemoryStats) int64 { return s.MemHigh })),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: ns, Name: "vips_allocs", Help: "Allocations libvips actives."},
			vipsStat(func(s *vips.MemoryStats) int64 { return s.Allocs })),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: ns, Name: "workers", Help: "Nombre de workers configurés."},
			func() float64 { return float64(workers) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: ns, Name: "queue_max", Help: "Taille max de la file."},
			func() float64 { return float64(maxQueue) }),
	)
	bi := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Name: "build_info", Help: "Version du service."}, []string{"version"})
	bi.WithLabelValues(version).Set(1)
	Registry.MustRegister(bi)
}
