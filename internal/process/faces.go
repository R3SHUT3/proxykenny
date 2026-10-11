package process

import (
	"fmt"
	"math"
	"proxykenny/internal/face"
	"proxykenny/internal/metrics"
	"proxykenny/internal/options"
	"time"

	"github.com/davidbyttow/govips/v2/vips"
)

// Visages : zoom (fc), cadres (fd), anonymisation (bf/pf) et détection.

// loadFaceZoom recadre serré autour des visages (avatars, photos de profil).
// L'image est chargée réduite à au plus 2560 px (ou 3× la cible) : assez de détails pour
// zoomer sur un visage, sans décoder une photo de 50 Mpx en entier.
func (p *Processor) loadFaceZoom(src []byte, o *options.Options, res *Result) (*vips.ImageRef, error) {
	limit := max(2560, 3*max(o.Width, o.Height))
	ip := vips.NewImportParams()
	if o.Page > 0 {
		ip.Page.Set(o.Page)
	}
	img, err := vips.LoadThumbnailFromBuffer(src, limit, limit, vips.InterestingNone, vips.SizeDown, ip)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if img, err = materialize(img); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProcessing, err)
	}
	fail := func(err error) (*vips.ImageRef, error) {
		img.Close()
		return nil, fmt.Errorf("%w: zoom visage: %v", ErrProcessing, err)
	}
	faces := p.detectFaces(img)
	res.Faces = len(faces)
	if o.FaceOne && len(faces) > 1 {
		faces = faces[:1] // triés du plus grand au plus petit
	}
	w, h := img.Width(), img.Height()
	b, ok := face.Bounds(faces)
	if !ok {
		// Aucun visage : recadrage « smart » classique aux dimensions demandées.
		if o.Width > 0 && o.Height > 0 {
			if err := img.ThumbnailWithSize(o.Width, o.Height, vips.InterestingAttention, sizeFor(o.Enlarge)); err != nil {
				return fail(err)
			}
		} else if rw, rh, _, _, ok := Scale(options.ResizeFit, w, h, o.Width, o.Height, o.Enlarge); ok {
			if err := img.ThumbnailWithSize(rw, rh, vips.InterestingNone, vips.SizeForce); err != nil {
				return fail(err)
			}
		}
		return img, nil
	}

	x, y, cw, ch := FaceRegion(b, o.FaceZoom, w, h, o.Width, o.Height)
	if err := img.ExtractArea(x, y, cw, ch); err != nil {
		return fail(err)
	}
	if rw, rh, _, _, ok := Scale(options.ResizeFit, cw, ch, o.Width, o.Height, o.Enlarge); ok {
		if o.Width > 0 && o.Height > 0 {
			// Avatars / vignettes : toujours la taille exacte demandée (proportions déjà
			// ajustées), quitte à agrandir un visage petit sur la photo d'origine.
			rw, rh = o.Width, o.Height
		}
		if err := img.ThumbnailWithSize(rw, rh, vips.InterestingNone, vips.SizeForce); err != nil {
			return fail(err)
		}
	}
	return img, nil
}

// FaceRegion calcule la zone à garder autour des visages b dans une image w×h :
// marge relative autour des visages, un peu plus d'espace sous le visage (menton, épaules),
// puis ajustement aux proportions de la cible tw×th si elles sont connues.
func FaceRegion(b face.Rect, margin float64, w, h, tw, th int) (x, y, cw, ch int) {
	fw, fh := float64(b.W)*(1+2*margin), float64(b.H)*(1+2*margin)
	cx, cy := float64(b.X)+float64(b.W)/2, float64(b.Y)+float64(b.H)*0.6
	if tw > 0 && th > 0 {
		ar := float64(tw) / float64(th)
		if fw/fh < ar {
			fw = fh * ar
		} else {
			fh = fw / ar
		}
		// Trop grand pour l'image : on réduit en gardant les proportions.
		if s := min(float64(w)/fw, float64(h)/fh, 1); s < 1 {
			fw, fh = fw*s, fh*s
		}
	}
	cw, ch = max(1, min(int(fw+0.5), w)), max(1, min(int(fh+0.5), h))
	x, y = CenterOn(cx, cy, w, h, cw, ch)
	return x, y, cw, ch
}

