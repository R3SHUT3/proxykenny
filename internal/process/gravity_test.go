package process

import (
	"testing"

	"proxykenny/internal/options"
)

func TestCropOffset(t *testing.T) {
	cases := []struct {
		g      options.GravityType
		dx, dy float64
		x, y   int
	}{
		{options.GravityCenter, 0, 0, 50, 25},
		{options.GravityNorthWest, 0, 0, 0, 0},
		{options.GravitySouthEast, 0, 0, 100, 50},
		{options.GravityNorth, 0, 0, 50, 0},
		{options.GravityEast, 10, 0, 90, 25},    // décalage vers l'intérieur
		{options.GravityWest, 1000, 0, 100, 25}, // borné au bord
	}
	for _, c := range cases {
		x, y := CropOffset(c.g, 200, 100, 100, 50, c.dx, c.dy)
		if x != c.x || y != c.y {
			t.Errorf("%s : (%d,%d) au lieu de (%d,%d)", c.g, x, y, c.x, c.y)
		}
	}
}

func TestScale(t *testing.T) {
	cases := []struct {
		rt             options.ResizeType
		w, h, tw, th   int
		enlarge        bool
		rw, rh, bw, bh int
	}{
		{options.ResizeFit, 4000, 3000, 400, 400, false, 400, 300, 400, 300},
		{options.ResizeFill, 4000, 3000, 400, 400, false, 533, 400, 400, 400},
		{options.ResizeForce, 4000, 3000, 400, 100, false, 400, 100, 400, 100},
		{options.ResizeFit, 4000, 3000, 0, 150, false, 200, 150, 200, 150},
		{options.ResizeFit, 100, 50, 400, 400, false, 100, 50, 100, 50},       // pas d'agrandissement
		{options.ResizeFit, 100, 50, 400, 400, true, 400, 200, 400, 200},      // agrandissement autorisé
		{options.ResizeAuto, 3000, 4000, 400, 300, false, 225, 300, 225, 300}, // orientations différentes → fit
		{options.ResizeAuto, 4000, 3000, 400, 300, false, 400, 300, 400, 300}, // même orientation → fill
	}
	for _, c := range cases {
		rw, rh, bw, bh, ok := Scale(c.rt, c.w, c.h, c.tw, c.th, c.enlarge)
		if !ok || rw != c.rw || rh != c.rh || bw != c.bw || bh != c.bh {
			t.Errorf("%v %dx%d→%dx%d : %dx%d boîte %dx%d", c.rt, c.w, c.h, c.tw, c.th, rw, rh, bw, bh)
		}
	}
	if _, _, _, _, ok := Scale(options.ResizeFit, 100, 100, 0, 0, false); ok {
		t.Error("aucune dimension : pas de redimensionnement attendu")
	}
}
