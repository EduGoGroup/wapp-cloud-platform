//go:build integracion

package procesos

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/identity-shared/auth"
	"github.com/EduGoGroup/identity-shared/auth/jwt"
)

const (
	// identidadEmisor es el issuer que el verificador del servidor exige (verifier.go de
	// identity-shared: el claim iss debe ser éste).
	identidadEmisor = "identity-core"
	// identidadEmisorAjeno es el issuer de TokenDeOtroEmisor: firmado con la clave buena, el
	// único defecto del token es éste.
	identidadEmisorAjeno = "emisor-ajeno"
	// identidadKid es el kid de la clave de firma, en el header de los tokens y en el JWKS. No
	// es el de la clave JWT del propio servidor (esa vive en claves): son dos pares distintos.
	identidadKid = "identidad-procesos-1"
	// identidadRutaJWKS es la ruta del JWKS en el doble.
	identidadRutaJWKS = "/.well-known/jwks.json"
)

// identidadUUID es la forma que el servidor espera en el sub del token (el usuario es un UUID).
var identidadUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// identidad es el doble de identity-core: una clave ES256 P-256 propia de la corrida, un
// servidor HTTP que publica su JWKS y los emisores de tokens firmados con ella. El servidor
// hace el PRIMER fetch del JWKS al arrancar (eager, fail-closed), así que el doble tiene que
// estar escuchando antes de crearlo: nuevaIdentidad ya lo deja listo.
type identidad struct {
	t        *testing.T // test que la creó: los errores de emisión (inesperados) lo fallan
	clave    *ecdsa.PrivateKey
	emisor   *jwt.Manager // iss = identity-core
	ajeno    *jwt.Manager // misma clave y kid, iss = emisor-ajeno
	servidor *httptest.Server
}

// jwkEC es una entrada del JWKS (RFC 7517 §5) con el formato que consume el cliente del
// servidor: x e y en base64url SIN padding y de exactamente 32 bytes.
type jwkEC struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

