package catalogo_test

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
)

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

// docV1PrecioTocado es docV1 con UN DÍGITO cambiado: 2.50 → 2.90. Mismo número de
// bytes, misma forma, mismo todo salvo el precio.
//
// 🔴 Este es el documento que separa una huella de verdad de un resumen barato: una
// invalidación por longitud, o por «los primeros N bytes», daría los dos por
// iguales y el pipeline seguiría cotizando el precio viejo indefinidamente.
const docV1PrecioTocado = `{"categories":[
  {"code":"1","label":"Bebidas","items":[
    {"code":"1","sku":"CAFE","label":"Café","price":2.90,"tags":["caliente"]},
    {"code":"2","sku":"TE","label":"Té","price":2.00}
  ]}
]}`

func nuevaCache(t *testing.T, f catalogo.Fuente) *catalogo.Cache {
	t.Helper()
	c, err := catalogo.NewCache(f, normalizadorDoble, 0)
	require.NoError(t, err)
	return c
}

// ---------------------------------------------------------------------------
// CRITERIO (a) — CONTEO: UNA CONSTRUCCIÓN, CERO LECTURAS ADICIONALES
// ---------------------------------------------------------------------------

// TestConteo_UnJobDeNItems_UnaLecturaYUnaConstruccion.
//
// 🔴 Se asserta el CONTADOR Y EL ESTADO, no solo el contador. Un contador dice lo
// que cuenta: que `lecturas` valga 1 no prueba que los 10 ítems miraran el MISMO
// índice. Por eso el test comprueba además que el puntero es el mismo y que los 10
// ítems obtienen la respuesta correcta.
func TestConteo_UnJobDeNItems_UnaLecturaYUnaConstruccion(t *testing.T) {
	ctx := context.Background()
	f := nuevaFuenteFalsa()
	f.publicar("t1", docV1)
	c := nuevaCache(t, f)

	idx, err := c.Obtener(ctx, "t1")
	require.NoError(t, err)
	require.Equal(t, 1, f.leidas(), "el job lee el documento UNA vez")
	require.Equal(t, uint64(1), c.Estadisticas().Construcciones)

	// Los N ítems del pedido. El tope es 10 (stages.TopeItemsPorPedido); se usan 10.
	const items = 10
	for i := range items {
		got, hay := idx.PorSKU("CAFE")
		require.Truef(t, hay, "ítem %d", i)
		require.Equal(t, 2.5, got.Articulo.Price)
		require.Len(t, idx.PorEtiqueta("cafe"), 1)
		require.Len(t, idx.PorTag("caliente"), 1)
	}

	require.Equal(t, 1, f.leidas(), "🔴 a partir del segundo ítem, CERO lecturas: el *Indice no tiene con qué leer")
	require.Equal(t, uint64(1), c.Estadisticas().Construcciones, "y una sola construcción para los 10 ítems")
}

// TestConteo_SegundoJob_MismoContenido_CeroConstrucciones: el segundo job SÍ lee
// —hay que hashear para saber si el catálogo cambió, y no hay versión que preguntar
// (D-044.44)— pero no vuelve a indexar. Y devuelve EL MISMO índice, que es el estado
// que el contador por sí solo no probaría.
func TestConteo_SegundoJob_MismoContenido_CeroConstrucciones(t *testing.T) {
	ctx := context.Background()
	f := nuevaFuenteFalsa()
	f.publicar("t1", docV1)
	c := nuevaCache(t, f)

	primero, err := c.Obtener(ctx, "t1")
	require.NoError(t, err)

	for range 5 {
		otro, err := c.Obtener(ctx, "t1")
		require.NoError(t, err)
		require.Same(t, primero, otro, "el mismo contenido tiene que devolver EL MISMO índice, no uno equivalente")
	}

	st := c.Estadisticas()
	require.Equal(t, uint64(1), st.Construcciones, "una sola construcción en seis jobs")
	require.Equal(t, uint64(5), st.Aciertos)
	require.Equal(t, 6, f.leidas(), "las lecturas sí son una por job: es el precio de invalidar por contenido")
}

// TestConteo_LaHuellaSeCalculaSobreLoQueYASeLEYO: la invalidación por contenido no
// puede costar una consulta extra. Con una Fuente que cuenta, seis jobs son seis
// lecturas — no doce.
func TestConteo_LaHuellaSeCalculaSobreLoQueYASeLEYO(t *testing.T) {
	ctx := context.Background()
	f := nuevaFuenteFalsa()
	f.publicar("t1", docV1)
	c := nuevaCache(t, f)

	for range 6 {
		_, err := c.Obtener(ctx, "t1")
		require.NoError(t, err)
	}
	require.Equal(t, 6, f.leidas(), "una lectura por job y ninguna más: la huella sale de esos mismos bytes")
}

