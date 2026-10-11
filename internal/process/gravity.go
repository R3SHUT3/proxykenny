package process

import (
	"proxykenny/internal/face"
	"proxykenny/internal/options"

	"github.com/davidbyttow/govips/v2/vips"
)

// CropOffset calcule le coin haut-gauche d'une zone cw×ch dans une image w×h,
// selon une gravité « boussole » et un décalage optionnel en pixels.
func CropOffset(g options.GravityType, w, h, cw, ch int, dx, dy float64) (int, int) {
	x, y := (w-cw)/2, (h-ch)/2
	switch g {
	case options.GravityNorth:
		y = 0
	case options.GravitySouth:
		y = h - ch
	case options.GravityEast:
		x = w - cw
	case options.GravityWest:
		x = 0
	case options.GravityNorthEast:
		x, y = w-cw, 0
	case options.GravityNorthWest:
		x, y = 0, 0
	case options.GravitySouthEast:
		x, y = w-cw, h-ch
	case options.GravitySouthWest:
		x, y = 0, h-ch
	}
	// Le décalage s'éloigne du bord d'ancrage (comme imgproxy).
	switch g {
	case options.GravityEast, options.GravityNorthEast, options.GravitySouthEast:
		x -= int(dx)
	default:
		x += int(dx)
	}
	switch g {
	case options.GravitySouth, options.GravitySouthEast, options.GravitySouthWest:
		y -= int(dy)
	default:
		y += int(dy)
	}
	return clamp(x, 0, w-cw), clamp(y, 0, h-ch)
}

// CenterOn place une zone cw×ch centrée sur (cx, cy), sans sortir de l'image.
func CenterOn(cx, cy float64, w, h, cw, ch int) (int, int) {
	return clamp(int(cx)-cw/2, 0, w-cw), clamp(int(cy)-ch/2, 0, h-ch)
}

// Scale calcule les dimensions cibles d'un redimensionnement.
// Renvoie (rw, rh) : taille après redimensionnement ; (bw, bh) : boîte finale (fill) ;
// ok=false si aucun redimensionnement n'est demandé.
func Scale(rt options.ResizeType, w, h, tw, th int, enlarge bool) (rw, rh, bw, bh int, ok bool) {
	if (tw <= 0 && th <= 0) || w <= 0 || h <= 0 {
		return w, h, w, h, false
	}
	fw, fh := float64(tw)/float64(w), float64(th)/float64(h)
	if rt == options.ResizeAuto {
		rt = options.ResizeFit
		if tw > 0 && th > 0 && (tw >= th) == (w >= h) {
			rt = options.ResizeFill
		}
	}
	var sx, sy float64
	switch {
	case tw <= 0:
		sx, sy = fh, fh
	case th <= 0:
		sx, sy = fw, fw
	case rt == options.ResizeFill:
		sx = max(fw, fh)
		sy = sx
	case rt == options.ResizeForce:
		sx, sy = fw, fh
	default: // fit
		sx = min(fw, fh)
		sy = sx
	}
	if !enlarge {
		sx, sy = min(sx, 1), min(sy, 1)
	}
	rw, rh = max(1, round(float64(w)*sx)), max(1, round(float64(h)*sy))
	bw, bh = rw, rh
	if rt == options.ResizeFill && tw > 0 && th > 0 {
		bw, bh = min(tw, rw), min(th, rh)
	}
	return rw, rh, bw, bh, true
}

func round(f float64) int { return int(f + 0.5) }

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return max(lo, min(v, hi))
}

// gravityCrop réduit l'image à cw×ch selon la gravité demandée.
func (p *Processor) gravityCrop(img *vips.ImageRef, cw, ch int, g options.Gravity, faces func() []face.Rect) error {
	w, h := img.Width(), img.Height()
	cw, ch = min(cw, w), min(ch, h)
	if cw == w && ch == h {
		return nil
	}
	var x, y int
	switch g.Type {
	case options.GravitySmart:
		return img.SmartCrop(cw, ch, vips.InterestingAttention)
	case options.GravityFace:
		fs := faces()
		b, ok := face.Bounds(fs)
		if ok && (b.W > cw || b.H > ch) {
			// Le groupe ne tient pas dans le cadre : on centre sur le visage principal.
			b = fs[0]
		}
		if !ok {
			// Aucun visage : on se rabat sur la zone la plus « intéressante ».
			return img.SmartCrop(cw, ch, vips.InterestingAttention)
		}
		x, y = CenterOn(float64(b.X)+float64(b.W)/2, float64(b.Y)+float64(b.H)/2, w, h, cw, ch)
	case options.GravityFocus:
		x, y = CenterOn(g.X*float64(w), g.Y*float64(h), w, h, cw, ch)
	default:
		x, y = CropOffset(g.Type, w, h, cw, ch, g.X, g.Y)
	}
	return img.ExtractArea(x, y, cw, ch)
}