// nuevaIdentidad genera una clave ES256 aleatoria (ninguna en el repo), levanta el doble en
// 127.0.0.1:<puerto efímero> sirviendo http://127.0.0.1:<p>/.well-known/jwks.json y registra su
// cierre en t.Cleanup. Devuelve la identidad ya escuchando. Falla (t.Fatalf) si no puede generar
// la clave, armar el JWKS o si el listener no quedó en 127.0.0.1 (el cliente del servidor solo
// admite http en loopback).
func nuevaIdentidad(t *testing.T) *identidad {
	t.Helper()
	clave, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("nuevaIdentidad: generando la clave ES256: %v", err)
	}
	emisor, err := jwt.NewManager(clave, identidadEmisor, identidadKid)
	if err != nil {
		t.Fatalf("nuevaIdentidad: emisor %q: %v", identidadEmisor, err)
	}
	ajeno, err := jwt.NewManager(clave, identidadEmisorAjeno, identidadKid)
	if err != nil {
		t.Fatalf("nuevaIdentidad: emisor %q: %v", identidadEmisorAjeno, err)
	}
	documento, err := jwksDe(&clave.PublicKey, identidadKid)
	if err != nil {
		t.Fatalf("nuevaIdentidad: armando el JWKS: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != identidadRutaJWKS {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(documento); err != nil {
			t.Logf("identidad: escribiendo el JWKS: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	if !strings.HasPrefix(srv.URL, "http://127.0.0.1:") {
		t.Fatalf("nuevaIdentidad: el doble debe escuchar en 127.0.0.1 y escucha en %s", srv.URL)
	}
	return &identidad{t: t, clave: clave, emisor: emisor, ajeno: ajeno, servidor: srv}
}

// jwksDe arma el documento JWKS de una clave pública P-256: una sola clave
// {"kty":"EC","crv":"P-256","kid":…,"x":…,"y":…,"use":"sig","alg":"ES256"}. Las coordenadas
// salen del punto sin comprimir (0x04 || X || Y), así que miden siempre 32 bytes aunque tengan
// ceros a la izquierda (big.Int.Bytes() los recortaría y el cliente del servidor descartaría la
// clave). Devuelve error si la serialización no da el punto de 65 bytes de P-256.
func jwksDe(pub *ecdsa.PublicKey, kid string) ([]byte, error) {
	punto, err := pub.Bytes()
	if err != nil {
		return nil, fmt.Errorf("serializando la clave pública: %w", err)
	}
	if len(punto) != 65 || punto[0] != 0x04 {
		return nil, fmt.Errorf("punto sin comprimir de %d bytes (prefijo %#x), se esperaban 65 con 0x04", len(punto), punto[0])
	}
	b64 := base64.RawURLEncoding
	doc := struct {
		Keys []jwkEC `json:"keys"`
	}{Keys: []jwkEC{{
		Kty: "EC",
		Crv: "P-256",
		Kid: kid,
		X:   b64.EncodeToString(punto[1:33]),
		Y:   b64.EncodeToString(punto[33:65]),
		Use: "sig",
		Alg: "ES256",
	}}}
	return json.Marshal(doc)
}

// JWKSURL devuelve la URL del JWKS, "http://127.0.0.1:<p>/.well-known/jwks.json": el valor de
// WAPP_IDENTITY_JWKS_URL. No recibe nada ni falla.
func (i *identidad) JWKSURL() string { return i.servidor.URL + identidadRutaJWKS }

// Entorno devuelve la variable "K=V" que apunta al servidor a este JWKS. Slice nuevo en cada
// llamada. No falla.
func (i *identidad) Entorno() []string {
	return []string{"WAPP_IDENTITY_JWKS_URL=" + i.JWKSURL()}
}

// validarEntradaToken comprueba lo que el servidor espera de un token: el usuario (sub) es un
// UUID y el system no está vacío (el valor habitual es wapp.bff, wapp.edge o wapp.platform, pero
// no se restringe, para poder probar un system equivocado). Devuelve nil si es válido.
func validarEntradaToken(usuario, system string) error {
	if !identidadUUID.MatchString(usuario) {
		return fmt.Errorf("usuario %q no es un UUID", usuario)
	}
	if system == "" {
		return errors.New("system vacío")
	}
	return nil
}

// emitir firma con m un Identity Token de 1 h para el usuario y el system. Es lo que usan
// TokenDe y TokenDeOtroEmisor; falla el test que creó la identidad (t.Fatalf) si la entrada no
// es válida o la firma falla.
func (i *identidad) emitir(m *jwt.Manager, usuario, system string) string {
	i.t.Helper()
	if err := validarEntradaToken(usuario, system); err != nil {
		i.t.Fatalf("identidad: %v", err)
	}
	token, _, err := m.GenerateIdentityToken(jwt.IdentityTokenInput{
		UserID:       usuario,
		System:       system,
		Email:        usuario + "@procesos.test",
		TokenVersion: 0,
		TTL:          time.Hour,
	})
	if err != nil {
		i.t.Fatalf("identidad: firmando el token: %v", err)
	}
	return token
}

// TokenDe devuelve un Identity Token válido: iss=identity-core, sub=usuario (un UUID),
// aud=[system], system, email=<usuario>@procesos.test, token_use=identity, token_version=0, header
// kid y vigencia de 1 h. Falla el test que creó la identidad (t.Fatalf) si el usuario no es un
// UUID, el system está vacío o la firma falla.
func (i *identidad) TokenDe(usuario, system string) string {
	i.t.Helper()
	return i.emitir(i.emisor, usuario, system)
}

// TokenDeOtroEmisor devuelve un token con el mismo formato y la misma firma buena pero con
// iss="emisor-ajeno" (distinto de identity-core): el verificador del servidor debe rechazarlo
// por el issuer y por nada más. Mismas entradas y mismos fallos que TokenDe.
func (i *identidad) TokenDeOtroEmisor(usuario, system string) string {
	i.t.Helper()
	return i.emitir(i.ajeno, usuario, system)
}

// TokenCaducado devuelve un token con firma e issuer correctos pero con exp una hora en el
// pasado (más allá de los 30 s de tolerancia del verificador), de modo que su único defecto es la
// caducidad. El Manager de identity-shared no emite TTL menores de un minuto, así que se firma
// a mano con firmarES256. Mismas entradas y mismos fallos que TokenDe.
func (i *identidad) TokenCaducado(usuario, system string) string {
	i.t.Helper()
	if err := validarEntradaToken(usuario, system); err != nil {
		i.t.Fatalf("identidad: %v", err)
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		i.t.Fatalf("identidad: generando el jti: %v", err)
	}
	ahora := time.Now()
	emitido := ahora.Add(-2 * time.Hour).Unix()
	return i.firmarES256(map[string]any{
		"iss":           identidadEmisor,
		"sub":           usuario,
		"aud":           []string{system},
		"system":        system,
		"email":         usuario + "@procesos.test",
		"token_use":     "identity",
		"token_version": 0,
		"jti":           hex.EncodeToString(jti),
		"iat":           emitido,
		"nbf":           emitido,
		"exp":           ahora.Add(-time.Hour).Unix(),
	})
}

// firmarES256 serializa un JWT compacto header.payload.firma: header {"alg":"ES256","typ":"JWT",
// "kid":<kid>}, el payload con los claims dados (JSON, base64url sin padding) y la firma ECDSA
// P-256 sobre el SHA-256 de "header.payload" en formato crudo r||s de 32+32 bytes (RFC 7518
// §3.4, NO el DER de ASN.1). Falla el test que creó la identidad (t.Fatalf) si algo no se
// puede serializar o firmar.
func (i *identidad) firmarES256(claims map[string]any) string {
	i.t.Helper()
	cabecera, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": identidadKid})
	if err != nil {
		i.t.Fatalf("identidad: serializando el header: %v", err)
	}
	carga, err := json.Marshal(claims)
	if err != nil {
		i.t.Fatalf("identidad: serializando los claims: %v", err)
	}
	b64 := base64.RawURLEncoding
	firmando := b64.EncodeToString(cabecera) + "." + b64.EncodeToString(carga)
	resumen := sha256.Sum256([]byte(firmando))
	r, s, err := ecdsa.Sign(rand.Reader, i.clave, resumen[:])
	if err != nil {
		i.t.Fatalf("identidad: firmando: %v", err)
	}
	firma := make([]byte, 64)
	r.FillBytes(firma[:32])
	s.FillBytes(firma[32:])
	return firmando + "." + b64.EncodeToString(firma)
}

// segmentoJWT decodifica el segmento idx (0 = header, 1 = payload) de un JWT compacto a un mapa
// JSON. No verifica nada. Falla el test (t.Fatalf) si el token no tiene tres segmentos o el
// segmento no es base64url sin padding con un objeto JSON.
func segmentoJWT(t *testing.T, token string, idx int) map[string]any {
	t.Helper()
	partes := strings.Split(token, ".")
	if len(partes) != 3 {
		t.Fatalf("el token tiene %d segmentos, se esperaban 3", len(partes))
	}
	crudo, err := base64.RawURLEncoding.DecodeString(partes[idx])
	if err != nil {
		t.Fatalf("segmento %d no es base64url sin padding: %v", idx, err)
	}
	var objeto map[string]any
	if err := json.Unmarshal(crudo, &objeto); err != nil {
		t.Fatalf("segmento %d no es un objeto JSON: %v", idx, err)
	}
	return objeto
}

// descargarJWKS hace GET a url y devuelve el código y el cuerpo, con la cabecera Content-Type.
// Falla el test (t.Fatalf) si la petición no se pudo hacer o leer.
func descargarJWKS(t *testing.T, url string) (codigo int, contentType string, cuerpo []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("construyendo GET %s: %v", url, err)
	}
	cliente := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := cliente.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	cuerpo, err = io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("leyendo GET %s: %v", url, err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), cuerpo
}