// ---------------------------------------------------------------------------
// CRITERIO (b) — INVALIDACIÓN: SIN REINICIAR EL PROCESO, POR LOS DOS CAMINOS
// ---------------------------------------------------------------------------

// TestInvalidacion_ElImportYElPutAMano_LosDosInvalidan.
//
// Los dos caminos se prueban por separado porque el criterio los nombra por
// separado, y conviene decir en voz alta por qué acaban siendo el mismo test: desde
// la caché son INDISTINGUIBLES por construcción. Ninguno de los dos deja versión en
// `tenant_content` —no hay columna donde dejarla— y los dos cambian los bytes. Esa
// indistinguibilidad ES la razón de D-044.44: una invalidación por versión habría
// atendido al import y se habría comido el `PUT` en silencio.
func TestInvalidacion_ElImportYElPutAMano_LosDosInvalidan(t *testing.T) {
	t.Run("import: el documento crece", func(t *testing.T) {
		ctx := context.Background()
		f := nuevaFuenteFalsa()
		f.publicar("t1", docV1)
		c := nuevaCache(t, f)

		viejo, err := c.Obtener(ctx, "t1")
		require.NoError(t, err)
		require.Equal(t, 2, viejo.Articulos())
		_, hay := viejo.PorSKU("CAFE-DESC")
		require.False(t, hay)

		f.publicar("t1", docV2) // ← el import escribe el documento nuevo

		nuevo, err := c.Obtener(ctx, "t1")
		require.NoError(t, err)
		require.NotSame(t, viejo, nuevo, "el índice tiene que ser otro")
		require.Equal(t, 3, nuevo.Articulos())
		_, hay = nuevo.PorSKU("CAFE-DESC")
		require.True(t, hay, "🔴 el siguiente match usa el catálogo NUEVO, sin reiniciar el proceso")
		require.Equal(t, uint64(2), c.Estadisticas().Construcciones)
	})

	t.Run("PUT a mano: cambia UN dígito y no cambia el tamaño", func(t *testing.T) {
		ctx := context.Background()
		f := nuevaFuenteFalsa()
		f.publicar("t1", docV1)
		c := nuevaCache(t, f)

		viejo, err := c.Obtener(ctx, "t1")
		require.NoError(t, err)
		cafe, _ := viejo.PorSKU("CAFE")
		require.Equal(t, 2.5, cafe.Articulo.Price)

		require.Equal(t, len(docV1), len(docV1PrecioTocado), "el fixture tiene que pesar lo mismo o no prueba nada")
		f.publicar("t1", docV1PrecioTocado) // ← el PUT genérico, que NO versiona

		nuevo, err := c.Obtener(ctx, "t1")
		require.NoError(t, err)
		cafe, _ = nuevo.PorSKU("CAFE")
		require.Equal(t, 2.9, cafe.Articulo.Price, "🔴 un catálogo editado a mano tiene que invalidar igual que uno importado")
		require.Equal(t, uint64(2), c.Estadisticas().Construcciones)
	})

	t.Run("volver al documento anterior también invalida", func(t *testing.T) {
		ctx := context.Background()
		f := nuevaFuenteFalsa()
		f.publicar("t1", docV1)
		c := nuevaCache(t, f)

		_, err := c.Obtener(ctx, "t1")
		require.NoError(t, err)
		f.publicar("t1", docV2)
		_, err = c.Obtener(ctx, "t1")
		require.NoError(t, err)
		f.publicar("t1", docV1) // deshacer: el contenido "retrocede"

		vuelta, err := c.Obtener(ctx, "t1")
		require.NoError(t, err)
		require.Equal(t, 2, vuelta.Articulos(), "la huella no es un número de versión: no tiene sentido de avance")
		require.Equal(t, uint64(3), c.Estadisticas().Construcciones)
	})
}

// TestInvalidacion_ElCambioDeUnTenantNoTocaAOtro: la caché es por tenant y el
// aislamiento es la mitad silenciosa del criterio (INV-8, el tenant no se cruza).
func TestInvalidacion_ElCambioDeUnTenantNoTocaAOtro(t *testing.T) {
	ctx := context.Background()
	f := nuevaFuenteFalsa()
	f.publicar("t1", docV1)
	f.publicar("t2", docV1)
	c := nuevaCache(t, f)

	idx1, err := c.Obtener(ctx, "t1")
	require.NoError(t, err)
	_, err = c.Obtener(ctx, "t2")
	require.NoError(t, err)

	f.publicar("t2", docV2)
	nuevo2, err := c.Obtener(ctx, "t2")
	require.NoError(t, err)
	require.Equal(t, 3, nuevo2.Articulos())

	mismo1, err := c.Obtener(ctx, "t1")
	require.NoError(t, err)
	require.Same(t, idx1, mismo1, "el catálogo de t1 no se ha tocado: su índice no se reconstruye")
	require.Equal(t, uint64(3), c.Estadisticas().Construcciones, "t1, t2 y el t2 nuevo: tres, no cuatro")
}

