package indice_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/indice"
)

// cache_test.go cubre el constructor, el conteo, la huella y el
// adaptador. Los errores están en cache_errors_test.go; la invalidación y el
// aislamiento por tenant, en cache_invalidation_test.go; el desalojo y la
// concurrencia, en cache_eviction_test.go (E-13).

// ---------------------------------------------------------------------------
// LOS DOCUMENTOS DEL TENANT (los mismos bytes que guardaría tenant_content)
// ---------------------------------------------------------------------------

const docV1 = `{"categories":[
  {"code":"1","label":"Bebidas","items":[
    {"code":"1","sku":"CAFE","label":"Café","price":2.50,"tags":["caliente"]},
    {"code":"2","sku":"TE","label":"Té","price":2.00}
  ]}
]}`

// docV2 añade un artículo: es el catálogo después de un import.
const docV2 = `{"categories":[
  {"code":"1","label":"Bebidas","items":[
    {"code":"1","sku":"CAFE","label":"Café","price":2.50,"tags":["caliente"]},
    {"code":"2","sku":"TE","label":"Té","price":2.00},
    {"code":"3","sku":"CAFE-DESC","label":"Café descafeinado","price":2.70}
  ]}
]}`

// docV1PriceTouched es docV1 con UN DÍGITO cambiado: 2.50 → 2.90. Mismo número de
// bytes, misma forma, mismo todo salvo el precio.
//
// 🔴 Este es el documento que separa una huella de verdad de un resumen barato
// (T-7): una invalidación por longitud, o por «los primeros N bytes», daría los dos
// por iguales y el pipeline seguiría cotizando el precio viejo indefinidamente.
const docV1PriceTouched = `{"categories":[
  {"code":"1","label":"Bebidas","items":[
    {"code":"1","sku":"CAFE","label":"Café","price":2.90,"tags":["caliente"]},
    {"code":"2","sku":"TE","label":"Té","price":2.00}
  ]}
]}`

// ---------------------------------------------------------------------------
// LA FUENTE FALSA — LA QUE CUENTA LAS LECTURAS
// ---------------------------------------------------------------------------

// ctxKey marca el contexto de un test para comprobar que llega a la Fuente.
type ctxKey struct{}

// fakeSource sirve un documento en memoria y CUENTA cuántas veces se lo piden. Es
// el contador con el que se demuestra que las búsquedas por ítem no leen.
type fakeSource struct {
	mu      sync.Mutex
	docs    map[string]indice.Documento
	reads   int
	err     error
	lastCtx context.Context
	// gate, si no es nil, se llama DENTRO de la lectura y fuera del candado de la
	// fuente: los tests de concurrencia retienen ahí a quien lee.
	gate func()
}

func newFakeSource() *fakeSource {
	return &fakeSource{docs: map[string]indice.Documento{}}
}

// publish simula una escritura del documento del tenant. Es la MISMA operación
// para los dos caminos —el import y el `PUT` a mano— y eso no es una
// simplificación del test: es lo que la caché ve. Ninguno de los dos deja marca de
// versión en `tenant_content`, así que desde aquí abajo son indistinguibles.
func (f *fakeSource) publish(tenantID, doc string) {
	f.publishStamped(tenantID, doc, time.Time{})
}

func (f *fakeSource) publishStamped(tenantID, doc string, stamp time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs[tenantID] = indice.Documento{Raw: []byte(doc), Sello: stamp}
}