// TestArnes_Identidad fija el contrato del doble de identity-core: el JWKS se descarga con
// coordenadas de 32 B exactos, lo acepta el mismo cliente que usa el servidor (identity-shared),
// los tokens llevan los claims esperados, el caducado solo falla por exp, el de otro emisor solo
// por iss, y el entorno apunta al JWKS. No necesita Docker.
func TestArnes_Identidad(t *testing.T) {
	id := nuevaIdentidad(t)
	const usuario = "7f3c2a9e-4b1d-4e6a-9c58-2d1f0b8a6e34"

	t.Run("el JWKS se descarga con x e y de 32 bytes", func(t *testing.T) { probarJWKS(t, id) })

	// El primer fetch del cliente del servidor es eager y fail-closed: si el documento no le
	// sirve, el servidor no arranca. Se prueba con ese mismo cliente.
	mv, err := jwt.NewMultiVerifierFromJWKS(identidadEmisor, jwt.JWKSOptions{URL: id.JWKSURL()})
	if err != nil {
		t.Fatalf("el cliente JWKS de identity-shared rechazó el documento del doble: %v", err)
	}
	if kids := mv.Kids(); !slices.Equal(kids, []string{identidadKid}) {
		t.Fatalf("kids cargados: %v, se esperaba [%s]", kids, identidadKid)
	}

	t.Run("TokenDe verifica con la clave del JWKS", func(t *testing.T) { probarTokenDe(t, id, mv, usuario) })
	t.Run("TokenCaducado solo falla por exp", func(t *testing.T) { probarTokenCaducado(t, id, mv, usuario) })
	t.Run("TokenDeOtroEmisor solo falla por iss", func(t *testing.T) { probarTokenOtroEmisor(t, id, mv, usuario) })

	t.Run("la entrada inválida se rechaza", func(t *testing.T) {
		for _, c := range []struct{ usuario, system string }{
			{"no-es-un-uuid", "wapp.bff"},
			{"", "wapp.bff"},
			{usuario, ""},
		} {
			if err := validarEntradaToken(c.usuario, c.system); err == nil {
				t.Errorf("validarEntradaToken(%q, %q) debería fallar", c.usuario, c.system)
			}
		}
		if err := validarEntradaToken(usuario, "wapp.platform"); err != nil {
			t.Errorf("validarEntradaToken con datos buenos: %v", err)
		}
	})

	t.Run("Entorno apunta al JWKS", func(t *testing.T) {
		url := id.JWKSURL()
		if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, identidadRutaJWKS) {
			t.Fatalf("JWKSURL = %q: se esperaba http://127.0.0.1:<p>%s", url, identidadRutaJWKS)
		}
		if got, want := id.Entorno(), []string{"WAPP_IDENTITY_JWKS_URL=" + url}; !slices.Equal(got, want) {
			t.Fatalf("Entorno: got %v, want %v", got, want)
		}
	})
}

