// Porta internal/intake/catalogo/indice.go @ 3c74b80

// Package indice es EL ÍNDICE EN MEMORIA del catálogo del tenant (Plan 044 ·
// Ola 3 · T3.7): la pieza que la etapa `match` consulta una vez por ÍTEM sin
// volver a leer ni a parsear el catálogo. En el árbol viejo el paquete se llamaba
// `catalogo` (internal/intake/catalogo) y por eso sus textos observables siguen
// diciendo "catalogo: …": el renombre del paquete no los cambia.
//
// # EL COSTE QUE ESTA PIEZA SACA DEL CAMINO
//
// `catalogo.ParseCatalog` es PURA y no memoiza nada: re-serializa `Content.Raw` y
// lo vuelve a decodificar en CADA llamada. Para un turno conversacional eso es
// tolerable —un mensaje, un parseo—, pero el pipeline de presupuestos hace lo
// contrario: un solo job trae hasta 10 ítems (D-044.39) y los 10 miran el MISMO
// catálogo. Sin índice, el match pagaría 10 parseos del documento entero.
//
// Este paquete lo paga UNA VEZ por (tenant, CONTENIDO) y lo reutiliza.
//
// # 🔴 LA INVALIDACIÓN ES POR CONTENIDO, NO POR VERSIÓN (D-044.44)
//
// Y no es una preferencia de diseño: es que NO HAY VERSIÓN QUE LEER.
// `public.tenant_content` tiene exactamente `tenant_id, ref, content, created_at,
// updated_at` — ni `version`, ni `etag`, ni `hash`. La tabla
// `tenant_content_versions` guarda el blob VIEJO y el motor no la lee NUNCA.
//
// Invalidar por hash del contenido ya leído tapa además un agujero real: el `PUT`
// genérico de `/api/v1/tenant-content/{ref}` NO versiona nada pero SÍ cambia el
// documento. Un catálogo editado a mano tiene que invalidar el índice igual que uno
// importado, y con una invalidación por versión no lo haría.
//
// `updated_at` viaja en Documento.Sello como REFUERZO y observabilidad (queda
// escrito de cuándo era el documento que se indexó); quien decide es el hash. Ver
// cache.go.
//
// # 🔴 DÓNDE VIVE ESTO, Y POR QUÉ IMPORTA (criterio (d) de T3.7)
//
// En el módulo `catalogo`, y lo consume EL WORKER DEL PIPELINE (`captacion`) — no
// `conversacion`, que es el turno conversacional. El parseo del catálogo NO puede
// volver al camino del entrante por la puerta de atrás (INV-02 / T1.5): el mensaje
// del cliente se contesta sin esperar a nada de esto. Que nada de `conversacion`
// importe este paquete lo custodia el candado de fronteras
// (internal/modulos/fronteras_test.go), no un comentario.
package indice

