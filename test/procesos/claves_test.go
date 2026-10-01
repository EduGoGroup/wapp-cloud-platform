//go:build integracion

package procesos

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/EduGoGroup/wapp-shared/envelope"
)

// clavesJWTKid es el kid de la clave ES256 de firma de los context tokens del servidor.
const clavesJWTKid = "procesos-1"

// clavesTamano es el tamaño en bytes de cada secreto simétrico (KEK maestra e indexKey, AES-256 y
// HMAC-SHA256) y de la semilla Ed25519.
const clavesTamano = 32

// claves es el material criptográfico efímero de un servidor de prueba: la clave de firma del lease
// (kill-switch anti-clon), el par X25519 de cifrado de tránsito de la nube, las dos claves del
// envelope de PII de negocio (KEK e indexKey; ojo, es la "DEK" homónima del código de este repo, no
// la del almacén de whatsmeow) y la clave ES256 con la que el servidor firma sus context tokens.
// Nace de nuevasClaves, es aleatoria por llamada y NO existe en el repositorio.
type claves struct {
	LeaseSeedB64 string            // semilla Ed25519 (32 B), base64 estándar: WAPP_LEASE_PRIVATE_KEY_B64
	LeasePub     ed25519.PublicKey // su pública, para validar leases desde el test

	NubePrivB64 string // privada X25519 (32 B), base64 estándar: WAPP_CLOUD_ENC_PRIVKEY_B64
	NubePriv    []byte // la misma privada en crudo, para abrir lo que el Edge sella hacia la nube
	NubePub     []byte // la pública de la nube, la que el Edge usa con envelope.SealFor

	KEKMasterB64 string // KEK maestra (32 B), base64 estándar: WAPP_KEK_MASTER_B64
	KEKIndexB64  string // indexKey del índice ciego (32 B), base64 estándar: WAPP_KEK_INDEX_B64

	JWTKeyFile string // PEM PKCS#8 de una clave EC P-256, modo 0600: WAPP_JWT_EC_PRIVATE_KEY_FILE
	JWTKid     string // kid de esa clave: WAPP_JWT_KID
}

// nuevasClaves genera todo el material de claves de un servidor de prueba: un par Ed25519 (se
// entrega la semilla de 32 B en base64 estándar, que es lo que WAPP_LEASE_PRIVATE_KEY_B64
// acepta), un par X25519 con envelope.GenerateKeyPair (la privada en base64 estándar), dos
// secretos de 32 B aleatorios para la KEK maestra y la indexKey (base64 estándar) y una clave ES256
// P-256 en PEM PKCS#8 escrita con modo 0600 en un t.TempDir() (kid "procesos-1"). Falla el test
// (t.Fatalf) si algo no se puede generar o escribir; el directorio se borra solo al terminar.
func nuevasClaves(t *testing.T) claves {
	t.Helper()
	leasePub, leasePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generar la clave Ed25519 del lease: %v", err)
	}
	nubePub, nubePriv, err := envelope.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generar el par X25519 de la nube: %v", err)
	}

	jwtClave, err := x509.MarshalPKCS8PrivateKey(pkiClave(t))
	if err != nil {
		t.Fatalf("serializar la clave ES256 (PKCS#8): %v", err)
	}
	jwtFichero := filepath.Join(t.TempDir(), "jwt-es256.pem")
	pkiEscribir(t, jwtFichero, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: jwtClave}), pkiModoClave)

	return claves{
		LeaseSeedB64: base64.StdEncoding.EncodeToString(leasePriv.Seed()),
		LeasePub:     leasePub,
		NubePrivB64:  base64.StdEncoding.EncodeToString(nubePriv),
		NubePriv:     nubePriv,
		NubePub:      nubePub,
		KEKMasterB64: clavesSecretoB64(t),
		KEKIndexB64:  clavesSecretoB64(t),
		JWTKeyFile:   jwtFichero,
		JWTKid:       clavesJWTKid,
	}
}