// drawFaces dessine un cadre vert autour de chaque visage (image en mémoire requise).
func drawFaces(img *vips.ImageRef, faces []face.Rect) error {
	t := max(2, min(img.Width(), img.Height())/250) // épaisseur proportionnelle
	ink := vips.ColorRGBA{R: 0, G: 230, B: 118, A: 255}
	for _, r := range faces {
		for i := range t {
			if r.W-2*i <= 0 || r.H-2*i <= 0 {
				break
			}
			if err := img.DrawRect(ink, r.X+i, r.Y+i, r.W-2*i, r.H-2*i, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// detectFaces cherche les visages sur une copie réduite (RGB pour YuNet, gris pour pigo).
func (p *Processor) detectFaces(img *vips.ImageRef) []face.Rect {
	t0 := time.Now()
	defer func() { metrics.FaceDetect.WithLabelValues(p.faces.Name()).Observe(time.Since(t0).Seconds()) }()
	small, err := img.Copy()
	if err != nil {
		return nil
	}
	defer small.Close()
	w, h := img.Width(), img.Height()
	s := 1.0
	if m := max(w, h); m > p.cfg.FaceMaxSide {
		s = float64(p.cfg.FaceMaxSide) / float64(m)
		if err := small.Resize(s, vips.KernelLinear); err != nil {
			return nil
		}
	}
	if small.HasAlpha() {
		if err := small.Flatten(&vips.Color{R: 255, G: 255, B: 255}); err != nil {
			return nil
		}
	}
	ch := p.faces.Channels()
	target := vips.InterpretationSRGB
	if ch == 1 {
		target = vips.InterpretationBW
	}
	if small.Interpretation() != target {
		if err := small.ToColorSpace(target); err != nil {
			return nil
		}
	}
	if small.BandFormat() != vips.BandFormatUchar {
		if err := small.Cast(vips.BandFormatUchar); err != nil { // images 16 bits
			return nil
		}
	}
	if small.Bands() > ch {
		if err := small.ExtractBand(0, ch); err != nil {
			return nil
		}
	}
	pix, err := small.ToBytes()
	sw, sh := small.Width(), small.Height()
	if err != nil || len(pix) != sw*sh*ch {
		return nil
	}
	rects := p.faces.Detect(pix, sw, sh)
	out := make([]face.Rect, 0, len(rects))
	for _, r := range rects {
		out = append(out, face.Clamp(face.Rect{
			X: int(float64(r.X) / s), Y: int(float64(r.Y) / s),
			W: int(float64(r.W) / s), H: int(float64(r.H) / s),
		}, w, h))
	}
	return out
}

// HeadRegion agrandit le cadre du visage (yeux → bouche) à toute la tête : cheveux, front, menton.
func HeadRegion(r face.Rect, w, h int) face.Rect {
	return face.Clamp(face.Rect{
		X: r.X - r.W/4, W: r.W * 3 / 2,
		Y: r.Y - r.H*2/5, H: r.H * 37 / 20,
	}, w, h)
}

// anonymize floute ou pixellise la tête (zone r), fondue dans l'image par un masque
// elliptique aux bords progressifs : pas de rectangle visible.
func (p *Processor) anonymize(img *vips.ImageRef, r face.Rect, o *options.Options) error {
	if r.W < 4 || r.H < 4 {
		return nil
	}
	sub, err := img.Copy()
	if err != nil {
		return err
	}
	defer sub.Close()
	if err := sub.ExtractArea(r.X, r.Y, r.W, r.H); err != nil {
		return err
	}
	inner := 0.6 // début du fondu (fraction du rayon) : flou très doux
	if o.PixFace > 1 {
		if err := pixelate(sub, o.PixFace); err != nil {
			return err
		}
		inner = 0.85 // pixellisation : bord plus net
	}
	if o.BlurFace > 0 {
		// Flou proportionnel à la taille du visage pour rester efficace quelle que soit la taille.
		if err := sub.GaussianBlur(max(o.BlurFace, float64(r.W)/10)); err != nil {
			return err
		}
	}
	// Images 8 bits sans transparence (cas courant) : fondu elliptique.
	// Autres cas (16 bits, alpha) : collage rectangulaire.
	if img.BandFormat() != vips.BandFormatUchar || img.HasAlpha() {
		return img.Insert(sub, r.X, r.Y, false, nil)
	}
	mask, err := vips.NewImageFromMemory(EllipseMask(r.W, r.H, inner), r.W, r.H, 1, vips.BandFormatUchar, vips.InterpretationBW)
	if err != nil {
		return err
	}
	defer mask.Close()
	if err := sub.BandJoin(mask); err != nil {
		return err
	}
	if err := img.Composite(sub, vips.BlendModeOver, r.X, r.Y); err != nil {
		return err
	}
	// La fusion ajoute un canal alpha (opaque) : on le retire aussitôt, sinon le visage
	// suivant verrait une image « avec transparence » et perdrait le fondu.
	return img.ExtractBand(0, img.Bands()-1)
}

// EllipseMask renvoie un masque w×h (0..255) : opaque jusqu'à inner × rayon, puis fondu
// progressif (smoothstep) jusqu'au bord de l'ellipse.
func EllipseMask(w, h int, inner float64) []byte {
	m := make([]byte, w*h)
	rx, ry := float64(w)/2, float64(h)/2
	for y := range h {
		dy := (float64(y) + 0.5 - ry) / ry
		for x := range w {
			dx := (float64(x) + 0.5 - rx) / rx
			d := math.Sqrt(dx*dx + dy*dy)
			a := 0.0
			switch {
			case d <= inner:
				a = 1
			case d < 1:
				t := (1 - d) / (1 - inner)
				a = t * t * (3 - 2*t)
			}
			m[y*w+x] = uint8(a*255 + 0.5)
		}
	}
	return m
}