// ---------------------------------------------------------------------------
// LA COTA DE MEMORIA DE LA CACHÉ
// ---------------------------------------------------------------------------

// TestCache_DesalojaElMenosUsado: sin tope, un proceso que atienda a muchos tenants
// acumula un índice por cada uno y no lo suelta nunca.
func TestCache_DesalojaElMenosUsado(t *testing.T) {
	ctx := context.Background()
	f := nuevaFuenteFalsa()
	c, err := catalogo.NewCache(f, normalizadorDoble, 2)
	require.NoError(t, err)

	for i := range 3 {
		f.publicar("t"+strconv.Itoa(i), docV1)
	}
	for i := range 3 {
		_, err := c.Obtener(ctx, "t"+strconv.Itoa(i))
		require.NoError(t, err)
	}

	require.Equal(t, 2, c.Tamano(), "el tope se respeta")
	require.Equal(t, uint64(1), c.Estadisticas().Desalojos)

	// t0 fue el menos usado: vuelve a construirse. t2 sigue dentro.
	_, err = c.Obtener(ctx, "t2")
	require.NoError(t, err)
	require.Equal(t, uint64(3), c.Estadisticas().Construcciones, "t2 seguía cacheado")

	_, err = c.Obtener(ctx, "t0")
	require.NoError(t, err)
	require.Equal(t, uint64(4), c.Estadisticas().Construcciones, "t0 fue desalojado y se reconstruye")
}

// ---------------------------------------------------------------------------
// LA CACHÉ NO NACE A MEDIAS
// ---------------------------------------------------------------------------

func TestCache_NoNaceSinFuenteNiSinNormalizadorValido(t *testing.T) {
	_, err := catalogo.NewCache(nil, normalizadorDoble, 0)
	require.ErrorIs(t, err, catalogo.ErrSinFuente)

	_, err = catalogo.NewCache(nuevaFuenteFalsa(), nil, 0)
	require.ErrorIs(t, err, catalogo.ErrSinNormalizador)

	_, err = catalogo.NewCache(nuevaFuenteFalsa(), normalizadorTramposo, 0)
	require.ErrorIs(t, err, catalogo.ErrNormalizadorInvalido)
}

// TestCache_UnDocumentoRotoNoDejaUnIndiceAMedias: el error sale con el tenant
// escrito y la caché no guarda nada — el siguiente job vuelve a intentarlo.
func TestCache_UnDocumentoRotoNoDejaUnIndiceAMedias(t *testing.T) {
	ctx := context.Background()
	f := nuevaFuenteFalsa()
	f.publicar("t1", `{"categories":`)
	c := nuevaCache(t, f)

	_, err := c.Obtener(ctx, "t1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "t1")
	require.Equal(t, 0, c.Tamano())
	require.Equal(t, uint64(0), c.Estadisticas().Construcciones)
}

// TestCache_ElAdaptadorSobreElLectorDeContenido comprueba el cableado con la firma
// que ya existe en el repo (`content.Store` / `flujos/store.Repository`), incluida
// la ref por defecto.
func TestCache_ElAdaptadorSobreElLectorDeContenido(t *testing.T) {
	ctx := context.Background()
	lector := &lectorFalso{blobs: map[string]string{"catalogo": docV1}}

	fuente := catalogo.NewFuenteContenido(lector, "")
	doc, err := fuente.LeerCatalogo(ctx, "t1")
	require.NoError(t, err)
	require.Equal(t, docV1, string(doc.Raw))
	require.Equal(t, "catalogo", lector.ultimaRef, "sin ref explícita usa RefCatalogo")
	require.True(t, doc.Sello.IsZero(), "GetTenantContent no devuelve updated_at: el sello queda a cero, y eso está dicho en el contrato")

	c, err := catalogo.NewCache(fuente, normalizadorDoble, 0)
	require.NoError(t, err)
	idx, err := c.Obtener(ctx, "t1")
	require.NoError(t, err)
	require.Equal(t, 2, idx.Articulos())
	require.NotEmpty(t, idx.Hash(), "el índice que sale de la caché lleva su procedencia")
}

// lectorFalso implementa catalogo.LectorContenido.
type lectorFalso struct {
	blobs     map[string]string
	ultimaRef string
}

func (l *lectorFalso) GetTenantContent(_ context.Context, _, ref string) ([]byte, error) {
	l.ultimaRef = ref
	blob, ok := l.blobs[ref]
	if !ok {
		return nil, fmt.Errorf("lectorFalso: sin blob para %q", ref)
	}
	return []byte(blob), nil
}
