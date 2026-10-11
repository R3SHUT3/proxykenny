package security

import "testing"

func TestSignVerify(t *testing.T) {
	key, salt := []byte("secret"), []byte("hello")
	path := "/rs:fit:300:0/plain/https://ex.com/a.jpg"
	sig := Sign(key, salt, path)
	if !Verify(key, salt, sig, path) {
		t.Fatal("signature valide refusée")
	}
	if Verify(key, salt, sig, path+"x") {
		t.Fatal("chemin modifié accepté")
	}
	if Verify([]byte("autre"), salt, sig, path) {
		t.Fatal("mauvaise clé acceptée")
	}
	if Verify(key, salt, "pas-du-base64!", path) {
		t.Fatal("signature malformée acceptée")
	}
}
