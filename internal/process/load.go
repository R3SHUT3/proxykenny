package process

import (
	"fmt"
	"proxykenny/internal/face"
	"proxykenny/internal/options"

	"github.com/davidbyttow/govips/v2/vips"
)

func header(src []byte, page int) (w, h, pages, orient int, err error) {
	ip := vips.NewImportParams()
	if page > 0 {
		ip.Page.Set(page)
	}
	ip.AutoRotate.Set(false)
	img, err := vips.LoadImageFromBuffer(src, ip)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer img.Close()
	return img.Width(), img.Height(), max(1, img.Pages()), img.Orientation(), nil
}

// load décode l'image, déjà redimensionnée si possible (shrink-on-load).
func (p *Processor) load(src []byte, o *options.Options, w, h int, animated bool) (*vips.ImageRef, error) {
	ip := vips.NewImportParams()
	if o.Page > 0 {
		ip.Page.Set(o.Page)
	}
	if animated {
		ip.NumPages.Set(-1)
	}
	rt := o.Resize
	if rt == options.ResizeAuto && !(o.Width > 0 && o.Height > 0) {
		rt = options.ResizeFit
	}

	if o.Crop == nil {
		rw, rh, bw, bh, ok := Scale(rt, w, h, o.Width, o.Height, o.Enlarge)
		if ok {
			crop, size := vips.InterestingNone, vips.SizeForce
			tw, th := rw, rh
			// Cas fréquents délégués entièrement à libvips (le plus rapide).
			if rt == options.ResizeFill && !o.Gravity.Offset {
				switch o.Gravity.Type {
				case options.GravityCenter:
					crop, tw, th, size = vips.InterestingCentre, bw, bh, vips.SizeBoth
				case options.GravitySmart:
					crop, tw, th, size = vips.InterestingAttention, bw, bh, vips.SizeBoth
				}
			}
			img, err := vips.LoadThumbnailFromBuffer(src, tw, th, crop, size, ip)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
			}
			return img, nil
		}
	}

	// Chargement complet : recadrage explicite (c:) ou pas de redimensionnement.
	img, err := vips.LoadImageFromBuffer(src, ip)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if o.AutoRot {
		if err := img.AutoRotate(); err != nil {
			img.Close()
			return nil, fmt.Errorf("%w: rotation EXIF: %v", ErrProcessing, err)
		}
	}
	if o.Crop != nil {
		cw, ch := o.Crop.Size(img.Width(), img.Height())
		if err := p.gravityCrop(img, cw, ch, o.Crop.Gravity, func() []face.Rect { return p.detectFaces(img) }); err != nil {
			img.Close()
			return nil, fmt.Errorf("%w: crop: %v", ErrProcessing, err)
		}
		if rw, rh, _, _, ok := Scale(rt, img.Width(), img.Height(), o.Width, o.Height, o.Enlarge); ok {
			if err := img.ThumbnailWithSize(rw, rh, vips.InterestingNone, vips.SizeForce); err != nil {
				img.Close()
				return nil, fmt.Errorf("%w: resize: %v", ErrProcessing, err)
			}
		}
	}
	return img, nil
}

func sizeFor(enlarge bool) vips.Size {
	if enlarge {
		return vips.SizeBoth
	}
	return vips.SizeDown
}

// materialize copie les pixels en mémoire pour permettre plusieurs lectures.
// Ferme l'image d'origine.
func materialize(img *vips.ImageRef) (*vips.ImageRef, error) {
	defer img.Close()
	pix, err := img.ToBytes()
	if err != nil {
		return nil, err
	}
	return vips.NewImageFromMemory(pix, img.Width(), img.Height(), img.Bands(), img.BandFormat(), img.Interpretation())
}