// Entorno devuelve las siete variables "K=V" que le dan este material al servidor:
// WAPP_LEASE_PRIVATE_KEY_B64, WAPP_CLOUD_ENC_PRIVKEY_B64, WAPP_KEK_PROVIDER (siempre env: las
// KEK viajan en el entorno, camino de desarrollo), WAPP_KEK_MASTER_B64, WAPP_KEK_INDEX_B64,
// WAPP_JWT_EC_PRIVATE_KEY_FILE y WAPP_JWT_KID. Cada llamada devuelve un slice nuevo.
func (c claves) Entorno() []string {
	return []string{
		"WAPP_LEASE_PRIVATE_KEY_B64=" + c.LeaseSeedB64,
		"WAPP_CLOUD_ENC_PRIVKEY_B64=" + c.NubePrivB64,
		"WAPP_KEK_PROVIDER=env",
		"WAPP_KEK_MASTER_B64=" + c.KEKMasterB64,
		"WAPP_KEK_INDEX_B64=" + c.KEKIndexB64,
		"WAPP_JWT_EC_PRIVATE_KEY_FILE=" + c.JWTKeyFile,
		"WAPP_JWT_KID=" + c.JWTKid,
	}
}

// clavesSecretoB64 devuelve 32 bytes aleatorios en base64 estándar (con relleno). Falla el test
// si el generador criptográfico no responde.
func clavesSecretoB64(t *testing.T) string {
	t.Helper()
	b := make([]byte, clavesTamano)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("leer aleatoriedad del sistema: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// clavesDecodificar decodifica base64 estándar (con relleno, como lo hace el servidor) y exige
// exactamente n bytes; falla el test si no.
func clavesDecodificar(t *testing.T, nombre, b64 string, n int) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("%s: no es base64 estándar: %v", nombre, err)
	}
	if len(b) != n {
		t.Fatalf("%s mide %d bytes, esperaba %d", nombre, len(b), n)
	}
	return b
}

// TestArnes_Claves comprueba que el material generado es el que el servidor sabe leer (formatos
// y tamaños de cada variable), que las parejas pública/privada se corresponden, que el sellado de
// la nube hace ida y vuelta con el envelope real, que el fichero JWT es una clave P-256 de modo
// 0600, y que cada llamada produce secretos nuevos. No necesita Docker.
func TestArnes_Claves(t *testing.T) {
	c := nuevasClaves(t)
	t.Run("lease", func(t *testing.T) { clavesVerificarLease(t, c) })
	t.Run("nube", func(t *testing.T) { clavesVerificarNube(t, c) })
	t.Run("sellado", func(t *testing.T) { clavesVerificarSellado(t, c) })
	t.Run("kek", func(t *testing.T) { clavesVerificarKEK(t, c) })
	t.Run("jwt", func(t *testing.T) { clavesVerificarJWT(t, c) })
	t.Run("entorno", func(t *testing.T) { clavesVerificarEntorno(t, c) })
	t.Run("aleatorias", func(t *testing.T) { clavesVerificarAleatorias(t, c) })
}

// clavesVerificarLease comprueba que la semilla se decodifica a 32 B, que ed25519.NewKeyFromSeed
// la acepta (como lease.ParsePrivateKeyBase64) y deriva LeasePub, y que firma y verifica.
func clavesVerificarLease(t *testing.T, c claves) {
	t.Helper()
	semilla := clavesDecodificar(t, "LeaseSeedB64", c.LeaseSeedB64, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(semilla)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("la pública derivada no es ed25519.PublicKey (%T)", priv.Public())
	}
	if !pub.Equal(c.LeasePub) {
		t.Error("LeasePub no es la pública de la semilla")
	}
	mensaje := []byte("lease de prueba")
	if !ed25519.Verify(c.LeasePub, mensaje, ed25519.Sign(priv, mensaje)) {
		t.Error("una firma hecha con la semilla no verifica con LeasePub")
	}
}

