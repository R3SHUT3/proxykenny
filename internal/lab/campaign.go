package lab

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Campagne : une série de bancs d'essai qui fait varier un paramètre à la fois
// (taille, format, opération, nombre de clients) pour comparer les résultats.

type CampaignStep struct {
	Group string // ex. « Taille de sortie »
	Label string // ex. « 1600 px »
	Opts  string
	Conc  int
}

type Campaign struct {
	ID       string
	Image    string
	Started  time.Time
	Ended    time.Time
	StepDur  time.Duration
	Steps    []CampaignStep
	Results  []*BenchResult // même ordre que Steps ; nil tant que l'étape n'est pas faite
	Current  int
	Running  bool
	Err      string
	mu       sync.Mutex
	finished chan struct{}
}

// Groupes de la campagne, dans l'ordre d'affichage.
const (
	GroupSize   = "Taille de sortie"
	GroupFormat = "Format de sortie"
	GroupOp     = "Opération"
	GroupLoad   = "Clients simultanés"
)

// DefaultCampaign : chaque groupe fait varier un seul paramètre, les autres restent fixes
// (800 px, WebP, redimensionnement simple, 4 clients).
func DefaultCampaign() []CampaignStep {
	var s []CampaignStep
	for _, w := range []int{320, 800, 1600, 2400} {
		s = append(s, CampaignStep{GroupSize, fmt.Sprintf("%d px", w), fmt.Sprintf("w:%d/f:webp", w), 4})
	}
	for _, f := range []string{"jpeg", "webp", "avif", "jxl"} {
		s = append(s, CampaignStep{GroupFormat, f, "w:800/f:" + f, 4})
	}
	for _, op := range []struct{ label, opts string }{
		{"redimensionner", "w:800/f:webp"},
		{"recadrage smart", "rs:fill:600:400/g:sm/f:webp"},
		{"centrage visage", "rs:fill:600:400/g:face/f:webp"},
		{"avatar (zoom visage)", "rs:fill:256:256/fc:0.4:largest/f:webp"},
		{"flouter les visages", "w:800/bf:12/f:webp"},
	} {
		s = append(s, CampaignStep{GroupOp, op.label, op.opts, 4})
	}
	for _, c := range []int{1, 2, 4, 8, 16, 32} {
		s = append(s, CampaignStep{GroupLoad, strconv.Itoa(c) + " client" + map[bool]string{true: "s", false: ""}[c > 1], "w:800/f:webp", c})
	}
	return s
}

// Bornes d'une étape (réduites par les tests).
var (
	minStepDur = 2 * time.Second
	stepPause  = 500 * time.Millisecond // laisse retomber la charge entre deux étapes
)

// StartCampaign lance la campagne en arrière-plan. Un seul test (banc ou campagne) à la fois.
func (l *Lab) StartCampaign(img Image, stepDur time.Duration) (*Campaign, error) {
	if !l.benchMu.TryLock() {
		return nil, ErrBenchBusy
	}
	stepDur = max(minStepDur, min(stepDur, 10*time.Second))
	c := &Campaign{
		ID: strconv.FormatInt(time.Now().UnixNano(), 36), Image: img.Rel, Started: time.Now(),
		StepDur: stepDur, Steps: DefaultCampaign(), Running: true, finished: make(chan struct{}),
	}
	c.Results = make([]*BenchResult, len(c.Steps))
	l.campMu.Lock()
	l.campaigns = append([]*Campaign{c}, l.campaigns...)
	if len(l.campaigns) > 10 {
		l.campaigns = l.campaigns[:10]
	}
	l.campMu.Unlock()

	go func() {
		defer l.benchMu.Unlock()
		defer close(c.finished)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(c.Steps)+2)*(stepDur+5*time.Second))
		defer cancel()
		for i, st := range c.Steps {
			c.mu.Lock()
			c.Current = i
			c.mu.Unlock()
			r := l.runBench(ctx, img, Preset{ID: "campagne", Name: st.Group + " : " + st.Label, Opts: st.Opts}, st.Conc, stepDur)
			c.mu.Lock()
			c.Results[i] = r
			c.mu.Unlock()
			campaignStepRPS.WithLabelValues(st.Group, st.Label).Set(r.RPS)
			campaignStepP50.WithLabelValues(st.Group, st.Label).Set(r.P50 / 1000)
			if ctx.Err() != nil {
				c.mu.Lock()
				c.Err = "campagne interrompue (délai dépassé)"
				c.mu.Unlock()
				break
			}
			time.Sleep(stepPause)
		}
		c.mu.Lock()
		c.Running, c.Ended = false, time.Now()
		c.mu.Unlock()
		campaignRuns.Inc()
	}()
	return c, nil
}

func (l *Lab) Campaign(id string) *Campaign {
	l.campMu.Lock()
	defer l.campMu.Unlock()
	for _, c := range l.campaigns {
		if c.ID == id || id == "" {
			return c
		}
	}
	return nil
}

