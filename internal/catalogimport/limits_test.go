package catalogimport_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/config"
)

// errSeLeyóDeMás es el veneno del lector: si ReadLimited pide un byte más allá del
// techo, el test falla POR AQUÍ y no por una comparación indirecta.
var errSeLeyóDeMás = errors.New("se leyó el cuerpo más allá del techo: eso es exactamente lo que el límite prohíbe")

// lectorVenenoso entrega un documento y, agotado este, espacios sin fin —espacios
// porque un JSON seguido de espacios SIGUE SIENDO JSON válido: si el validador
// leyera de más y deserializara igual, obtendría un documento impecable y el test
// lo vería, en vez de confundir el rechazo por tamaño con un rechazo por sintaxis.
//
// Lleva la cuenta de lo leído y envenena la lectura pasado tope.
type lectorVenenoso struct {
	cuerpo []byte
	leídos int
	tope   int
}

func (l *lectorVenenoso) Read(p []byte) (int, error) {
	if l.leídos >= l.tope {
		return 0, errSeLeyóDeMás
	}
	n := min(len(p), l.tope-l.leídos)
	for i := range n {
		pos := l.leídos + i
		if pos < len(l.cuerpo) {
			p[i] = l.cuerpo[pos]
			continue
		}
		p[i] = ' '
	}
	l.leídos += n
	return n, nil
}

// documentoDe fabrica un documento VÁLIDO de exactamente n bytes, rellenando la
// descripción de su único artículo. Que sea válido es el punto: lo único que puede
// hacerlo fallar es su tamaño.
func documentoDe(t *testing.T, n int) []byte {
	t.Helper()
	const plantilla = `{"format":"wapp.catalog_import","version":1,"catalog":{"categories":[` +
		`{"code":"1","label":"Bebidas","items":[{"code":"1","sku":"CAFE","label":"Café","price":2500,"description":"%s"}]}]}}`
	base := len(plantilla) - len("%s")
	if n < base {
		t.Fatalf("no se puede fabricar un documento de %d bytes: el mínimo es %d", n, base)
	}
	doc := []byte(strings.Replace(plantilla, "%s", strings.Repeat("a", n-base), 1))
	if len(doc) != n {
		t.Fatalf("el documento fabricado mide %d bytes y se pedían %d", len(doc), n)
	}
	if _, verr := catalogimport.Validate(doc, catalogimport.DefaultLimits()); verr != nil {
		t.Fatalf("el documento fabricado debe ser válido; devolvió %v", verr)
	}
	return doc
}

// TestReadLimited_UnByteDeMás_RechazaSinDeserializar es el criterio de T3.1: el
// cuerpo se corta por tamaño ANTES de que encoding/json vea nada.
//
// La prueba se sostiene sobre tres patas, porque "hay un límite" no demuestra nada:
//
//  1. el cuerpo son 1 MiB+1 bytes de un documento IMPECABLE seguidos de un mar de
//     espacios: si se hubiera deserializado, habría pasado la validación y no
//     habría error de tamaño;
//  2. el lector lleva la cuenta y solo se le sacaron 1 MiB+1 bytes — el resto del
//     cuerpo, ilimitado, jamás se materializó en memoria;
//  3. leer un byte más habría devuelto errSeLeyóDeMás, un error distinto y
//     reconocible, en vez de ErrDocumentTooLarge.
func TestReadLimited_UnByteDeMás_RechazaSinDeserializar(t *testing.T) {
	límites := catalogimport.DefaultLimits()
	techo := int(límites.MaxJSONBytes)

	cuerpo := documentoDe(t, techo+1) // válido, pero un byte por encima del techo
	lector := &lectorVenenoso{cuerpo: cuerpo, tope: techo + 1}

	body, err := catalogimport.ReadLimited(lector, límites)
	if !errors.Is(err, catalogimport.ErrDocumentTooLarge) {
		t.Fatalf("se esperaba ErrDocumentTooLarge; llegó %v", err)
	}
	if body != nil {
		t.Errorf("un cuerpo rechazado no se devuelve; llegaron %d bytes", len(body))
	}
	if lector.leídos != techo+1 {
		t.Errorf("se leyeron %d bytes; el techo permite leer exactamente %d (uno más que el máximo, para saber que se pasó)", lector.leídos, techo+1)
	}
}