import (
	"errors"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// MaxArticulos es LA COTA DE TAMAÑO del índice: 2.000 artículos por catálogo
// (D-044.44, decidida por Jhoan el 2026-08-26).
//
// Es un SUPERCONJUNTO SEGURO de la cota que ya existe una capa más arriba: el
// import acota a `catalogimport.DefaultMaxItems` = 500 artículos por documento.
// Los dos números conviven a propósito y dicen cosas distintas: 500 es cuánto se
// admite CARGAR de una vez, 2.000 es cuánto se admite INDEXAR —y el segundo tiene
// que ser mayor, porque un catálogo puede crecer por varios imports y por el `PUT`
// a mano, que no pasa por el validador del import.
//
// La cota es de MEMORIA y es explícita: el techo del proceso es
// `MaxTenantsEnCache × MaxArticulos` entradas (64 × 2.000 = 128.000), cada una un
// `catalogo.Article` más una posición en cuatro mapas. No hay ninguna otra
// estructura que crezca con el tamaño del catálogo.
const MaxArticulos = 2000

// Errores del índice. Ninguno es recuperable dentro del paquete: los tres dicen
// que el índice NO SE PUEDE construir, y el llamante (el worker) decide si eso mata
// el job o lo devuelve a la cola. Sus textos son literales observables.
var (
	// ErrSinNormalizador es «se intentó construir el índice sin normalizador». El
	// índice no nace a medias: sin normalizador, `PorEtiqueta` y `PorTag` no
	// podrían casar «Café» con «cafe» y el match saldría vacío SIN dar error, que
	// es la peor de las averías posibles.
	ErrSinNormalizador = errors.New("catalogo: el índice necesita el normalizador de textos (hoy, wapp-shared/textmatch.Normalize)")

	// ErrNormalizadorInvalido es «el normalizador inyectado no cumple el contrato».
	// Ver VerificarNormalizador: el contrato incluye PRESERVAR LA Ñ, y un
	// `strings.ToLower` a secas —que parece un normalizador razonable— colapsaría
	// «año» con «ano» sin que nadie se entere.
	ErrNormalizadorInvalido = errors.New("catalogo: el normalizador inyectado no cumple el contrato del índice")

	// ErrCatalogoDemasiadoGrande es «el catálogo excede MaxArticulos».
	//
	// 🔴 SE RECHAZA, NO SE TRUNCA. Un índice truncado casaría los primeros N
	// artículos y dejaría los demás como `unmatched` SIN decir por qué: el dueño
	// vería un presupuesto al que le faltan líneas y no habría nada en el log que
	// lo explicara. Un fallo con la cifra escrita —cuántos trae y cuál es el
	// tope— es peor experiencia y mejor diagnóstico, y el pipeline ya sabe morir
	// con la causa escrita (`Fail`).
	ErrCatalogoDemasiadoGrande = errors.New("catalogo: el catálogo excede el tope de artículos del índice")
)

// Normalizador es la frontera con el motor de comparación de textos: una función
// pura que lleva un texto a su forma canónica.
//
// 🔴 ES UN PUERTO INYECTADO, NO UNA COPIA. La implementación de producción es
// `github.com/EduGoGroup/wapp-shared/textmatch.Normalize` (Plan 044 · T3.1), la
// misma que usa la etapa `match`. Duplicar aquí el plegado de diacríticos dejaría
// DOS normalizadores que se desincronizan en silencio (y el síntoma sería un match
// que falla solo con las palabras acentuadas que alguien tocó en uno de los dos).
// Así que el índice declara la forma y la recibe; ver `normalizador.go` para el
// contrato exacto.
type Normalizador func(string) string

// Coincidencia es lo que devuelve una búsqueda del índice: el artículo y la
// categoría a la que pertenece, más la variante concreta cuando la búsqueda fue
// por variante.
//
// ⚠️ `Articulo` se devuelve POR VALOR, pero sus campos compuestos (`Tags`,
// `Attributes`, `Variants`, `Components`) comparten el array de respaldo con el
// índice: el índice es de SOLO LECTURA y quien reciba una Coincidencia no debe
// mutar esos slices ni ese mapa. Copiar en profundidad en cada búsqueda pagaría en
// cada ítem lo que esta tarea existe para no pagar.
type Coincidencia struct {
	// Categoria es el Code de la categoría del artículo ("1", "2", …).
	Categoria string
	// CategoriaLabel es la etiqueta visible de esa categoría ("Bebidas").
	CategoriaLabel string
	// Articulo es el artículo del catálogo, tal como lo dejó catalogo.ParseCatalog.
	Articulo catalogo.Article
	// Variante es la presentación concreta que casó. Solo se puebla en PorVariante;
	// en el resto de búsquedas queda a cero y HayVariante es false.
	Variante catalogo.Variant
	// HayVariante distingue «la variante es la cero» de «no hubo variante». Sin
	// este booleano, una variante con code y label vacíos sería indistinguible de
	// una búsqueda por sku.
	HayVariante bool
}

// Indice es el catálogo de UN tenant preparado para consultarse por ítem.
//
// 🔴 NO TIENE PUERTO DE LECTURA, Y ESO ES LA GARANTÍA (criterio (a) de T3.7). No
// guarda la Fuente, ni el documento crudo, ni nada con lo que volver a Postgres:
// una búsqueda NO PUEDE leer `tenant_content` aunque alguien lo intentara, porque
// no tiene con qué. La garantía es estructural, no una advertencia en un
// comentario: `*Indice` NO satisface Fuente.
//
// Es de SOLO LECTURA una vez construido, así que varias goroutines pueden
// consultarlo a la vez sin sincronización.
//
// El orden de los artículos es el EXACTO del documento (categoría a categoría,
// artículo a artículo), y ese orden es el contrato de salida de todas las
// búsquedas: es lo que hace que el índice y una búsqueda lineal ingenua devuelvan
// lo mismo, elemento a elemento.
type Indice struct{}

// Construir indexa un catálogo ya parseado. Es PURO: sin I/O y sin estado global.
//
// Falla, y no a medias (el índice devuelto es nil):
//
//   - con `normalizar` nil: ErrSinNormalizador;
//   - si `normalizar` no cumple el contrato: el error de VerificarNormalizador, que
//     envuelve ErrNormalizadorInvalido;
//   - si el catálogo trae más de MaxArticulos artículos, CONTADOS SOBRE TODAS LAS
//     CATEGORÍAS (el tope es del documento, no por categoría): un error que
//     envuelve ErrCatalogoDemasiadoGrande y dice
//     «<ErrCatalogoDemasiadoGrande>: trae <n> artículos y el tope es 2000».
//     2.000 entran; 2.001, no.
//
// El índice que devuelve no ha visto bytes: su Hash es "" y su Sello, cero.
//
// Las reglas de empate coinciden EXACTAMENTE con lo que hace una búsqueda lineal
// ingenua sobre el mismo catálogo:
//
//   - sku repetido: gana el PRIMERO en orden de documento (un sku duplicado es un
//     catálogo roto; el import lo rechaza, el runtime no puede);
//   - misma clave repetida en el MISMO artículo (dos tags que normalizan igual): el
//     artículo aparece UNA vez, no dos;
//   - dos variantes del mismo artículo que normalizan igual: aparecen las DOS,
//     porque el resultado de una búsqueda por variante es el par (artículo,
//     variante) y ahí son dos pares distintos.
//
// No hay ningún caso especial para la clave vacía: un artículo con label vacío se
// indexa bajo "" y `PorEtiqueta("")` lo devuelve —igual que un label de solo
// espacios, que normaliza a ""—. Es lo que hace la búsqueda lineal y una excepción
// aquí sería una divergencia que nadie recordaría.
//
// El coste es O(artículos + tags + variantes) y se paga UNA VEZ por contenido; lo
// que se ahorra es ese mismo coste multiplicado por el número de ítems del pedido.
func Construir(cat catalogo.Catalog, normalizar Normalizador) (*Indice, error) {
	panic(pendiente.Implementar("indice.Construir"))
}

// Articulos es cuántos artículos indexa. Es la cifra que se compara con
// MaxArticulos y la que debe ir al log del worker.
func (i *Indice) Articulos() int {
	panic(pendiente.Implementar("indice.Indice.Articulos"))
}

// Etiqueta devuelve la etiqueta CRUDA (sin normalizar) del artículo que ocupa la
// posición n en el orden del documento, o "" si n cae fuera (negativa, o
// ≥ Articulos). Junto con Articulos y En forma el RECORRIDO de solo lectura del
// catálogo.
//
// 🔴 POR QUÉ EXISTE, Y POR QUÉ ES UN CURSOR Y NO UN `[]string`. Lo pide el escalón
// FUZZY de la cascada de T3.2: los cuatro accesos por clave (PorSKU, PorEtiqueta,
// PorTag, PorVariante) son hash, y un hash no sabe contestar «¿qué etiqueta se
// PARECE a ésta?». Sin recorrido, la cascada `Exact → Fuzzy → LLM` se queda sin su
// escalón del medio y toda errata acaba pagando una llamada al modelo.
//
// Devolver un slice materializaría 2.000 cadenas en cada llamada; devolver una
// Coincidencia por entrada copiaría el `catalogo.Article` entero —con sus slices y
// su mapa— 2.000 veces por ítem. El cursor recorre sin copiar y solo el GANADOR se
// materializa con En.
//
// NO abre el puerto de lectura que el criterio (a) de T3.7 cierra: sigue sin haber
// forma de volver a `tenant_content` desde aquí.
func (i *Indice) Etiqueta(n int) string {
	panic(pendiente.Implementar("indice.Indice.Etiqueta"))
}

// En materializa la Coincidencia del artículo en la posición n —categoría,
// etiqueta de categoría y artículo; sin variante—, o la coincidencia cero si n cae
// fuera. Es el complemento de Etiqueta: se recorre con Etiqueta —que no copia
// nada— y se paga la copia UNA vez, la del ganador.
func (i *Indice) En(n int) Coincidencia {
	panic(pendiente.Implementar("indice.Indice.En"))
}

// Hash es la huella del documento del que salió el índice, y es la que decide la
// invalidación. Vacía si el índice se construyó con Construir en vez de por la
// caché (Construir indexa un catálogo, no un documento: no ha visto bytes).
func (i *Indice) Hash() string {
	panic(pendiente.Implementar("indice.Indice.Hash"))
}

// Sello es el `updated_at` del documento indexado, cuando la Fuente lo sirve; cero
// si no, o si el índice salió de Construir. Es REFUERZO y observabilidad: quien
// decide si el índice sigue valiendo es Hash.
func (i *Indice) Sello() time.Time {
	panic(pendiente.Implementar("indice.Indice.Sello"))
}

// PorSKU busca por identificador de negocio. El sku es OPACO: se compara literal,
// sin normalizar —ni mayúsculas, ni tildes, ni espacios—, porque no es texto que
// teclee un cliente sino la clave con la que el dueño nombra su producto
// ("TORTA-CHOC"). Normalizarlo colapsaría "TORTA-CHOC" con "torta choc" y haría
// ambiguos dos skus que el dueño escribió distintos.
//
// Con skus repetidos devuelve el primero en orden de documento. Sin acierto,
// la coincidencia cero y false. La coincidencia no lleva variante.
func (i *Indice) PorSKU(sku string) (Coincidencia, bool) {
	panic(pendiente.Implementar("indice.Indice.PorSKU"))
}

// PorEtiqueta busca por el label del artículo, normalizando los DOS lados: el texto
// que llega y el del catálogo. Devuelve los artículos en orden de documento, sin
// variante, o nil si ninguno casa.
func (i *Indice) PorEtiqueta(texto string) []Coincidencia {
	panic(pendiente.Implementar("indice.Indice.PorEtiqueta"))
}

// PorTag busca por etiqueta informativa del artículo («vegano», «sin gluten»),
// normalizando los dos lados. Un tag puede casar VARIOS artículos: los devuelve
// todos, en orden de documento y cada uno UNA vez, sin variante; nil si ninguno.
func (i *Indice) PorTag(texto string) []Coincidencia {
	panic(pendiente.Implementar("indice.Indice.PorTag"))
}

// PorVariante busca por el label de una variante («grande», «12 porciones»),
// normalizando los dos lados, y devuelve el par (artículo, variante) con
// HayVariante en true: sin la variante —y con ella SU precio—, quien reciba la
// coincidencia no sabría qué cobrar, que es justo lo que la variante decide
// (D-041.4). En orden de documento y, dentro del artículo, en el de sus
// variantes; nil si ninguna casa.
func (i *Indice) PorVariante(texto string) []Coincidencia {
	panic(pendiente.Implementar("indice.Indice.PorVariante"))
}