func (f *fakeSource) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeSource) LeerCatalogo(ctx context.Context, tenantID string) (indice.Documento, error) {
	if f.gate != nil {
		f.gate()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	f.lastCtx = ctx
	if f.err != nil {
		return indice.Documento{}, f.err
	}
	doc, ok := f.docs[tenantID]
	if !ok {
		return indice.Documento{}, errors.New("fakeSource: sin documento para " + tenantID)
	}
	return doc, nil
}

func (f *fakeSource) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// newCache construye una caché con el normalizador de producción y el tope dado.
func newCache(t *testing.T, f indice.Fuente, limit int) *indice.Cache {
	t.Helper()
	c, err := indice.NewCache(f, textmatch.Normalize, limit)
	if err != nil {
		t.Fatalf("NewCache = %v; se esperaba una caché", err)
	}
	return c
}

// get es Obtener que falla el test si hay error.
func get(t *testing.T, c *indice.Cache, tenantID string) *indice.Indice {
	t.Helper()
	idx, err := c.Obtener(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("Obtener(%q) = %v; se esperaba un índice", tenantID, err)
	}
	if idx == nil {
		t.Fatalf("Obtener(%q) = nil sin error", tenantID)
	}
	return idx
}

// assertStats compara los tres contadores de una vez: se leen juntos.
func assertStats(t *testing.T, c *indice.Cache, want indice.Estadisticas, why string) {
	t.Helper()
	if got := c.Estadisticas(); got != want {
		t.Errorf("Estadisticas() = %+v; se esperaba %+v — %s", got, want, why)
	}
}

// ---------------------------------------------------------------------------
// LITERALES Y CONSTRUCTOR
// ---------------------------------------------------------------------------

func TestCache_Literals(t *testing.T) {
	if indice.RefCatalogo != "catalogo" {
		t.Errorf("RefCatalogo = %q; la ref del catálogo en tenant_content es \"catalogo\"", indice.RefCatalogo)
	}
	if indice.MaxTenantsEnCache != 64 {
		t.Errorf("MaxTenantsEnCache = %d; el tope es 64", indice.MaxTenantsEnCache)
	}
	want := "catalogo: la caché necesita una Fuente de la que leer el documento"
	if indice.ErrSinFuente.Error() != want {
		t.Errorf("ErrSinFuente = %q; el texto es literal: %q", indice.ErrSinFuente.Error(), want)
	}
}

func TestNewCache_RequiresSourceAndValidNormalizer(t *testing.T) {
	cases := []struct {
		name       string
		source     indice.Fuente
		normalizer indice.Normalizador
		want       error
	}{
		{"no source", nil, textmatch.Normalize, indice.ErrSinFuente},
		{"no source is checked first", nil, nil, indice.ErrSinFuente},
		{"no normalizer", newFakeSource(), nil, indice.ErrSinNormalizador},
		{"normalizer that folds the enye", newFakeSource(), enyeFoldingNormalizer, indice.ErrNormalizadorInvalido},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cache, err := indice.NewCache(c.source, c.normalizer, 0)
			if !errors.Is(err, c.want) {
				t.Errorf("NewCache = %v; se esperaba %v", err, c.want)
			}
			if cache != nil {
				t.Errorf("NewCache devolvió una caché junto al error; no nace a medias")
			}
		})
	}
}

func TestNewCache_StartsEmpty(t *testing.T) {
	c := newCache(t, newFakeSource(), 0)
	if c.Tamano() != 0 {
		t.Errorf("Tamano() = %d; una caché nueva está vacía", c.Tamano())
	}
	assertStats(t, c, indice.Estadisticas{}, "una caché nueva no ha contado nada")
}

// ---------------------------------------------------------------------------
// CONTEO: UNA CONSTRUCCIÓN, CERO LECTURAS ADICIONALES
// ---------------------------------------------------------------------------

// TestObtener_OneJobOfNItems_OneReadOneBuild.
//
// 🔴 Se afirma el CONTADOR Y EL ESTADO, no solo el contador. Un contador dice lo
// que cuenta: que las lecturas valgan 1 no prueba que los 10 ítems miraran el
// MISMO índice. Por eso el test comprueba además que los 10 ítems obtienen la
// respuesta correcta.
func TestObtener_OneJobOfNItems_OneReadOneBuild(t *testing.T) {
	f := newFakeSource()
	f.publish("t1", docV1)
	c := newCache(t, f, 0)

	ctx := context.WithValue(context.Background(), ctxKey{}, "job-1")
	idx, err := c.Obtener(ctx, "t1")
	if err != nil {
		t.Fatalf("Obtener(t1) = %v", err)
	}
	if f.readCount() != 1 {
		t.Errorf("lecturas = %d; el job lee el documento UNA vez", f.readCount())
	}
	if f.lastCtx.Value(ctxKey{}) != "job-1" {
		t.Errorf("la Fuente no recibió el contexto del job")
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 1}, "el primer job construye")
	if c.Tamano() != 1 {
		t.Errorf("Tamano() = %d; se esperaba 1", c.Tamano())
	}

	// Los N ítems del pedido. El tope es 10 (D-044.39); se usan 10.
	for i := range 10 {
		got, ok := idx.PorSKU("CAFE")
		if !ok || got.Articulo.Price != 2.5 {
			t.Errorf("ítem %d: PorSKU(CAFE) = %+v, %v; se esperaba el café a 2.5", i, got, ok)
		}
		if n := len(idx.PorEtiqueta("cafe")); n != 1 {
			t.Errorf("ítem %d: PorEtiqueta(cafe) = %d coincidencias; se esperaba 1", i, n)
		}
		if n := len(idx.PorTag("caliente")); n != 1 {
			t.Errorf("ítem %d: PorTag(caliente) = %d coincidencias; se esperaba 1", i, n)
		}
	}

	if f.readCount() != 1 {
		t.Errorf("lecturas = %d tras 10 ítems; 🔴 a partir del segundo ítem, CERO lecturas: el *Indice no tiene con qué leer", f.readCount())
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 1}, "una sola construcción para los 10 ítems")
}

