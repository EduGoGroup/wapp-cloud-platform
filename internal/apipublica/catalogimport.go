// Porta internal/publicapi/catalogimport.go @ 724f3035 (324 líneas, entero) y el registro de I14
// (internal/publicapi/publicapi.go @ 724f3035, registerCatalogImport, líneas 1059-1104).
//
// catalogimport.go — LA PUERTA JSON DEL IMPORT DE CATÁLOGO (Plan 041 · Ola 3 · T3.3, D-041.6;
// mapa §2.9, I14): POST /api/v1/catalog/import. El dueño sube el documento de catálogo del
// contrato y, según el modo, ve qué cambiaría o lo aplica. Sus hermanas son la planilla
// (catalogtabular.go, I15) y la plantilla con su prompt (catalogtemplate.go, I16 e I17): las
// cuatro rutas montan con la MISMA condición, y por eso comparten CatalogImportDeps.
//
// En el rojo solo existían los dos puertos, CatalogImportDeps y MountCatalogImport; el handler,
// la respuesta, el tramo común con la planilla y sus auxiliares nacieron con el verde (05 E-4,
// P6). Los nombres no exportados llevan el tema del fichero (catalogImport…); el comentario de
// cada uno dice cómo se llamaba en la cara vieja.

package apipublica

import (
	"context"
	"errors"
	"net/http"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/catalogo/catalogimport"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
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
	// Sin store de contenido, sin versionador o sin resolver de features las rutas NO
	// se montan: mejor un 404 de ruta inexistente que un import que responde 500 a
	// medio camino o, peor, que se aplica sin poder comprobar el plan.
	if !catalogImportMountable(d) {
		return
	}
	mustHaveMW(k, "MountCatalogImport")

	// TRES GUARDIAS, Y NINGUNO SUSTITUYE A OTRO. El scope (content.write) dice
	// "puedes tocar el contenido de este tenant"; la feature (catalog_import) dice "tu
	// plan incluye cargarlo de golpe"; y la auditoría deja constancia. RequireFeature
	// se compone SIEMPRE después de Authenticate y RequirePermission: antes no habría
	// identidad de la que sacar el tenant y el gate cortaría fail-closed a todo el
	// mundo.
	//
	// MISMO SCOPE QUE tenant-content, y por lo mismo que las variables de empresa: el
	// import escribe exactamente donde escribe el PUT genérico (public.tenant_content),
	// así que inventarle un scope propio no protegería nada nuevo y dejaría la ruta
	// inaccesible hasta una migración de grants.
	//
	// A DIFERENCIA de tenant-variables, esto SÍ lleva gate de feature: cargar el
	// catálogo entero validado, con diff y versión, es una capacidad que se vende
	// (taxonomía del Plan 040). Quien no la tenga sigue pudiendo escribir su blob a
	// mano por PUT /api/v1/tenant-content/{ref}.
	canImport := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureCatalogImport)
	c.Handle("POST /api/v1/catalog/import", protect(k, "content.write", "catalog_import",
		canImport(catalogImportHandler(d.Content, d.ContentVersions, catalogImportLimits(d)))))
}

// catalogImportMountable es la condición de montaje de las CUATRO rutas del import (el
// `if d.Content == nil || d.ContentVersions == nil || d.Entitlements == nil` de
// registerCatalogImport en la cara vieja). Vive en una función para que los tres Mount* no
// puedan escribirla distinta.
func catalogImportMountable(d CatalogImportDeps) bool {
	return d.Content != nil && d.ContentVersions != nil && d.Entitlements != nil
}

// catalogImportLimits arma los topes anti-abuso de las dos puertas del import. El de bytes es
// el techo de tenant_content ya resuelto (una sola fuente: ver tenantContentBytes); el de
// artículos viaja tal cual y lo resuelve el validador.
func catalogImportLimits(d CatalogImportDeps) catalogimport.Limits {
	return catalogimport.Limits{
		MaxJSONBytes: tenantContentBytes(d.ContentMaxBytes),
		MaxItems:     d.ImportMaxItems,
	}
}

// Modos del import (D-041.6). validate no escribe NADA; apply re-valida y escribe.
// (importModeValidate / importModeApply en la cara vieja.)
const (
	catalogImportModeValidate = "validate"
	catalogImportModeApply    = "apply"
)

// catalogImportDefaultRef (defaultCatalogRef en la cara vieja) es la ref de tenant_content a la
// que va el catálogo cuando el llamante no dice otra. No es un valor cualquiera: es el que usan
// los flujos del e2e y el que tres tests del motor dan por hecho, así que un default distinto
// dejaría el import escribiendo en una ref que nadie lee.
const catalogImportDefaultRef = "catalogo"

// catalogImportErrors es el cuerpo del 400 por documento inválido: el código
// estable que la pantalla distingue, más TODOS los defectos con su ubicación
// (T3.1). Es el motivo de que el 400 no use writeError: una lista de problemas
// accionables no cabe en un `{"error":"..."}`.
type catalogImportErrors struct {
	Error  string                           `json:"error"`
	Errors []catalogimport.ImportFieldError `json:"errors"`
}

