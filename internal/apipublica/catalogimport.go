// Porta internal/publicapi/catalogimport.go @ 724f3035 (324 líneas, entero) y el registro de I14
// (internal/publicapi/publicapi.go @ 724f3035, registerCatalogImport, líneas 1059-1104).
//
// catalogimport.go — LA PUERTA JSON DEL IMPORT DE CATÁLOGO (Plan 041 · Ola 3 · T3.3, D-041.6;
// mapa §2.9, I14): POST /api/v1/catalog/import. El dueño sube el documento de catálogo del
// contrato y, según el modo, ve qué cambiaría o lo aplica. Sus hermanas son la planilla
// (catalogtabular.go, I15) y la plantilla con su prompt (catalogtemplate.go, I16 e I17): las
// cuatro rutas montan con la MISMA condición, y por eso comparten CatalogImportDeps.
//
// En el rojo solo existen los dos puertos, CatalogImportDeps y MountCatalogImport; el handler,
// la respuesta, el tramo común con la planilla y sus auxiliares nacen con el verde (05 E-4, P6).

package apipublica

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// CatalogImportContentReader es el puerto MÍNIMO de lectura que el import consume: el blob
// vigente de una ref de public.tenant_content, que es el lado VIEJO del diff. Lo satisfacen
// *store.PostgresRepository y *store.MemoryRepository del módulo conversación NUEVO
// (internal/modulos/conversacion/store). Acotado al tenant (INV-8): el tenant sale del token,
// jamás del cuerpo.
//
// En la cara vieja el import recibía el TenantContentStore entero (cuatro métodos) y usaba solo
// éste: aquí el puerto es de un método para que el import no pueda borrar ni sobrescribir sin
// versionar.
type CatalogImportContentReader interface {
	// GetTenantContent devuelve el blob JSON crudo de (tenantID, ref). Si la ref no tiene
	// contenido, un error que casa con store.ErrTenantContentNotFound (errors.Is; llega
	// envuelto): es el PRIMER import de esa ref y no un fallo. Cualquier otro error es un fallo
	// del almacén.
	GetTenantContent(ctx context.Context, tenantID, ref string) ([]byte, error)
}

// CatalogImportVersionWriter (CatalogVersionWriter en la cara vieja) es el puerto MÍNIMO del
// versionado que el import consume: archivar el contenido vigente y escribir el nuevo en un
// solo acto. Lo satisfacen *store.PostgresRepository y *store.MemoryRepository del módulo
// conversación NUEVO. Acotado al tenant (INV-8).
//
// Va SEPARADO del puerto de tenant-content aunque el mismo objeto satisfaga los dos: el PUT
// genérico de tenant-content no versiona (D-041.8) y no tiene por qué exigir un método que no
// usa.
type CatalogImportVersionWriter interface {
	// ReplaceTenantContentVersioned archiva el blob vigente de (tenantID, ref) —si lo hay— y
	// escribe blob en su lugar. archived es el número de versión con que quedó guardado el
	// ANTERIOR, o 0 si no había nada que archivar. source es la procedencia del acto: una de las
	// store.VersionSource*.
	ReplaceTenantContentVersioned(ctx context.Context, tenantID, ref string, blob []byte, source string) (archived int, err error)
}

// CatalogImportDeps es lo que necesitan las CUATRO rutas del import de catálogo (I14–I17). En
// la cara vieja eran los campos Content, ContentVersions, ContentMaxBytes e ImportMaxItems de
// publicapi.MediaDeps más Entitlements de publicapi.Deps.
//
// 🔴 LAS CUATRO MONTAN CON LA MISMA CONDICIÓN: Content, ContentVersions y Entitlements, los TRES
// distintos de nil. También la plantilla y el prompt (I16, I17), que no tocan ningún almacén:
// son la primera mitad del mismo acto, y repartir la plantilla de un import que no está montado
// mandaría al operador a llenarla para encontrarse un 404 al subirla (T3.2). Por eso
// MountCatalogImport, MountCatalogTabular y MountCatalogTemplate reciben ESTE tipo y no uno
// propio: con tres tipos la condición podría divergir.
type CatalogImportDeps struct {
	// Content lee el catálogo vigente. nil ⇒ ninguna de las cuatro rutas se monta.
	Content CatalogImportContentReader
	// ContentVersions archiva el vigente y escribe el nuevo. nil ⇒ ninguna se monta.
	ContentVersions CatalogImportVersionWriter
	// Entitlements es el resolver de derechos del módulo acceso NUEVO: el MISMO, con su caché,
	// que gatea el resto de la plataforma. nil ⇒ ninguna se monta (mejor un 404 que un import
	// que se aplica sin poder comprobar el plan).
	Entitlements entitlements.Resolver
	// ContentMaxBytes es el techo del blob de tenant_content (WAPP_TENANT_CONTENT_MAX_BYTES), que
	// es también el del documento y el del archivo subido: una sola fuente, para que un catálogo
	// que pasa el import quepa siempre en su destino. <= 0 ⇒ catalogimport.DefaultMaxJSONBytes
	// (1 MiB).
	ContentMaxBytes int64
	// ImportMaxItems es el tope de artículos de UN documento (WAPP_IMPORT_MAX_ITEMS). <= 0 ⇒
	// catalogimport.DefaultMaxItems (500); lo resuelve el validador.
	ImportMaxItems int
}