// probarJWKS descarga el JWKS del doble y comprueba su forma: 200 JSON, una sola clave EC P-256
// con kid, use y alg, y x e y en base64url sin padding que decodifican a exactamente 32 bytes y
// coinciden con el punto de la clave pública. Otra ruta debe dar 404.
func probarJWKS(t *testing.T, id *identidad) {
	t.Helper()
	codigo, tipo, cuerpo := descargarJWKS(t, id.JWKSURL())
	if codigo != http.StatusOK || !strings.HasPrefix(tipo, "application/json") {
		t.Fatalf("GET JWKS: código %d, Content-Type %q", codigo, tipo)
	}
	var doc struct {
		Keys []jwkEC `json:"keys"`
	}
	if err := json.Unmarshal(cuerpo, &doc); err != nil {
		t.Fatalf("el JWKS no es JSON: %v\n%s", err, cuerpo)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("el JWKS tiene %d claves, se esperaba 1: %s", len(doc.Keys), cuerpo)
	}
	k := doc.Keys[0]
	if k.Kty != "EC" || k.Crv != "P-256" || k.Kid != identidadKid || k.Use != "sig" || k.Alg != "ES256" {
		t.Fatalf("cabecera de la clave del JWKS: %+v", k)
	}
	punto, err := id.clave.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("serializando la pública: %v", err)
	}
	probarCoordenada(t, "x", k.X, punto[1:33])
	probarCoordenada(t, "y", k.Y, punto[33:65])
	if otra, _, _ := descargarJWKS(t, id.servidor.URL+"/otra"); otra != http.StatusNotFound {
		t.Errorf("GET /otra: código %d, se esperaba 404", otra)
	}
}

// probarCoordenada comprueba una coordenada del JWKS: base64url sin padding que decodifica a
// exactamente 32 bytes y es igual a la esperada (la de la clave pública).
func probarCoordenada(t *testing.T, nombre, valor string, quiere []byte) {
	t.Helper()
	if strings.Contains(valor, "=") {
		t.Errorf("%s lleva padding: %q", nombre, valor)
	}
	crudo, err := base64.RawURLEncoding.DecodeString(valor)
	if err != nil {
		t.Fatalf("%s no es base64url sin padding: %v", nombre, err)
	}
	if len(crudo) != 32 {
		t.Errorf("%s mide %d bytes, se esperaban 32 exactos", nombre, len(crudo))
	}
	if !slices.Equal(crudo, quiere) {
		t.Errorf("%s no coincide con la clave pública", nombre)
	}
}

