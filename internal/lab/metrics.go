package lab

import "github.com/prometheus/client_golang/prometheus"

var (
	Registry = prometheus.NewRegistry()

	suitesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pklab", Name: "suites_total", Help: "Séries de transformations mesurées.",
	})
	uploadsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pklab", Name: "uploads_total", Help: "Images envoyées via le lab.",
	})
	benchRuns = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pklab", Name: "bench_runs_total", Help: "Bancs d'essai lancés.",
	})
	benchRPS = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pklab", Name: "bench_last_rps", Help: "Débit (req/s réussies) du dernier banc d'essai.",
	})
	benchP95 = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pklab", Name: "bench_last_p95_seconds", Help: "Latence p95 du dernier banc d'essai.",
	})
	campaignRuns = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "pklab", Name: "campaign_runs_total", Help: "Campagnes de tests terminées.",
	})
	campaignStepRPS = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "pklab", Name: "campaign_step_rps", Help: "Dernière campagne : débit (images/s) par étape.",
	}, []string{"group", "step"})
	campaignStepP50 = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "pklab", Name: "campaign_step_p50_seconds", Help: "Dernière campagne : latence médiane par étape.",
	}, []string{"group", "step"})
)

func init() {
	Registry.MustRegister(suitesTotal, uploadsTotal, benchRuns, benchRPS, benchP95, campaignRuns, campaignStepRPS, campaignStepP50)
}