// clavesVerificarNube comprueba que NubePriv/NubePub miden 32 B, que NubePrivB64 es NubePriv en
// base64 estándar y que NubePub es la pública X25519 de esa privada (la que el servidor deriva
// con curve25519.X25519 y publica al Edge en el enrolamiento).
func clavesVerificarNube(t *testing.T, c claves) {
	t.Helper()
	priv := clavesDecodificar(t, "NubePrivB64", c.NubePrivB64, envelope.PrivateKeySize)
	if string(priv) != string(c.NubePriv) {
		t.Error("NubePrivB64 no es NubePriv en base64")
	}
	if len(c.NubePub) != envelope.PublicKeySize {
		t.Fatalf("NubePub mide %d bytes, esperaba %d", len(c.NubePub), envelope.PublicKeySize)
	}
	clave, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		t.Fatalf("la privada de la nube no es una clave X25519: %v", err)
	}
	if string(clave.PublicKey().Bytes()) != string(c.NubePub) {
		t.Error("NubePub no es la pública X25519 de NubePriv")
	}
}

// clavesVerificarSellado comprueba la ida y vuelta envelope.SealFor(NubePub) / OpenWith(NubePriv)
// y que otra privada no abre el sellado.
func clavesVerificarSellado(t *testing.T, c claves) {
	t.Helper()
	mensaje := []byte("payload que el Edge sella hacia la nube")
	sellado, err := envelope.SealFor(c.NubePub, mensaje)
	if err != nil {
		t.Fatalf("SealFor: %v", err)
	}
	abierto, err := envelope.OpenWith(c.NubePriv, sellado)
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	if string(abierto) != string(mensaje) {
		t.Errorf("OpenWith devolvió %q, esperaba %q", abierto, mensaje)
	}
	_, otraPriv, err := envelope.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	if _, err := envelope.OpenWith(otraPriv, sellado); !errors.Is(err, envelope.ErrOpenFailed) {
		t.Errorf("OpenWith con otra privada: err = %v, esperaba envelope.ErrOpenFailed", err)
	}
}

// clavesVerificarKEK comprueba que la KEK maestra y la indexKey miden 32 B en base64 estándar y que
// son distintas entre sí y de la semilla del lease y de la privada de la nube.
func clavesVerificarKEK(t *testing.T, c claves) {
	t.Helper()
	master := clavesDecodificar(t, "KEKMasterB64", c.KEKMasterB64, clavesTamano)
	index := clavesDecodificar(t, "KEKIndexB64", c.KEKIndexB64, clavesTamano)
	if string(master) == string(index) {
		t.Error("la KEK maestra y la indexKey son iguales")
	}
	semilla := clavesDecodificar(t, "LeaseSeedB64", c.LeaseSeedB64, clavesTamano)
	for nombre, otro := range map[string][]byte{"semilla del lease": semilla, "privada de la nube": c.NubePriv} {
		if string(master) == string(otro) || string(index) == string(otro) {
			t.Errorf("una clave del envelope coincide con la %s", nombre)
		}
	}
}

