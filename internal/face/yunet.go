package face

import (
	_ "embed"
	"fmt"
	"math"
	"sort"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// YuNet (OpenCV Zoo, licence MIT) : petit réseau de neurones de détection de visages,
// fiable sur les petits visages (dès ~10 px), les têtes inclinées, de trois quarts ou en
// partie couvertes. Exécuté par ONNX Runtime sur CPU, 1 thread par détection.
//
//go:embed yunet.onnx
var yunetModel []byte

var yunetOutputs = []string{"cls_8", "cls_16", "cls_32", "obj_8", "obj_16", "obj_32", "bbox_8", "bbox_16", "bbox_32"}

type YuNet struct {
	session        *ort.DynamicAdvancedSession
	ScoreThreshold float32 // score minimal d'un visage (0..1)
	NMSThreshold   float32 // recouvrement au-delà duquel deux détections sont fusionnées
}

var ortInit sync.Once
var ortErr error

// NewYuNet charge ONNX Runtime (bibliothèque partagée libPath) et le modèle embarqué.
func NewYuNet(libPath string) (*YuNet, error) {
	ortInit.Do(func() {
		ort.SetSharedLibraryPath(libPath)
		ortErr = ort.InitializeEnvironment(ort.WithLogLevelError())
	})
	if ortErr != nil {
		return nil, fmt.Errorf("ONNX Runtime (%s): %w", libPath, ortErr)
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()
	// 1 thread : le parallélisme vient des workers ; évite aussi l'épinglage de threads
	// (« affinity ») refusé dans un conteneur.
	for _, set := range []func() error{
		func() error { return opts.SetIntraOpNumThreads(1) },
		func() error { return opts.SetInterOpNumThreads(1) },
		func() error { return opts.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll) },
	} {
		if err := set(); err != nil {
			return nil, err
		}
	}
	s, err := ort.NewDynamicAdvancedSessionWithONNXData(yunetModel, []string{"input"}, yunetOutputs, opts)
	if err != nil {
		return nil, fmt.Errorf("chargement de YuNet: %w", err)
	}
	return &YuNet{session: s, ScoreThreshold: 0.6, NMSThreshold: 0.3}, nil
}

func (y *YuNet) Name() string  { return "yunet" }
func (y *YuNet) Channels() int { return 3 }

type scored struct {
	r     [4]float32 // x, y, w, h
	score float32
}

// Detect cherche les visages dans une image RGB (3 octets par pixel, w×h).
func (y *YuNet) Detect(rgb []uint8, w, h int) []Rect {
	if w <= 0 || h <= 0 || len(rgb) < w*h*3 {
		return nil
	}
	// Le réseau travaille par blocs de 32 px : on complète l'image avec du noir.
	pw, ph := (w+31)/32*32, (h+31)/32*32
	plane := pw * ph
	in := make([]float32, 3*plane)
	for yy := range h {
		row := yy * pw
		for xx := range w {
			o := (yy*w + xx) * 3
			// Ordre BGR, valeurs 0..255 (comme OpenCV).
			in[row+xx] = float32(rgb[o+2])
			in[plane+row+xx] = float32(rgb[o+1])
			in[2*plane+row+xx] = float32(rgb[o])
		}
	}
	input, err := ort.NewTensor(ort.NewShape(1, 3, int64(ph), int64(pw)), in)
	if err != nil {
		return nil
	}
	defer input.Destroy()
	outputs := make([]ort.Value, len(yunetOutputs))
	if err := y.session.Run([]ort.Value{input}, outputs); err != nil {
		return nil
	}
	defer func() {
		for _, o := range outputs {
			if o != nil {
				o.Destroy()
			}
		}
	}()
	data := func(i int) []float32 {
		if t, ok := outputs[i].(*ort.Tensor[float32]); ok {
			return t.GetData()
		}
		return nil
	}

	var cands []scored
	for k, stride := range []int{8, 16, 32} {
		cls, obj, box := data(k), data(3+k), data(6+k)
		cols, rows := pw/stride, ph/stride
		n := min(rows*cols, len(cls), len(obj), len(box)/4)
		for i := range n {
			s := float32(math.Sqrt(float64(clamp01(cls[i]) * clamp01(obj[i]))))
			if s < y.ScoreThreshold {
				continue
			}
			r, c := i/cols, i%cols
			cx := (float32(c) + box[4*i]) * float32(stride)
			cy := (float32(r) + box[4*i+1]) * float32(stride)
			bw := float32(math.Exp(float64(box[4*i+2]))) * float32(stride)
			bh := float32(math.Exp(float64(box[4*i+3]))) * float32(stride)
			cands = append(cands, scored{r: [4]float32{cx - bw/2, cy - bh/2, bw, bh}, score: s})
		}
	}

	// Suppression des doublons (NMS) : on garde la meilleure détection de chaque visage.
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	var out []Rect
	var kept []scored
	for _, c := range cands {
		dup := false
		for _, k := range kept {
			if iou(c.r, k.r) > y.NMSThreshold {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		kept = append(kept, c)
		r := Clamp(Rect{X: int(c.r[0] + 0.5), Y: int(c.r[1] + 0.5), W: int(c.r[2] + 0.5), H: int(c.r[3] + 0.5)}, w, h)
		if r.W > 1 && r.H > 1 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].W*out[i].H > out[j].W*out[j].H })
	return out
}

func clamp01(v float32) float32 { return max(0, min(v, 1)) }

func iou(a, b [4]float32) float32 {
	x0, y0 := max(a[0], b[0]), max(a[1], b[1])
	x1, y1 := min(a[0]+a[2], b[0]+b[2]), min(a[1]+a[3], b[1]+b[3])
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	inter := (x1 - x0) * (y1 - y0)
	return inter / (a[2]*a[3] + b[2]*b[3] - inter)
}