// probarTokenDe verifica, para los tres systems, que el token de TokenDe lo acepta el verificador
// del JWKS con los claims esperados y que su header lleva alg y kid.
func probarTokenDe(t *testing.T, id *identidad, mv *jwt.MultiVerifier, usuario string) {
	t.Helper()
	for _, system := range []string{"wapp.bff", "wapp.edge", "wapp.platform"} {
		token := id.TokenDe(usuario, system)
		cabecera := segmentoJWT(t, token, 0)
		if cabecera["alg"] != "ES256" || cabecera["kid"] != identidadKid {
			t.Errorf("%s: header %v, se esperaba alg=ES256 y kid=%s", system, cabecera, identidadKid)
		}
		claims, err := mv.ValidateIdentityToken(token, system)
		if err != nil {
			t.Errorf("%s: el token de TokenDe no verifica: %v", system, err)
			continue
		}
		if claims.Issuer != identidadEmisor || claims.Subject != usuario || claims.System != system ||
			claims.Email != usuario+"@procesos.test" || claims.TokenUse != jwt.TokenUseIdentity ||
			!slices.Equal(claims.Audience, []string{system}) || claims.ID == "" {
			t.Errorf("%s: claims inesperados: %+v", system, claims)
		}
		if claims.ExpiresAt == nil || time.Until(claims.ExpiresAt.Time) < 50*time.Minute {
			t.Errorf("%s: exp %v, se esperaba ~1 h de vigencia", system, claims.ExpiresAt)
		}
	}
}

// probarTokenCaducado comprueba que el token caducado tiene exp < now, el resto de claims
// iguales a los de TokenDe y que el verificador lo rechaza como caducado (ErrTokenExpired): si
// además hubiera un defecto de firma o de issuer, se clasificaría como inválido y no como
// caducado.
func probarTokenCaducado(t *testing.T, id *identidad, mv *jwt.MultiVerifier, usuario string) {
	t.Helper()
	const system = "wapp.bff"
	token := id.TokenCaducado(usuario, system)
	carga := segmentoJWT(t, token, 1)
	exp, ok := carga["exp"].(float64)
	if !ok || exp >= float64(time.Now().Unix()) {
		t.Fatalf("exp = %v: se esperaba un instante pasado", carga["exp"])
	}
	if carga["iss"] != identidadEmisor || carga["sub"] != usuario || carga["system"] != system ||
		carga["token_use"] != "identity" || carga["email"] != usuario+"@procesos.test" {
		t.Errorf("claims del caducado: %v", carga)
	}
	if cabecera := segmentoJWT(t, token, 0); cabecera["alg"] != "ES256" || cabecera["kid"] != identidadKid {
		t.Errorf("header del caducado: %v", cabecera)
	}
	if _, err := mv.ValidateIdentityToken(token, system); !errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("el caducado debería dar ErrTokenExpired (firma e issuer buenos) y dio: %v", err)
	}
}

// probarTokenOtroEmisor comprueba que el token de otro emisor lleva un iss distinto de
// identity-core y que el verificador lo rechaza como inválido (no como caducado): la firma es
// buena, así que el único motivo es el issuer.
func probarTokenOtroEmisor(t *testing.T, id *identidad, mv *jwt.MultiVerifier, usuario string) {
	t.Helper()
	const system = "wapp.platform"
	token := id.TokenDeOtroEmisor(usuario, system)
	if iss := segmentoJWT(t, token, 1)["iss"]; iss == identidadEmisor || iss == nil || iss == "" {
		t.Fatalf("iss = %v: se esperaba uno distinto de %s", iss, identidadEmisor)
	}
	_, err := mv.ValidateIdentityToken(token, system)
	if !errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("el de otro emisor debería dar ErrInvalidToken y dio: %v", err)
	}
	if !strings.Contains(err.Error(), "issuer") {
		t.Errorf("el rechazo debería citar el issuer: %v", err)
	}
}
