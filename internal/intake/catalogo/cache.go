package catalogo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/model"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
)

// cache.go — LA CACHÉ POR PROCESO DEL ÍNDICE, INVALIDADA POR CONTENIDO (D-044.44).
//
// # LAS DOS MITADES, Y POR QUÉ SON DOS
//
// `Cache.Obtener` es POR JOB: lee el documento, decide si el índice sigue valiendo
// y lo devuelve. Las búsquedas son POR ÍTEM y viven en `*Indice`, que no tiene con
// qué leer nada. Esa separación ES el criterio (a) de T3.7: no hay forma de que el
// ítem número 7 de un pedido dispare un SELECT, porque el objeto que consulta no
// conoce la Fuente.
//
// # POR QUÉ SE LEE SIEMPRE Y AUN ASÍ HAY CACHÉ
//
// Cada `Obtener` hace UNA lectura del documento. No es un descuido: es el precio de
// invalidar por contenido cuando la tabla no tiene versión que preguntar (ver el
// bloque de indice.go). Lo que la caché ahorra es lo CARO —el parseo del documento
// entero y la construcción de los cuatro accesos—, no el SELECT.
//
// La cuenta, para que quede dicha con números: un job de 10 ítems pasa de 10
// SELECT + 10 parseos completos a 1 SELECT + 0 parseos si el catálogo no cambió, y
// a 1 SELECT + 1 parseo si cambió.
//
// # 🔴 EL AGUJERO QUE ESTO TAPA, Y QUE UNA CACHÉ POR VERSIÓN NO TAPARÍA
//
// `PUT /api/v1/tenant-content/{ref}` escribe el documento sin versionar nada
// (`publicapi/tenantcontent.go`). Si la caché mirara una versión, un catálogo
// editado a mano seguiría cotizándose con el índice viejo hasta que alguien
// reiniciara el proceso — y nadie relacionaría el precio equivocado con el reinicio
// que no se hizo. Mirando el contenido, el import y el `PUT` son indistinguibles: los
// dos cambian bytes, los dos invalidan.

// RefCatalogo es la referencia de `public.tenant_content` bajo la que vive el
// catálogo. Es la MISMA que usa el import (`publicapi/catalogimport.go:26`, ahí
// `defaultCatalogRef`), duplicada aquí porque allí no está exportada y porque
// `internal/intake` no debe importar el transporte.
const RefCatalogo = "catalogo"

// MaxTenantsEnCache es cuántos catálogos distintos se guardan a la vez. Es la otra
// mitad de la cota de memoria: sin él, un proceso que atienda a muchos tenants
// acumularía un índice por cada uno y no lo soltaría nunca — un crecimiento lento y
// permanente, que es la clase de fuga que nadie ve hasta que el proceso muere.
//
// Al superarlo se desaloja el índice MENOS USADO recientemente. Desalojar no pierde
// nada: el siguiente `Obtener` de ese tenant lo reconstruye desde el documento que
// de todos modos iba a leer.
const MaxTenantsEnCache = 64

// Documento es el catálogo crudo tal como está guardado, más su sello temporal.
type Documento struct {
	// Raw es el JSON crudo de `public.tenant_content.content`. Es lo que se hashea:
	// la huella se calcula sobre los BYTES QUE YA SE LEYERON, sin ninguna consulta
	// extra a la tabla.
	Raw []byte
	// Sello es el `updated_at` de la fila, cuando la Fuente lo sirve.
	//
	// ⚠️ Es REFUERZO y observabilidad, NO un criterio de decisión: quien decide si
	// el índice sigue valiendo es el hash, y solo el hash. Hoy el adaptador sobre el
	// lector de contenido lo deja a cero porque la consulta que existe
	// (`GetTenantContent`, repository_postgres.go:402) devuelve solo `content`; el
	// campo está aquí para que una Fuente que sí lo lea pueda dejarlo escrito en el
	// índice sin cambiar el puerto.
	Sello time.Time
}

// Fuente es de dónde sale el documento del catálogo de un tenant. Puerto ESTRECHO:
// una operación y ninguna más. El índice no necesita listar refs, ni escribir, ni
// saber de versiones, y un puerto ancho invitaría a que alguien colgara aquí una
// escritura que el worker no debe poder hacer.
type Fuente interface {
	LeerCatalogo(ctx context.Context, tenantID string) (Documento, error)
}

