package options

import (
	"encoding/base64"
	"testing"
)

var lim = Limits{MaxDim: 4000}

func TestParsePlain(t *testing.T) {
	o, src, err := Parse("/rs:fill:300:200/g:face/bl:2.5/q:70/plain/https://ex.com/a/b.jpg@webp", lim)
	if err != nil {
		t.Fatal(err)
	}
	if src != "https://ex.com/a/b.jpg" {
		t.Errorf("source = %q", src)
	}
	if o.Resize != ResizeFill || o.Width != 300 || o.Height != 200 {
		t.Errorf("resize = %v %dx%d", o.Resize, o.Width, o.Height)
	}
	if o.Gravity.Type != GravityFace || o.Blur != 2.5 || o.Quality != 70 || o.Format != "webp" {
		t.Errorf("options = %+v", o)
	}
}

func TestParseBase64(t *testing.T) {
	enc := base64.RawURLEncoding.EncodeToString([]byte("https://ex.com/photo.png?v=2"))
	o, src, err := Parse("/w:120/"+enc+".avif", lim)
	if err != nil {
		t.Fatal(err)
	}
	if src != "https://ex.com/photo.png?v=2" || o.Width != 120 || o.Format != "avif" {
		t.Errorf("src=%q w=%d f=%q", src, o.Width, o.Format)
	}
}

func TestParseCropFocusAndDPR(t *testing.T) {
	o, _, err := Parse("/c:500:400:fp:0.3:0.7/s:100:50/dpr:2/plain/local:///x.jpg", lim)
	if err != nil {
		t.Fatal(err)
	}
	if o.Crop == nil || o.Crop.W != 500 || o.Crop.Gravity.Type != GravityFocus || o.Crop.Gravity.X != 0.3 {
		t.Errorf("crop = %+v", o.Crop)
	}
	if o.Width != 200 || o.Height != 100 {
		t.Errorf("dpr non appliqué : %dx%d", o.Width, o.Height)
	}
}

func TestParseFaceOptions(t *testing.T) {
	o, _, err := Parse("/rs:fill:256:256/fc:0.4:largest/fd/plain/local:///x.jpg", lim)
	if err != nil {
		t.Fatal(err)
	}
	if o.FaceZoom != 0.4 || !o.FaceOne || !o.FaceDbg {
		t.Errorf("fc/fd : %+v", o)
	}
	if o, _, _ = Parse("/fc/plain/local:///x.jpg", lim); o.FaceZoom != 0.5 || o.FaceOne {
		t.Errorf("fc par défaut : zoom %v, un seul %v", o.FaceZoom, o.FaceOne)
	}
}

func TestCropFraction(t *testing.T) {
	o, _, err := Parse("/c:0.5:0.25:sowe/plain/local:///x.jpg", lim)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := o.Crop.Size(1000, 800); w != 500 || h != 200 {
		t.Errorf("fraction : %dx%d", w, h)
	}
	c := &Crop{W: 5000, H: 0}
	if w, h := c.Size(1000, 800); w != 1000 || h != 800 {
		t.Errorf("borne / 0 = tout : %dx%d", w, h)
	}
}

func TestParseErrors(t *testing.T) {
	for _, p := range []string{
		"/rs:zoom:10:10/plain/http://a/b",  // type inconnu
		"/w:99999/plain/http://a/b",        // dimension hors limites
		"/bl:500/plain/http://a/b",         // sigma hors limites
		"/rot:45/plain/http://a/b",         // rotation non multiple de 90
		"/g:fp:2:0/plain/http://a/b",       // point focal hors [0,1]
		"/f:bmp/plain/http://a/b",          // format de sortie inconnu
		"/nimportequoi:1/plain/http://a/b", // option inconnue
		"/w:100",                           // pas de source
		"/q:101/plain/http://a/b",          // qualité hors limites
	} {
		if _, _, err := Parse(p, lim); err == nil {
			t.Errorf("%s : erreur attendue", p)
		}
	}
}
