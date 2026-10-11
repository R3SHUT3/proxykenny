package process

import "github.com/davidbyttow/govips/v2/vips"

// pixelate réduit puis agrandit l'image en « plus proche voisin ».
func pixelate(img *vips.ImageRef, size int) error {
	w, h := img.Width(), img.Height()
	size = min(size, w, h)
	if size < 2 {
		return nil
	}
	if err := img.Resize(1/float64(size), vips.KernelLinear); err != nil {
		return err
	}
	if err := img.Resize(float64(size), vips.KernelNearest); err != nil {
		return err
	}
	if img.Width() > w || img.Height() > h {
		if err := img.ExtractArea(0, 0, min(img.Width(), w), min(img.Height(), h)); err != nil {
			return err
		}
	}
	if img.Width() < w || img.Height() < h {
		return img.Embed(0, 0, w, h, vips.ExtendCopy)
	}
	return nil
}