// TestObtener_SameContent_NoRebuild: el segundo job SÍ lee —hay que hashear para
// saber si el catálogo cambió, y no hay versión que preguntar (D-044.44)— pero no
// vuelve a indexar. Y devuelve EL MISMO índice, que es el estado que el contador
// por sí solo no probaría. Seis jobs son seis lecturas —no doce—: la huella sale
// de los bytes ya leídos.
func TestObtener_SameContent_NoRebuild(t *testing.T) {
	f := newFakeSource()
	f.publish("t1", docV1)
	c := newCache(t, f, 0)

	first := get(t, c, "t1")
	for range 5 {
		if other := get(t, c, "t1"); other != first {
			t.Fatalf("el mismo contenido devolvió OTRO índice; tiene que ser el mismo puntero, no uno equivalente")
		}
	}

	assertStats(t, c, indice.Estadisticas{Construcciones: 1, Aciertos: 5}, "una construcción y cinco aciertos en seis jobs")
	if f.readCount() != 6 {
		t.Errorf("lecturas = %d; son una por job y ninguna más: es el precio de invalidar por contenido", f.readCount())
	}
}

// ---------------------------------------------------------------------------
// LA HUELLA (T-7)
// ---------------------------------------------------------------------------

// TestObtener_FingerprintIsSHA256OfTheBytesRead: la procedencia del índice es el
// SHA-256 en hexadecimal de los bytes que sirvió la Fuente, y el sello, el suyo.
func TestObtener_FingerprintIsSHA256OfTheBytesRead(t *testing.T) {
	stamp := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	f := newFakeSource()
	f.publishStamped("t1", docV1, stamp)
	c := newCache(t, f, 0)

	idx := get(t, c, "t1")
	sum := sha256.Sum256([]byte(docV1))
	if want := hex.EncodeToString(sum[:]); idx.Hash() != want {
		t.Errorf("Hash() = %q; se esperaba el SHA-256 hex del documento, %q", idx.Hash(), want)
	}
	if !idx.Sello().Equal(stamp) {
		t.Errorf("Sello() = %v; se esperaba el del documento, %v", idx.Sello(), stamp)
	}
	if f.readCount() != 1 {
		t.Errorf("lecturas = %d; la huella no cuesta una lectura extra", f.readCount())
	}

	f.publish("t1", docV1PriceTouched)
	touched := get(t, c, "t1")
	if touched.Hash() == idx.Hash() {
		t.Errorf("dos documentos que difieren en un dígito tienen la misma huella %q", idx.Hash())
	}
	sum = sha256.Sum256([]byte(docV1PriceTouched))
	if want := hex.EncodeToString(sum[:]); touched.Hash() != want {
		t.Errorf("Hash() = %q tras el cambio; se esperaba %q", touched.Hash(), want)
	}
}

// TestObtener_TheFingerprintDecides_NotTheStamp: el sello es refuerzo. Los mismos
// bytes con otro sello son un acierto y devuelven el índice que ya había, con el
// sello con el que se indexó.
func TestObtener_TheFingerprintDecides_NotTheStamp(t *testing.T) {
	first := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	f := newFakeSource()
	f.publishStamped("t1", docV1, first)
	c := newCache(t, f, 0)

	idx := get(t, c, "t1")
	f.publishStamped("t1", docV1, first.Add(time.Hour))
	again := get(t, c, "t1")

	if again != idx {
		t.Errorf("mismos bytes con otro sello: se reconstruyó; decide la huella y solo la huella")
	}
	if !again.Sello().Equal(first) {
		t.Errorf("Sello() = %v; el índice conserva el sello con el que se indexó, %v", again.Sello(), first)
	}
	assertStats(t, c, indice.Estadisticas{Construcciones: 1, Aciertos: 1}, "otro sello no es otro contenido")
}

