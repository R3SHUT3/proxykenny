// Package security signe et vérifie les URL (HMAC-SHA256, comme imgproxy) :
// signature = base64url( HMAC_SHA256(clé, sel + chemin) ).
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// Sign renvoie la signature d'un chemin (ex. "/rs:fit:300:0/plain/https://...").
func Sign(key, salt []byte, path string) string {
	m := hmac.New(sha256.New, key)
	m.Write(salt)
	m.Write([]byte(path))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify compare en temps constant la signature reçue à la signature attendue.
func Verify(key, salt []byte, sig, path string) bool {
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, key)
	m.Write(salt)
	m.Write([]byte(path))
	return hmac.Equal(got, m.Sum(nil))
}