// LectorContenido es la forma EXACTA de `content.Store` y de
// `flujos/store.Repository` en lo que a contenido de tenant se refiere. Se declara
// aquí en vez de importar aquel paquete (structural typing): así este paquete —que
// vive en el worker— no arrastra una dependencia del motor conversacional solo para
// nombrar una firma.
type LectorContenido interface {
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// ErrSinFuente es «se intentó construir la caché sin de dónde leer».
var ErrSinFuente = errors.New("catalogo: la caché necesita una Fuente de la que leer el documento")

// fuenteContenido adapta un LectorContenido a Fuente.
type fuenteContenido struct {
	lector LectorContenido
	ref    string
}

// NewFuenteContenido envuelve el lector de `tenant_content` que ya existe. Con `ref`
// vacía usa RefCatalogo.
//
// El Documento que devuelve lleva el Sello a CERO, y es honesto que así sea: la
// consulta de abajo no selecciona `updated_at`. Ver Documento.Sello.
func NewFuenteContenido(lector LectorContenido, ref string) Fuente {
	if ref == "" {
		ref = RefCatalogo
	}
	return fuenteContenido{lector: lector, ref: ref}
}

// LeerCatalogo implementa Fuente sobre el lector de contenido de tenant.
func (f fuenteContenido) LeerCatalogo(ctx context.Context, tenantID string) (Documento, error) {
	raw, err := f.lector.GetTenantContent(ctx, tenantID, f.ref)
	if err != nil {
		return Documento{}, err
	}
	return Documento{Raw: raw}, nil
}

// Estadisticas son los contadores de la caché. Van al log del worker y son lo que
// hace verificable el criterio (a) desde fuera del paquete.
//
// 🔴 Un contador dice lo que CUENTA, no lo que pasó: `Aciertos` alto con
// `Construcciones` alto a la vez significa que hay tenants alternándose y
// desalojándose, no que la caché vaya bien. Los tres números se leen juntos.
type Estadisticas struct {
	// Aciertos son las veces que el hash coincidió y NO hubo que indexar nada.
	Aciertos uint64
	// Construcciones son las veces que sí hubo que parsear e indexar: la primera de
	// cada tenant, y cada cambio de contenido.
	Construcciones uint64
	// Desalojos son los índices tirados por MaxTenantsEnCache.
	Desalojos uint64
}

// entradaCache es un índice vivo más su marca de último uso (el reloj lógico de la
// caché, no el de pared: para ordenar por antigüedad de uso no hace falta la hora,
// y un contador no se ve afectado por un salto del reloj del sistema).
type entradaCache struct {
	indice    *Indice
	ultimoUso uint64
}

// Cache es la caché por proceso de índices de catálogo, una por tenant.
//
// ⚠️ ES POR PROCESO, y eso es una decisión, no un descuido. Con dos réplicas del
// Cloud habría dos cachés y por tanto hasta dos parseos por cambio de catálogo —
// que es exactamente lo mismo que ya se acepta para el aforo del pipeline
// (`pipeline/plaza.go`: «el Cloud corre en UNA réplica»). Nada se corrompe con dos:
// las dos leen el mismo documento y llegan al mismo índice; solo se paga dos veces
// un trabajo barato.
//
// Es segura para uso concurrente.
type Cache struct {
	fuente     Fuente
	normalizar Normalizador
	max        int

	mu      sync.Mutex
	reloj   uint64
	indices map[string]*entradaCache
	stats   Estadisticas
}

// NewCache construye la caché. `max` <= 0 cae a MaxTenantsEnCache: igual que en el
// resto del repo, la configuración nunca DESACTIVA un tope por accidente.
//
// Falla si falta la Fuente o si el normalizador no cumple el contrato — y falla
// AQUÍ, en el arranque, no en el primer job de la noche.
func NewCache(fuente Fuente, normalizar Normalizador, max int) (*Cache, error) {
	if fuente == nil {
		return nil, ErrSinFuente
	}
	if err := VerificarNormalizador(normalizar); err != nil {
		return nil, err
	}
	if max <= 0 {
		max = MaxTenantsEnCache
	}
	return &Cache{fuente: fuente, normalizar: normalizar, max: max, indices: make(map[string]*entradaCache, max)}, nil
}

// Obtener devuelve el índice del catálogo del tenant, construyéndolo solo si el
// contenido cambió desde la última vez.
//
// El llamante (el worker del pipeline) lo llama UNA VEZ POR JOB y le pasa el
// `*Indice` a la etapa `match`, que lo consulta por cada ítem. Ese reparto es el
// criterio (a) y está garantizado por la forma de los tipos, no por disciplina: el
// `*Indice` no puede leer.
func (c *Cache) Obtener(ctx context.Context, tenantID string) (*Indice, error) {
	// La lectura va FUERA del candado: es la única operación con I/O de todo el
	// método y retenerlo durante un round-trip a Postgres bloquearía a los demás
	// tenants por algo que no es suyo.
	doc, err := c.fuente.LeerCatalogo(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("catalogo: leer el documento del tenant %q: %w", tenantID, err)
	}
	h := huella(doc.Raw)

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.indices[tenantID]; ok && e.indice.hash == h {
		c.stats.Aciertos++
		c.reloj++
		e.ultimoUso = c.reloj
		return e.indice, nil
	}

	idx, err := c.indexar(doc, h)
	if err != nil {
		return nil, fmt.Errorf("catalogo: tenant %q: %w", tenantID, err)
	}

	c.stats.Construcciones++
	c.reloj++
	c.indices[tenantID] = &entradaCache{indice: idx, ultimoUso: c.reloj}
	c.desalojar()
	return idx, nil
}

// indexar parsea el documento crudo y construye el índice. Se hace CON el candado
// tomado a propósito: sin I/O de por medio es un trabajo acotado (O(artículos), con
// el tope de MaxArticulos encima), y hacerlo dentro es lo que garantiza que dos
// llamadas simultáneas del mismo tenant no construyan dos veces.
func (c *Cache) indexar(doc Documento, h string) (*Indice, error) {
	cat, err := parsear(doc.Raw)
	if err != nil {
		return nil, err
	}
	idx, err := Construir(cat, c.normalizar)
	if err != nil {
		return nil, err
	}
	idx.hash, idx.sello = h, doc.Sello
	return idx, nil
}

// desalojar tira los índices menos usados hasta caber en el tope. Se llama con el
// candado tomado.
func (c *Cache) desalojar() {
	for len(c.indices) > c.max {
		var victima string
		var min uint64
		for k, e := range c.indices {
			if victima == "" || e.ultimoUso < min {
				victima, min = k, e.ultimoUso
			}
		}
		delete(c.indices, victima)
		c.stats.Desalojos++
	}
}

// Estadisticas devuelve una copia de los contadores.
func (c *Cache) Estadisticas() Estadisticas {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// Tamano es cuántos catálogos hay cacheados ahora mismo.
func (c *Cache) Tamano() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.indices)
}

