// Package face détecte les visages avec pigo (Go pur, sans dépendance native).
// La détection se fait sur une image réduite en niveaux de gris : rapide et peu gourmande.
package face

import (
	_ "embed"
	"fmt"
	"sort"

	pigo "github.com/esimov/pigo/core"
)

//go:embed facefinder
var cascade []byte

// Rect est un visage détecté, en pixels.
type Rect struct{ X, Y, W, H int }

// Engine est un moteur de détection de visages (YuNet par défaut, pigo en secours).
type Engine interface {
	Name() string
	Channels() int // 1 = niveaux de gris, 3 = RGB
	Detect(pix []uint8, w, h int) []Rect
}

func (d *Detector) Name() string  { return "pigo" }
func (d *Detector) Channels() int { return 1 }

// TiltMode : recherche des têtes inclinées (pigo ne trouve que les visages droits par défaut).
type TiltMode string

const (
	TiltOff      TiltMode = "off"      // visages droits uniquement (le plus rapide)
	TiltFallback TiltMode = "fallback" // inclinaisons essayées seulement si aucun visage droit
	TiltAlways   TiltMode = "always"   // toujours (≈ 3× plus lent)
)

// Inclinaisons essayées en plus de 0, en fraction de tour pour pigo (0.05 ≈ 18°, 0.09 ≈ 32°).
var tiltAngles = []float64{0.05, -0.05, 0.09, -0.09}

// Detector est sûr en concurrence : le classifieur n'est lu qu'en lecture.
type Detector struct {
	classifier *pigo.Pigo
	MinQuality float32 // score minimal pour retenir un visage
	Tilt       TiltMode
}

// DefaultMinQuality : mesuré sur des photos réelles, les vrais visages scorent > 100,
// les faux positifs (drapeaux, écussons, doublons) < 20.
const DefaultMinQuality = 30

func New() (*Detector, error) {
	c, err := pigo.NewPigo().Unpack(cascade)
	if err != nil {
		return nil, fmt.Errorf("chargement du modèle de visages: %w", err)
	}
	return &Detector{classifier: c, MinQuality: DefaultMinQuality, Tilt: TiltFallback}, nil
}

// Detect cherche les visages dans une image en niveaux de gris (1 octet par pixel).
func (d *Detector) Detect(gray []uint8, w, h int) []Rect {
	if w <= 0 || h <= 0 || len(gray) < w*h {
		return nil
	}
	side := min(w, h)
	params := pigo.CascadeParams{
		MinSize:     max(20, side/25),
		MaxSize:     side,
		ShiftFactor: 0.1,
		ScaleFactor: 1.1,
		ImageParams: pigo.ImageParams{Pixels: gray, Rows: h, Cols: w, Dim: w},
	}
	run := func(angle float64) []pigo.Detection {
		return d.classifier.ClusterDetections(d.classifier.RunCascade(params, angle), 0.2)
	}
	dets := run(0)
	if d.Tilt == TiltAlways || (d.Tilt == TiltFallback && !d.anyAbove(dets)) {
		for _, a := range tiltAngles {
			dets = append(dets, run(a)...)
		}
	}
	// Les plus sûres d'abord ; une détection dont le centre tombe dans un visage déjà
	// retenu est un doublon (cadre plus large ou décalé autour du même visage).
	sort.Slice(dets, func(i, j int) bool { return dets[i].Q > dets[j].Q })
	out := make([]Rect, 0, len(dets))
	for _, det := range dets {
		if det.Q < d.MinQuality {
			break
		}
		dup := false
		for _, k := range out {
			if det.Col >= k.X && det.Col <= k.X+k.W && det.Row >= k.Y && det.Row <= k.Y+k.H {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, Clamp(Rect{X: det.Col - det.Scale/2, Y: det.Row - det.Scale/2, W: det.Scale, H: det.Scale}, w, h))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].W*out[i].H > out[j].W*out[j].H })
	return out
}

func (d *Detector) anyAbove(dets []pigo.Detection) bool {
	for _, x := range dets {
		if x.Q >= d.MinQuality {
			return true
		}
	}
	return false
}

// Clamp garde le rectangle dans l'image.
func Clamp(r Rect, w, h int) Rect {
	if r.X < 0 {
		r.W += r.X
		r.X = 0
	}
	if r.Y < 0 {
		r.H += r.Y
		r.Y = 0
	}
	r.W = max(0, min(r.W, w-r.X))
	r.H = max(0, min(r.H, h-r.Y))
	return r
}

// Bounds renvoie le rectangle englobant tous les visages.
func Bounds(rs []Rect) (Rect, bool) {
	if len(rs) == 0 {
		return Rect{}, false
	}
	x0, y0, x1, y1 := rs[0].X, rs[0].Y, rs[0].X+rs[0].W, rs[0].Y+rs[0].H
	for _, r := range rs[1:] {
		x0, y0 = min(x0, r.X), min(y0, r.Y)
		x1, y1 = max(x1, r.X+r.W), max(y1, r.Y+r.H)
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, true
}