// MountCatalogImport registra en c la ruta I14, "POST /api/v1/catalog/import", solo si
// d.Content, d.ContentVersions y d.Entitlements son los TRES distintos de nil. Si falta
// cualquiera, la ruta NO existe (404 de ruta inexistente; no se registra ningún patrón; T-11).
// No monta I15, I16 ni I17 (los montan MountCatalogTabular y MountCatalogTemplate, con esta
// misma condición).
//
// TRES GUARDIAS, Y NINGUNO SUSTITUYE A OTRO. Cadena W de Common, permiso "content.write",
// recurso de auditoría "catalog_import" y, POR DENTRO de la auditoría, el gate de la feature
// "catalog_import" (entitlements.RequireFeature, fail-closed):
//
//   - 401 sin token; 403 {"error":"permiso denegado"} sin el permiso o con un token sin empresa;
//     ningún registro de auditoría en esos dos;
//   - con el permiso y SIN la feature ⇒ 403 con EXACTAMENTE
//     {"error":"feature_not_enabled","feature":"catalog_import"}; ningún puerto se consulta y,
//     como el gate va por dentro de la auditoría, queda UN registro "failure" con Meta
//     {"status":403};
//   - sin el permiso Y sin la feature ⇒ el 403 es el del permiso, no el del gate;
//   - "content.read" no basta. 🔴 Se exige "content.write" TAMBIÉN para mode=validate, que no
//     escribe: elegir el permiso según un parámetro de la URL haría que la autorización
//     dependiera de una entrada del usuario. Una ruta, un permiso: el más fuerte;
//   - UN registro por petición que pasa permiso: "success" con el 200 —también en validate—,
//     "failure" con Meta {"status":<código>} en cualquier 4xx o 5xx.
//
// LA PETICIÓN. `POST /api/v1/catalog/import?mode=validate|apply&ref=<ref>` con el documento del
// contrato como JSON CRUDO en el cuerpo (el JSON es portátil, INV-05: no lleva tenant ni ref).
// El Content-Type no se mira.
//
//   - "mode": si falta o viene vacío ⇒ validate. 🔴 Es una decisión de seguridad, no de
//     comodidad: quien olvide el parámetro ve el diff, no se encuentra el catálogo reemplazado.
//     Solo valen los literales "validate" y "apply" (ni mayúsculas ni espacios); otro ⇒ 400
//     {"error":"mode debe ser validate o apply"} —no se adivina: "aply" no puede degradar a "no
//     hagas nada" ni a "escribe"—, decidido ANTES de leer el cuerpo y sin consultar ningún
//     puerto;
//   - "ref": si falta o viene vacía ⇒ "catalogo" (la ref que leen los flujos). Otra se usa TAL
//     CUAL para leer y para escribir;
//   - el tenant es SIEMPRE el del token (INV-8): uno en la query o en el cuerpo no cuenta, y un
//     import de un tenant no lee ni escribe ni archiva nada de otro;
//   - el cuerpo se lee con el techo de bytes aplicado ANTES de deserializar
//     (catalogimport.ReadLimited): un documento de exactamente el techo pasa; con un byte más ⇒
//     413 {"error":"el documento excede el tamaño máximo de N bytes","max_bytes":N}, con N =
//     el techo efectivo (d.ContentMaxBytes, o 1048576 si es <= 0). Un fallo de lectura del
//     cuerpo ⇒ 400 {"error":"no se pudo leer el cuerpo"};
//   - el documento se valida con catalogimport.Validate y los límites
//     {MaxJSONBytes: el techo efectivo, MaxItems: d.ImportMaxItems}. Si no valida ⇒ 400
//     {"error":"validation_failed","errors":[…]} con TODOS los defectos del validador tal cual
//     (su `field`, su `reason` y sus índices): es el motivo de que este 400 no sea un
//     {"error":"…"} pelado. Un cuerpo vacío o que no es JSON es un documento inválido más. Con
//     un 400 o un 413 NO se lee el catálogo vigente ni se escribe nada, tampoco en apply.
//
// EL DIFF. El lado viejo es el blob vigente de (tenant, ref), leído por d.Content y parseado
// con el MISMO parser del motor (catalogo.ParseCatalog, D-041.7). Tres desenlaces:
//
//   - la ref no tiene contenido (store.ErrTenantContentNotFound, también envuelto) ⇒ catálogo
//     vacío y SIN aviso: es el primer import y todo el documento sale como `added`. Jamás un 500;
//   - hay contenido pero no se puede interpretar como catálogo (no es un objeto JSON, o lo es y
//     el parser lo rechaza) ⇒ catálogo vacío MÁS UN AVISO, añadido al FINAL de
//     "diff"."current_warnings": «catálogo vigente: la ref tiene contenido, pero no se pudo
//     interpretar como catálogo; la comparación se hizo contra un catálogo vacío y por eso todo
//     aparece como nuevo». Sin él, «todo nuevo» se leería como «no pierdo nada»;
//   - cualquier otro error de d.Content ⇒ 500 {"error":"no se pudo leer el catálogo vigente"},
//     sin repetir el error del almacén y SIN escribir. Aquí NO se degrada a «catálogo vacío»:
//     enseñar «todo nuevo» porque la BD no respondió empujaría a confirmar un reemplazo a ciegas.
//
// El diff es catalogimport.DiffCatalog(vigente, documento) tal cual, y se calcula SIEMPRE
// (también en apply), porque es lo que se responde: la pantalla enseña lo mismo que confirmó.
//
// LA RESPUESTA. 200, Content-Type application/json, con el MISMO objeto en validate y en apply,
// en este orden: {"mode","ref","applied","items","diff","archived_version"?}.
//
//   - "mode" y "ref" son los EFECTIVOS (con sus defectos aplicados);
//   - "items" es cuántos artículos trae el documento subido, sumando todas las categorías;
//   - "applied" es false SIEMPRE en validate —es la garantía de que mirar no cambia nada:
//     d.ContentVersions no se llama y el blob vigente queda byte a byte— y true en apply;
//   - "archived_version" es el número con que quedó guardado el catálogo ANTERIOR. Se OMITE
//     cuando vale 0: en validate y en el primer import de una ref (no había nada que archivar);
//   - 🔴 NO lleva "document": quien sube un JSON ya lo tiene (lo lleva solo la planilla, I15);
//   - ninguna respuesta lleva el tenant.
//
// APPLY RE-VALIDA STATELESS. No hay ticket, ni sesión, ni «confirma lo que validaste»: el apply
// vuelve a leer, a validar y a diffear el documento que le llega, y entonces llama UNA vez a
// d.ContentVersions.ReplaceTenantContentVersioned(tenant del token, ref, blob,
// store.VersionSourceImportJSON = "import_json"). La procedencia la fija el CAMINO, no el
// llamante.
//
//   - el blob es el `catalog` del documento YA NORMALIZADO por el validador, serializado con
//     encoding/json tal cual (la forma v2 que consume el motor, D-041.5): sin "format", sin
//     "version", sin "source" y sin campos ajenos. No son los bytes que llegaron;
//   - reaplicar el MISMO documento NO es un no-op: el diff sale vacío (todo `unchanged`), el blob
//     vigente no cambia y SÍ queda una versión más (decisión declarada de T3.3);
//   - un fallo del versionador ⇒ 500 {"error":"no se pudo aplicar el catálogo"}, sin repetir el
//     error. (Un fallo al serializar el blob daría 500 {"error":"no se pudo serializar el
//     catálogo"}; con un documento validado no se alcanza.)
//
// Defensa que los tokens de sharedjwt no alcanzan (RequirePermission corta antes): una identidad
// sin empresa que llegara al handler recibiría 401 {"error":"autenticación requerida"} sin tocar
// ningún puerto.
//
// No se porta la rama vieja «store nil ⇒ 500 store de contenido no configurado»: la ruta solo se
// monta con los dos puertos presentes, así que era inalcanzable.
//
// Fallo de cableado: k.MW nil con las tres dependencias presentes hace panic AL MONTAR, con un
// mensaje que nombra MountCatalogImport (ver Common). Si falta una dependencia no se monta nada
// y k ni se mira.
func MountCatalogImport(c *Cara, k Common, d CatalogImportDeps) {
	panic(pendiente.Implementar("apipublica.MountCatalogImport"))
}