// huella es la identidad del contenido: SHA-256 en hexadecimal de los bytes del
// documento.
//
// No es un hash criptográfico por paranoia sino por AUSENCIA DE COLISIONES: un
// resumen barato (longitud, CRC de los primeros bytes) daría por iguales dos
// catálogos que solo difieren en un precio, y el síntoma sería cobrar el precio
// viejo indefinidamente. El coste es de un documento de como mucho 1 MiB
// (`catalogimport.DefaultMaxJSONBytes`) una vez por job, no por ítem.
func huella(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// parsear lleva el documento crudo al árbol tipado del carrito.
//
// 🔴 SÍ, PAGA EL ROUND-TRIP DE `cart.ParseCatalog` (bytes → map → bytes → tipos):
// esa función recibe `model.Content.Raw`, que es un `map[string]any`, y re-serializa
// por dentro (catalog.go:266). No se evita reimplementando el parseo aquí, y no se
// debe: `ParseCatalog` es el parser TOLERANTE del v2 —descarta lo mal formado con
// aviso en vez de dejar al tenant sin catálogo, rechaza el prefijo de sku reservado
// del sistema, resuelve variantes y combos— y un segundo parser divergiría de él en
// silencio.
//
// Lo que esta tarea cambia no es el coste del parseo: es CUÁNTAS VECES se paga. Una
// por contenido, en vez de una por ítem.
func parsear(raw []byte) (cart.Catalog, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return cart.Catalog{}, fmt.Errorf("el documento del catálogo no es un objeto JSON: %w", err)
	}
	return cart.ParseCatalog(model.Content{Raw: m})
}
