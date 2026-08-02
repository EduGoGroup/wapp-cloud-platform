package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jwksServer levanta un endpoint que sirve el JWKS de la clave dada, armado con
// el MISMO buildJWKSConfig que empuja la pública al Edge (ADR-0025 dec.2). Que
// identity-shared acepte ese documento no es casualidad ni redundancia: es la
// comprobación de que los dos lados del grupo hablan el mismo JWKS.
func jwksServer(t *testing.T, pub *ecdsa.PublicKey, kid string) *httptest.Server {
	t.Helper()
	payload, err := buildJWKSConfig(pub, kid)
	if err != nil {
		t.Fatalf("buildJWKSConfig: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, werr := w.Write(payload.Payload); werr != nil {
			t.Errorf("escribiendo el JWKS de prueba: %v", werr)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildIdentityVerifier_LoadsKeysFromJWKS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generando clave de prueba: %v", err)
	}
	const kid = "es256-20260802"
	srv := jwksServer(t, &key.PublicKey, kid)

	mv, err := buildIdentityVerifier(srv.URL)
	if err != nil {
		t.Fatalf("buildIdentityVerifier: %v", err)
	}
	kids := mv.Kids()
	if len(kids) != 1 || kids[0] != kid {
		t.Fatalf("kids cargados: got %v, want [%s]", kids, kid)
	}
}

func TestBuildIdentityVerifier_FailsClosed(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(unreachable.Close)

	tests := []struct {
		name    string
		jwksURL string
	}{
		// El emisor responde pero no entrega claves: sin claves no se verifica.
		{name: "el JWKS responde 500", jwksURL: unreachable.URL},
		// http fuera de loopback: quien sirva el JWKS sustituye el conjunto de
		// claves entero, así que ahí se exige TLS.
		{name: "http fuera de loopback", jwksURL: "http://identity.example/.well-known/jwks.json"},
		{name: "URL relativa", jwksURL: "/.well-known/jwks.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mv, err := buildIdentityVerifier(tt.jwksURL)
			if err == nil {
				t.Fatalf("se esperaba error de arranque, got verificador %v", mv)
			}
			// El error nombra la URL: es lo que el operador necesita para saber a
			// qué identity no llegó el arranque.
			if !strings.Contains(err.Error(), tt.jwksURL) {
				t.Errorf("el error debería nombrar la URL %q: %v", tt.jwksURL, err)
			}
		})
	}
}