// TestReadLimited_JustoEnElTecho_Pasa: el byte de más es lo que separa "cabe" de
// "se pasó". Un documento de exactamente el máximo entra y valida.
func TestReadLimited_JustoEnElTecho_Pasa(t *testing.T) {
	límites := catalogimport.DefaultLimits()
	techo := int(límites.MaxJSONBytes)

	cuerpo := documentoDe(t, techo)
	body, err := catalogimport.ReadLimited(bytes.NewReader(cuerpo), límites)
	if err != nil {
		t.Fatalf("un documento de exactamente %d bytes debe pasar: %v", techo, err)
	}
	if len(body) != techo {
		t.Fatalf("se leyeron %d bytes de %d", len(body), techo)
	}
	if _, verr := catalogimport.Validate(body, límites); verr != nil {
		t.Fatalf("el documento leído debe validar: %v", verr)
	}
}

// TestReadLimited_TechoPropio respeta un límite configurado más bajo que el
// default: es lo que hace WAPP_IMPORT_MAX_JSON_BYTES útil.
func TestReadLimited_TechoPropio(t *testing.T) {
	límites := catalogimport.Limits{MaxJSONBytes: 64, MaxItems: 10}
	if _, err := catalogimport.ReadLimited(strings.NewReader(strings.Repeat("x", 65)), límites); !errors.Is(err, catalogimport.ErrDocumentTooLarge) {
		t.Fatalf("con el techo en 64 bytes, 65 debe rechazarse; llegó %v", err)
	}
	if _, err := catalogimport.ReadLimited(strings.NewReader(strings.Repeat("x", 64)), límites); err != nil {
		t.Fatalf("64 bytes con el techo en 64 debe pasar: %v", err)
	}
}

// TestLimits_NoPositivosCaenAlDefault: una configuración a 0 —o negativa— no
// DESACTIVA el tope. Es la misma regla que el resto de la configuración del repo, y
// aquí es de seguridad: un WAPP_IMPORT_MAX_JSON_BYTES mal escrito abriría la puerta
// a subir cualquier cosa.
func TestLimits_NoPositivosCaenAlDefault(t *testing.T) {
	for _, límites := range []catalogimport.Limits{{}, {MaxJSONBytes: -1, MaxItems: -1}} {
		if _, err := catalogimport.ReadLimited(strings.NewReader(strings.Repeat("x", int(catalogimport.DefaultMaxJSONBytes)+1)), límites); !errors.Is(err, catalogimport.ErrDocumentTooLarge) {
			t.Errorf("con %+v el techo debe caer al default y rechazar; llegó %v", límites, err)
		}
	}
}

// TestDefaultsDelValidadorYDeLaConfiguraciónCoinciden amarra las dos parejas de
// números. Los topes se declaran en dos sitios por fuerza —la configuración los lee
// del entorno, el validador los aplica— y una divergencia silenciosa haría que el
// .env.example documentara un límite y el código impusiera otro.
//
// Las variables se ponen a "0" a propósito: fuerza el camino del default sin
// depender de lo que tenga el entorno de quien corre el test, y de paso comprueba
// que un valor no positivo del entorno no desactiva el tope.
func TestDefaultsDelValidadorYDeLaConfiguraciónCoinciden(t *testing.T) {
	t.Setenv("WAPP_IMPORT_MAX_JSON_BYTES", "0")
	t.Setenv("WAPP_IMPORT_MAX_ITEMS", "0")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("no se pudo cargar la configuración: %v", err)
	}
	if cfg.Import.MaxJSONBytes != catalogimport.DefaultMaxJSONBytes {
		t.Errorf("config dice %d bytes y el validador %d", cfg.Import.MaxJSONBytes, catalogimport.DefaultMaxJSONBytes)
	}
	if cfg.Import.MaxItems != catalogimport.DefaultMaxItems {
		t.Errorf("config dice %d artículos y el validador %d", cfg.Import.MaxItems, catalogimport.DefaultMaxItems)
	}
}
