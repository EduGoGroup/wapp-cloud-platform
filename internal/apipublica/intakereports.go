// Porta el registro de G7, G9 y G10 de internal/publicapi/publicapi.go @ ed60c24
// (registerIntakes: la guarda de la línea 649, los gates de 652-654, G7 en 746-769 y G9/G10 en
// 787-791).
//
// intakereports.go — EL MONTAJE DE LAS LECTURAS DE LA BANDEJA QUE SALEN DE ELLA (mapa §2.7):
// el export (G9), el resumen (G10) y la sugerencia de cotización (G7). Los tres handlers viven
// en export.go, summary.go y quotesuggestion.go; el plazo de escritura de G7, en
// writedeadline.go. Este fichero no existía en la spec FX: nace porque la bandeja se monta por
// frentes y cada frente trae su Mount* (el resto de G lo monta MountIntakes).
//
// En el rojo solo existían el puerto, IntakeReportsDeps y MountIntakeReports (05 E-4, P6).

package apipublica

import (
	"context"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// IntakeReportService es el puerto de LECTURA EN BLOQUE de la bandeja que consumen el export y
// el resumen. Lo satisface *intakes.Service del módulo solicitudes NUEVO.
//
// 🔴 ES UN SUBCONJUNTO ESTRICTO del servicio, y la ausencia es el mecanismo: aquí no hay ni
// aprobar, ni corregir, ni descartar. Un handler de este fichero no puede escribir en la bandeja
// ni por descuido.
//
// El tenant es un ARGUMENTO que sale del token, nunca del filtro (INV-7 / INV-8).
type IntakeReportService interface {
	// ListDetails devuelve TODAS las solicitudes de tenantID que casan con f, con sus líneas y
	// sin paginar, o intakes.ErrTooLarge si son más de intakes.MaxExportIntakes.
	ListDetails(ctx context.Context, tenantID string, f intakes.Filter) ([]intakes.Detail, error)
	// Summary agrega esas mismas solicitudes (misma cota, mismo intakes.ErrTooLarge) y fecha el
	// resultado con el reloj que el servicio tenga inyectado (D-F6-5, intakes.WithClock).
	Summary(ctx context.Context, tenantID string, f intakes.Filter) (intakes.Summary, error)
}

// IntakeReportsDeps es lo que G7, G9 y G10 necesitan. En la cara vieja eran los campos Intakes,
// Entitlements y QuoteSuggestions de publicapi.Deps; QuoteWriteDeadline y Now no existían (el
// plazo era una constante del paquete y el reloj, time.Now a secas).
type IntakeReportsDeps struct {
	// Intakes lee la bandeja en bloque. nil ⇒ no se monta NINGUNA de las tres rutas.
	Intakes IntakeReportService
	// Entitlements es el resolver de derechos del módulo acceso NUEVO, el MISMO (una sola caché)
	// que gatea el resto de la plataforma. nil ⇒ no se monta NINGUNA de las tres rutas.
	Entitlements entitlements.Resolver
	// QuoteSuggestions es el generador de la cotización sugerida. nil ⇒ G7 no se monta (G9 y G10
	// sí).
	QuoteSuggestions QuoteSuggester
	// QuoteWriteDeadline es el plazo de ESCRITURA de G7: cuánto tiempo, desde que entra la
	// petición, puede el servidor seguir escribiendo su respuesta. Lo calcula el arranque como
	// el plazo de la llamada al modelo que le pone a quotetext.WithTimeout más su margen (mapa
	// §4.3: derivado, no copiado; en la cara vieja eran 48 s + 12 s). Obligatorio (> 0) cuando
	// QuoteSuggestions no es nil; sin QuoteSuggestions no se mira.
	QuoteWriteDeadline time.Duration
	// Now es el reloj de la cara: fecha el nombre del fichero del export y es el «ahora» desde
	// el que se cuenta QuoteWriteDeadline. nil ⇒ time.Now. (El generated_at del resumen NO sale
	// de aquí: lo pone el servicio, con su propio reloj inyectado.)
	Now func() time.Time
}

// MountIntakeReports registra en c, solo si d.Intakes y d.Entitlements son los dos distintos de
// nil:
//
//   - G9  "GET /api/v1/intakes/export"
//   - G10 "GET /api/v1/intakes/summary.json"
//   - G7  "POST /api/v1/intakes/{id}/quote-suggestion", solo si ADEMÁS d.QuoteSuggestions no es
//     nil.
//
// Lo que no se monta no existe (404 de ruta inexistente; T-11): es más honesto que una ruta que
// responde 500, y G7 sin generador no deja a G9 ni a G10 sin montar.
//
// Las tres llevan cadena R con permiso "intakes.read" (ver Common: 401 sin token; 403
// {"error":"permiso denegado"} sin el permiso o con un token sin empresa; NINGÚN registro de
// auditoría, tampoco en el camino feliz — también G7, que es POST pero no escribe nada) y, POR
// DENTRO de ella, sus gates de feature (entitlements.RequireFeature, fail-closed: un resolver
// que falla corta con el mismo 403 del gate, nunca con 500):
//
//   - G9 y G10: "intakes_export". Sin ella ⇒ 403 con EXACTAMENTE
//     {"error":"feature_not_enabled","feature":"intakes_export"}; el puerto no se consulta.
//     "cart_basic" NO la sustituye;
//   - G7: "cart_basic" Y "llm_intake", en ese orden (se opera sobre un pedido, y es «la máquina
//     que redacta el borrador sola», que se vende aparte). Sin "cart_basic" ⇒ el 403 nombra
//     "cart_basic" (tenga o no la otra); con ella y sin "llm_intake" ⇒ nombra "llm_intake". El
//     generador no se consulta;
//   - sin el permiso Y sin la feature ⇒ el 403 es el del permiso, no el del gate.
//
// Lo que promete G7 lo dice QuoteSuggester (quotesuggestion.go). G9 y G10 comparten esto:
//
//   - el puerto recibe SIEMPRE el tenant del token (INV-7 / INV-8) y el filtro de la query, que
//     es EL MISMO de la lista de la bandeja (from, to, status, session…): si divergieran, el
//     fichero no contendría lo que la bandeja enseña. Un filtro mal escrito ⇒ 400 con el mensaje
//     del filtro (p. ej. {"error":"status desconocido"}) y el puerto no se consulta;
//   - no paginan. intakes.ErrTooLarge (también envuelto) ⇒ 422 {"error":"el filtro abarca más
//     de 5000 solicitudes: acótalo con from/to"} (la cifra es intakes.MaxExportIntakes): nunca
//     se recorta en silencio;
//   - el contexto que recibe el puerto es el de la petición, SIN plazo añadido por la cara (son
//     lecturas en bloque: el 1,5 s de las lecturas previas al envío las abortaría);
//   - una identidad sin empresa que llegara al handler ⇒ 401 {"error":"autenticación
//     requerida"} (defensa que los tokens de sharedjwt no alcanzan).
//
// G9, el EXPORT (Plan 041 · T1.2, REQ-03, D-041.15):
//
//   - "format": si falta o viene vacío ⇒ csv; solo valen los literales "csv" y "xlsx" (ni
//     mayúsculas ni espacios). Otro ⇒ 400 {"error":"format inválido: usa csv o xlsx"}, que se
//     decide ANTES de mirar el filtro y sin consultar el puerto;
//   - cualquier otro fallo de ListDetails ⇒ 500 {"error":"no se pudieron leer las
//     solicitudes"}, que no repite el error del puerto;
//   - 200 con el fichero ENTERO, generado en memoria antes de escribir la primera cabecera (un
//     fallo a media serialización no puede dejar un 200 con un fichero corrupto). Cabeceras:
//     Content-Type "text/csv; charset=utf-8" o
//     "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"; Content-Disposition
//     `attachment; filename="intakes-AAAAMMDD-HHMMSS.<format>"`, con el instante de d.Now() en
//     UTC (dos exports del mismo día no se pisan); Content-Length = los bytes del cuerpo. Un
//     error (400, 422, 500) NO lleva Content-Disposition;
//   - las columnas son CONTRATO POSICIONAL, estas 13 y en este orden: intake_id, created_at,
//     status, session_id, contact_ref, intake_total, customer_note | sku, label, customization,
//     qty, unit_price, line_total;
//   - una fila POR LÍNEA, en el orden del puerto, con las 7 columnas de la solicitud REPETIDAS
//     en cada una (desnormalizado). Una solicitud SIN líneas produce igualmente UNA fila, con
//     las 6 columnas de línea vacías: no desaparece del fichero;
//   - created_at va en UTC, RFC 3339 con segundos; status sale tal como lo da el puerto (ya
//     normalizado por el dominio); contact_ref es el opaco TAL CUAL (ADR-0010); line_total es
//     qty × unit_price y nada más (INV-13). No hay ninguna columna del comprador;
//   - CSV: BOM UTF-8 (sin él Excel en Windows destroza cada tilde), fin de línea CRLF y comillas
//     RFC 4180; los números con punto decimal, sin separador de miles y sin ceros de relleno
//     (18000, 0.5, -3). 🔴 Una celda de TEXTO cuyo PRIMER carácter es `=`, `+`, `-`, `@`,
//     tabulador o retorno de carro sale prefijada con `'` (inyección de fórmulas: `customer_note`
//     y `customization` las escribe el cliente final). Solo el primer carácter cuenta, y un
//     NÚMERO negativo no se prefija (dejaría de sumarse);
//   - XLSX: un libro con UNA hoja, "solicitudes"; la fila 1 es la cabecera. El texto se escribe
//     como CADENA —íntegro, sin apóstrofo, y nunca como fórmula—, los números como números, y
//     una celda vacía no se escribe (ni cero ni cadena).
//
// G10, el RESUMEN (Plan 041 · T1.3, REQ-04, D-041.15), pensado para pegárselo a un LLM EXTERNO:
//
//   - cualquier otro fallo de Summary ⇒ 500 {"error":"no se pudo resumir las solicitudes"};
//   - 200 con, en este orden: {"generated_at", "range":{"from","to"},
//     "totals":{"intakes","revenue","by_status"}, "top_items":[{"sku","label","qty_total",
//     "revenue"}], "intakes":[{"id","status","created_at","total","customer_note",
//     "items":[{"sku","label","customization","qty","unit_price"}]}]}, todo tal como lo da el
//     puerto y en su orden;
//   - generated_at es Summary.GeneratedAt (el reloj inyectado en el SERVICIO, D-F6-5), y como
//     created_at va en UTC, RFC 3339 con segundos. "range" es el REALMENTE aplicado: una cota sin
//     pedir viaja como cadena vacía;
//   - nada es null: "by_status" es {} y "top_items", "intakes" e "items" son [] cuando no hay
//     nada, también si el puerto los da nil;
//   - 🔴 CERO PII: ni contact_id —aunque sea opaco—, ni session_id, ni nada del comprador. La
//     proyección se construye campo a campo. `customization` y `customer_note` SÍ viajan.
//
// 🔴 EL PLAZO DE ESCRITURA DE G7 (T-6 de FX). G7 es la única ruta que espera a un modelo dentro
// de la petición, y con el WriteTimeout del servidor su respuesta no cabría por el cable. Por
// eso, y SOLO en G7:
//
//   - antes de autenticar y de consultar al generador, se le pone a la conexión el plazo de
//     escritura d.Now() + d.QuoteWriteDeadline (http.ResponseController.SetWriteDeadline);
//   - el envoltorio va POR FUERA de la cadena R: el plazo llega al ResponseWriter que recibe la
//     Cara también con access-log (Common.Log no nil), cuyo ResponseWriter no desenvuelve. Por
//     ir por fuera se pone también en el 401, el 403 y el 400 de G7;
//   - si el ResponseWriter no admite plazos, la petición se sirve IGUAL —sin plazo extendido la
//     ruta va mal en el caso lento y bien en el rápido; cortarla cambiaría un defecto de entrega
//     por una caída— y queda UNA línea Warn en Common.Log: "no se pudo extender el plazo de
//     escritura de la sugerencia de cotización: la respuesta larga volverá a no caber por el
//     cable", con los campos "path" (r.URL.Path, nunca la query), "plazo" (el plazo, como
//     texto) y "error". Con Common.Log nil se sirve igual, en silencio;
//   - G9 y G10 no tocan el plazo de escritura.
//
// Fallos de cableado, los dos con panic AL MONTAR y un mensaje que nombra MountIntakeReports:
//
//   - k.MW nil con d.Intakes y d.Entitlements presentes (ver Common);
//   - d.QuoteSuggestions presente con d.QuoteWriteDeadline <= 0: un plazo cero dejaría a G7
//     escribiendo contra un plazo ya vencido —ninguna respuesta llegaría—, y la cara no tiene un
//     valor por defecto que poner sin copiar el reloj que solo el arranque conoce. Sin
//     QuoteSuggestions, un QuoteWriteDeadline cero no es un error.
func MountIntakeReports(c *Cara, k Common, d IntakeReportsDeps) {
	// Sin servicio o sin resolver de features NADA de esto se monta: un 404 de ruta inexistente es
	// más honesto que un export que responde 500 (T-11). Es la guarda de registerIntakes en la
	// cara vieja.
	if d.Intakes == nil || d.Entitlements == nil {
		return
	}
	mustHaveMW(k, "MountIntakeReports")
	now := d.Now
	if now == nil {
		now = time.Now
	}

	// Export y resumen (Plan 041 · T1.2/T1.3, REQ-03/REQ-04). El gate es `intakes_export`, no
	// `cart_basic`: sacar la bandeja a un fichero es una capacidad que se vende aparte.
	canExport := entitlements.RequireFeature(d.Entitlements, entitlements.FeatureIntakesExport)
	c.Handle("GET /api/v1/intakes/export", protectRead(k, "intakes.read",
		canExport(exportIntakesHandler(d.Intakes, now))))
}