// clavesVerificarJWT comprueba el fichero de la clave ES256: modo exacto 0600 (el servidor en
// prod exige ≤ 0600), PEM PKCS#8 con una clave ECDSA P-256 (la parseable como en
// parseECP256PrivateKeyPEM), que firme y verifique ES256, y el kid.
func clavesVerificarJWT(t *testing.T, c claves) {
	t.Helper()
	info, err := os.Stat(c.JWTKeyFile)
	if err != nil {
		t.Fatalf("stat %s: %v", c.JWTKeyFile, err)
	}
	if got := info.Mode().Perm(); got != pkiModoClave {
		t.Errorf("modo del fichero JWT = %#o, esperaba %#o", got, pkiModoClave)
	}
	if !filepath.IsAbs(c.JWTKeyFile) {
		t.Errorf("JWTKeyFile %q debe ser una ruta absoluta", c.JWTKeyFile)
	}
	bloque, _ := pem.Decode(pkiLeerPEM(t, c.JWTKeyFile))
	if bloque == nil || bloque.Type != "PRIVATE KEY" {
		t.Fatal("el fichero JWT debe contener un bloque PEM PRIVATE KEY (PKCS#8)")
	}
	parseada, err := x509.ParsePKCS8PrivateKey(bloque.Bytes)
	if err != nil {
		t.Fatalf("el fichero JWT no es PKCS#8: %v", err)
	}
	clave, ok := parseada.(*ecdsa.PrivateKey)
	if !ok || clave.Curve != elliptic.P256() {
		t.Fatalf("la clave JWT debe ser ECDSA P-256 (ES256), es %T", parseada)
	}
	resumen := sha256.Sum256([]byte("header.payload"))
	firma, err := ecdsa.SignASN1(rand.Reader, clave, resumen[:])
	if err != nil {
		t.Fatalf("firmar ES256: %v", err)
	}
	if !ecdsa.VerifyASN1(&clave.PublicKey, resumen[:], firma) {
		t.Error("la firma ES256 no verifica con la pública de la clave JWT")
	}
	if c.JWTKid != clavesJWTKid {
		t.Errorf("JWTKid = %q, esperaba %q", c.JWTKid, clavesJWTKid)
	}
}

// clavesVerificarEntorno comprueba que Entorno() trae exactamente las siete variables, sin
// duplicados, con los valores del struct y WAPP_KEK_PROVIDER=env.
func clavesVerificarEntorno(t *testing.T, c claves) {
	t.Helper()
	env := entornoAMapa(t, c.Entorno())
	esperado := map[string]string{
		"WAPP_LEASE_PRIVATE_KEY_B64":   c.LeaseSeedB64,
		"WAPP_CLOUD_ENC_PRIVKEY_B64":   c.NubePrivB64,
		"WAPP_KEK_PROVIDER":            "env",
		"WAPP_KEK_MASTER_B64":          c.KEKMasterB64,
		"WAPP_KEK_INDEX_B64":           c.KEKIndexB64,
		"WAPP_JWT_EC_PRIVATE_KEY_FILE": c.JWTKeyFile,
		"WAPP_JWT_KID":                 c.JWTKid,
	}
	if len(env) != len(esperado) {
		t.Errorf("Entorno() trae %d variables, esperaba %d: %v", len(env), len(esperado), env)
	}
	for k, v := range esperado {
		if env[k] != v {
			t.Errorf("%s = %q, esperaba %q", k, env[k], v)
		}
	}
}

// clavesVerificarAleatorias comprueba que una segunda llamada a nuevasClaves no repite ningún
// secreto ni el fichero JWT (ni su ruta ni su contenido).
func clavesVerificarAleatorias(t *testing.T, c claves) {
	t.Helper()
	otras := nuevasClaves(t)
	secretos := map[string][2]string{
		"semilla del lease":  {c.LeaseSeedB64, otras.LeaseSeedB64},
		"privada de la nube": {c.NubePrivB64, otras.NubePrivB64},
		"KEK maestra":        {c.KEKMasterB64, otras.KEKMasterB64},
		"indexKey":           {c.KEKIndexB64, otras.KEKIndexB64},
		"ruta del JWT":       {c.JWTKeyFile, otras.JWTKeyFile},
		"clave JWT":          {string(pkiLeerPEM(t, c.JWTKeyFile)), string(pkiLeerPEM(t, otras.JWTKeyFile))},
	}
	for nombre, par := range secretos {
		if par[0] == par[1] {
			t.Errorf("dos llamadas a nuevasClaves repiten la %s", nombre)
		}
	}
}