// CampaignView est une copie cohérente de la campagne pour l'affichage (sans verrou à tenir).
type CampaignView struct {
	*Campaign
	Running     bool // copies prises sous verrou (la campagne tourne en parallèle)
	Err         string
	Ended       time.Time
	Done, Total int
	Percent     int
	ETA         time.Duration
	Groups      []GroupView
	Summary     []string
}

type GroupView struct {
	Name string
	Rows []RowView
}

type RowView struct {
	Step      CampaignStep
	R         *BenchResult
	BarRPS    int // largeur relative de la barre (%) dans le groupe
	BarP50    int
	AvgBytes  int64
	IsBest    bool
	IsCurrent bool
}

func (c *Campaign) View() CampaignView {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := CampaignView{Campaign: c, Total: len(c.Steps), Running: c.Running, Err: c.Err, Ended: c.Ended}
	for _, r := range c.Results {
		if r != nil {
			v.Done++
		}
	}
	v.Percent = v.Done * 100 / max(1, v.Total)
	v.ETA = time.Duration(v.Total-v.Done) * (c.StepDur + stepPause + 100*time.Millisecond)

	idx := map[string]int{}
	for i, st := range c.Steps {
		gi, ok := idx[st.Group]
		if !ok {
			gi = len(v.Groups)
			idx[st.Group] = gi
			v.Groups = append(v.Groups, GroupView{Name: st.Group})
		}
		row := RowView{Step: st, R: c.Results[i], IsCurrent: c.Running && i == c.Current}
		if row.R != nil && row.R.OK > 0 {
			row.AvgBytes = row.R.Bytes / int64(row.R.OK)
		}
		v.Groups[gi].Rows = append(v.Groups[gi].Rows, row)
	}
	for gi := range v.Groups {
		rows := v.Groups[gi].Rows
		var maxRPS, maxP50 float64
		for _, r := range rows {
			if r.R != nil {
				maxRPS, maxP50 = max(maxRPS, r.R.RPS), max(maxP50, r.R.P50)
			}
		}
		for i, r := range rows {
			if r.R == nil {
				continue
			}
			if maxRPS > 0 {
				rows[i].BarRPS = int(r.R.RPS / maxRPS * 100)
				rows[i].IsBest = r.R.RPS == maxRPS
			}
			if maxP50 > 0 {
				rows[i].BarP50 = int(r.R.P50 / maxP50 * 100)
			}
		}
	}
	v.Summary = summarize(v.Groups)
	return v
}

// summarize tire des conclusions lisibles des résultats.
func summarize(groups []GroupView) []string {
	var out []string
	for _, g := range groups {
		var best, worst *RowView
		for i := range g.Rows {
			r := &g.Rows[i]
			if r.R == nil || r.R.OK == 0 {
				continue
			}
			if best == nil || r.R.RPS > best.R.RPS {
				best = r
			}
			if worst == nil || r.R.RPS < worst.R.RPS {
				worst = r
			}
		}
		if best == nil {
			continue
		}
		if g.Name == GroupSize {
			// Deux tailles demandées → même poids produit : la source est plus petite et n'est pas agrandie.
			for i := 1; i < len(g.Rows); i++ {
				a, b := g.Rows[i-1], g.Rows[i]
				if a.R != nil && b.R != nil && a.AvgBytes > 0 && a.AvgBytes == b.AvgBytes {
					out = append(out, fmt.Sprintf("« %s » et « %s » donnent la même image : la source est plus petite et proxykenny ne l'agrandit pas (option el:1 pour l'autoriser).",
						a.Step.Label, b.Step.Label))
					break
				}
			}
		}
		switch g.Name {
		case GroupLoad:
			// Saturation : plus petit nombre de clients qui atteint 95 % du débit maximal.
			for _, r := range g.Rows {
				if r.R != nil && r.R.RPS >= 0.95*best.R.RPS {
					out = append(out, fmt.Sprintf("Débit maximal ≈ %.0f images/s, atteint dès %d clients simultanés : au-delà, les requêtes attendent leur tour (latence p50 %.0f ms à %d clients).",
						best.R.RPS, r.Step.Conc, g.Rows[len(g.Rows)-1].latency(), g.Rows[len(g.Rows)-1].Step.Conc))
					break
				}
			}
		default:
			if worst != best {
				out = append(out, fmt.Sprintf("%s : le plus rapide est « %s » (%.0f img/s), le plus lent « %s » (%.0f img/s, %.1f× moins).",
					g.Name, best.Step.Label, best.R.RPS, worst.Step.Label, worst.R.RPS, best.R.RPS/max(worst.R.RPS, 0.001)))
			}
		}
	}
	return out
}

func (r RowView) latency() float64 {
	if r.R == nil {
		return 0
	}
	return r.R.P50
}