// ---------------------------------------------------------------------------
// EL ADAPTADOR SOBRE EL LECTOR DE CONTENIDO
// ---------------------------------------------------------------------------

// fakeReader implementa indice.LectorContenido y recuerda con qué lo llamaron.
type fakeReader struct {
	blobs      map[string]string
	err        error
	lastTenant string
	lastRef    string
	lastCtx    context.Context
}

func (r *fakeReader) GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error) {
	r.lastCtx, r.lastTenant, r.lastRef = ctx, tenantID, ref
	if r.err != nil {
		return []byte("no debe salir"), r.err
	}
	blob, ok := r.blobs[ref]
	if !ok {
		return nil, fmt.Errorf("fakeReader: sin blob para %q", ref)
	}
	return []byte(blob), nil
}

func TestNewFuenteContenido_DefaultRefIsCatalogo(t *testing.T) {
	spy := &fakeReader{blobs: map[string]string{"catalogo": docV1, "otra": docV2}}
	var reader indice.LectorContenido = spy
	ctx := context.WithValue(context.Background(), ctxKey{}, "job-7")

	source := indice.NewFuenteContenido(reader, "")
	doc, err := source.LeerCatalogo(ctx, "t1")
	if err != nil {
		t.Fatalf("LeerCatalogo = %v", err)
	}
	if string(doc.Raw) != docV1 {
		t.Errorf("Raw = %q; se esperaban los bytes del lector", doc.Raw)
	}
	if spy.lastRef != "catalogo" {
		t.Errorf("ref = %q; sin ref explícita usa RefCatalogo", spy.lastRef)
	}
	if spy.lastTenant != "t1" || spy.lastCtx.Value(ctxKey{}) != "job-7" {
		t.Errorf("el lector recibió tenant %q y otro contexto; se esperaban los de la llamada", spy.lastTenant)
	}
	if !doc.Sello.IsZero() {
		t.Errorf("Sello = %v; GetTenantContent no devuelve updated_at: queda a cero", doc.Sello)
	}

	// Con ref explícita, esa.
	other, err := indice.NewFuenteContenido(reader, "otra").LeerCatalogo(ctx, "t2")
	if err != nil || string(other.Raw) != docV2 || spy.lastRef != "otra" || spy.lastTenant != "t2" {
		t.Errorf("con ref «otra»: Raw = %q, err = %v, ref = %q, tenant = %q", other.Raw, err, spy.lastRef, spy.lastTenant)
	}
}

func TestNewFuenteContenido_ReaderErrorComesOutAsIs(t *testing.T) {
	cause := errors.New("sin fila")
	source := indice.NewFuenteContenido(&fakeReader{err: cause}, "")
	doc, err := source.LeerCatalogo(context.Background(), "t1")
	if err != cause { //nolint:errorlint // el contrato dice TAL CUAL: identidad, no envoltura
		t.Errorf("error = %v; el del lector sale tal cual", err)
	}
	if doc.Raw != nil || !doc.Sello.IsZero() {
		t.Errorf("Documento = %+v junto al error; se esperaba el cero", doc)
	}
}

// TestNewFuenteContenido_FeedsTheCache comprueba el cableado completo que hará F7:
// lector → adaptador → caché → índice con su procedencia.
func TestNewFuenteContenido_FeedsTheCache(t *testing.T) {
	reader := &fakeReader{blobs: map[string]string{"catalogo": docV1}}
	c := newCache(t, indice.NewFuenteContenido(reader, ""), 0)

	idx := get(t, c, "t1")
	if idx.Articulos() != 2 {
		t.Errorf("Articulos() = %d; se esperaban 2", idx.Articulos())
	}
	if len(idx.Hash()) != 64 {
		t.Errorf("Hash() = %q; el índice que sale de la caché lleva su procedencia (64 hex)", idx.Hash())
	}
	if !idx.Sello().IsZero() {
		t.Errorf("Sello() = %v; el adaptador no sirve sello", idx.Sello())
	}

	reader.err = errors.New("sin fila")
	_, err := c.Obtener(context.Background(), "t9")
	if want := `catalogo: leer el documento del tenant "t9": sin fila`; err == nil || err.Error() != want {
		t.Errorf("error = %v; se esperaba %q", err, want)
	}
}