// catalogImportHandler devuelve el handler de
// POST /api/v1/catalog/import?mode=validate|apply&ref=<ref> (D-041.6): recibe el
// documento de catálogo como JSON CRUDO en el cuerpo (el JSON es portátil, INV-05:
// no lleva tenant ni ref) y, según el modo, enseña qué cambiaría o lo aplica.
//
// EL MODO POR DEFECTO ES validate, y es una decisión de seguridad, no de
// comodidad: quien olvide el parámetro ve el diff, no se encuentra el catálogo
// reemplazado. Un modo desconocido es 400 —no se adivina— porque "aply" tecleado
// a las prisas no puede degradar a "no hagas nada" en silencio ni, mucho menos,
// a "escribe".
//
// APPLY RE-VALIDA STATELESS. No hay ticket, ni sesión, ni "confirma lo que
// validaste": el apply vuelve a leer, a validar y a diffear el documento que le
// llega. Así el estado del servidor no depende de una llamada anterior y dos
// pantallas abiertas no pueden confirmar la validación de la otra.
//
// Las respuestas, una a una, están en el contrato de MountCatalogImport.
func catalogImportHandler(cs CatalogImportContentReader, vw CatalogImportVersionWriter, limits catalogimport.Limits) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, ok := catalogImportTargetFrom(w, r, store.VersionSourceImportJSON)
		if !ok {
			return
		}
		doc, code, errBody := catalogImportDecodeBody(r, limits)
		if errBody != nil {
			writeJSON(w, code, errBody)
			return
		}
		catalogImportFinish(w, r, cs, vw, target, doc)
	})
}

// catalogImportTargetFrom resuelve identidad y query de un import, y responde ÉL MISMO
// cuando algo falta (ok=false ⇒ la respuesta ya está escrita).
//
// Los dos caminos del import comparten este preámbulo ENTERO, y por eso está aquí y
// no copiado en cada handler: escrito dos veces, bastaría con que un día uno de los
// dos dejara de comprobar el tenant para que un import escribiera donde no debe.
//
// La comprobación vieja de «store nil ⇒ 500» no se porta: las rutas solo se montan con los dos
// puertos presentes (catalogImportMountable).
func catalogImportTargetFrom(w http.ResponseWriter, r *http.Request, source string) (catalogImportTarget, bool) {
	id, ok := httpapi.IdentityFromContext(r.Context())
	if !ok || id.TenantID == "" {
		writeError(w, http.StatusUnauthorized, "autenticación requerida")
		return catalogImportTarget{}, false
	}
	mode, ref, msg := catalogImportParams(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return catalogImportTarget{}, false
	}
	return catalogImportTarget{tenantID: id.TenantID, mode: mode, ref: ref, source: source}, true
}

// catalogImportParams (importParams en la cara vieja) resuelve el modo y la ref del query
// string. Devuelve el mensaje de error (vacío = todo bien) en vez de escribir la respuesta,
// para que el handler conserve un solo punto de salida por caso y no infle su complejidad.
func catalogImportParams(r *http.Request) (mode, ref, msg string) {
	mode = r.URL.Query().Get("mode")
	if mode == "" {
		mode = catalogImportModeValidate
	}
	if mode != catalogImportModeValidate && mode != catalogImportModeApply {
		return "", "", "mode debe ser " + catalogImportModeValidate + " o " + catalogImportModeApply
	}
	ref = r.URL.Query().Get("ref")
	if ref == "" {
		ref = catalogImportDefaultRef
	}
	return mode, ref, ""
}

// catalogImportDecodeBody (decodeImportBody en la cara vieja) lee el documento con el techo de
// bytes aplicado ANTES de deserializar y lo valida. Devuelve el documento tipado y, si algo
// falla, el status + el cuerpo de error ya armado (nil = todo bien). El cuerpo es `any`
// porque los dos fallos posibles tienen forma distinta: el techo de bytes
// responde un error simple y el documento inválido, la lista completa de
// defectos.
func catalogImportDecodeBody(r *http.Request, limits catalogimport.Limits) (catalogimport.CatalogImport, int, any) {
	raw, err := catalogimport.ReadLimited(r.Body, limits)
	if err != nil {
		if errors.Is(err, catalogimport.ErrDocumentTooLarge) {
			return catalogimport.CatalogImport{}, http.StatusRequestEntityTooLarge,
				tooLarge("el documento", tenantContentBytes(limits.MaxJSONBytes))
		}
		return catalogimport.CatalogImport{}, http.StatusBadRequest,
			errorBody("no se pudo leer el cuerpo")
	}
	doc, verr := catalogimport.Validate(raw, limits)
	if verr != nil {
		return catalogimport.CatalogImport{}, http.StatusBadRequest,
			catalogImportErrors{Error: "validation_failed", Errors: verr.Errors}
	}
	return doc, 0, nil
}
