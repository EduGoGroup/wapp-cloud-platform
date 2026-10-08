// Porta internal/intakes/metricas.go @ 64c181a

// metricas.go — LAS MÉTRICAS DE LA BANDEJA DEL DUEÑO (Plan 044 · T5.2, design §10).
//
// Tres de los cinco eventos de design §10 nacen aquí, y son los TRES que produce
// una persona apretando un botón en su consola: corregir las líneas, aprobar el
// presupuesto y pedirle un dato al cliente. Los otros dos los emite el pipeline, que
// es su único productor y donde viven sus constantes por la misma regla.
//
// 🔴 EN ESTOS PAYLOADS NO ENTRA NI UNA PALABRA DEL CLIENTE NI DEL DUEÑO. Son
// CONTADORES y NÚMEROS DE REVISIÓN, y la forma es la que fija design §10 byte a
// byte. Ni la cotización que escribió el dueño, ni la pregunta que le manda al
// cliente, ni las etiquetas de los artículos, ni el sku: `flow_events` es una tabla
// EN CLARO que se lee entera desde la telemetría y el ADR-0034 no admite ahí ni el
// literal ni nada que identifique. El `contact_id` de la fila es el OPACO de la
// solicitud (ADR-0010/ADR-0017), nunca un número ni un JID.
//
// BEST-EFFORT, Y ESO ES EL CONTRATO ENTERO. Un fallo del emisor se AVISA (Warn, con
// el `intake_id`) y la operación sigue: aprobar un presupuesto NO puede fallar
// porque una fila de telemetría no se escribiera — el pedido ya está confirmado, el
// cliente ya recibió su cotización, y devolverle un 500 al dueño le haría reintentar
// contra un 422. Y sin emisor cableado (R-04) el dominio funciona entero y NO
// publica nada, que no es un error: ni siquiera se calcula el KPI que no se iba a
// publicar.
//
// Este fichero lleva los nombres de los eventos y el puerto. Quien PUBLICA —las
// opciones `WithMetrics`/`WithMetricsClock` y los recuentos— nace con el `Service`.

package intakes

import "context"

// EventLineCorrected, EventApproved y EventInfoRequested son los `flow_events.name`
// de las tres acciones del dueño (design §10). Los literales se declaran AQUÍ porque
// aquí está su único productor: un nombre lógico declarado como constante junto a
// quien lo emite, jamás un literal suelto.
//
// `flow_events.name` es TEXT libre sin CHECK (migración 0009), así que no hay
// migración que dar de alta: lo que hay que dar de alta es la constante.
//
// El payload de cada uno es EXACTAMENTE el de design §10, sin una clave de más (un
// evento de telemetría que gana campos en silencio acaba llevando algo que no
// debía):
//
//   - EventLineCorrected → `{"lines_corrected": 2, "lines_total": 4}`. Cuenta solo
//     líneas de CLIENTE (la de plataforma, de sku con prefijo reservado, no entra por
//     ninguno de los dos lados). `lines_total` es max(|antes|, |después|) y
//     `lines_corrected` es ese total menos las líneas que sobrevivieron IGUALES,
//     comparadas como multiconjunto por (sku, etiqueta, personalización, cantidad,
//     precio) — `AddedAt` no entra, o ninguna casaría nunca. Así corregir, quitar o
//     añadir una línea cuentan los tres y `lines_corrected` jamás supera a
//     `lines_total`; no tocar nada publica igualmente, con cero corregidas.
//   - EventApproved → `{"rev": 3, "elapsed_from_draft_ms": 1900000}`. `rev` es el
//     número de la revisión que escribe la aprobación, nunca una constante. El
//     tiempo corre desde el `created_at` de la revisión `interpreted` de número MÁS
//     BAJO (la primera, no la de un re-análisis) hasta el reloj del proceso. Sin
//     revisión `interpreted` —la solicitud nació del carrito— vale 0 y se registra
//     en Debug, no en Warn: es el curso normal. Un resultado NEGATIVO no se publica:
//     se recorta a 0 y SÍ se avisa.
//   - EventInfoRequested → `{"questions": 1}`. El 1 es literal y es correcto: la
//     puerta manda UNA pregunta; el KPI se calcula con `SUM(questions)`.
const (
	// EventLineCorrected es el PUT de líneas del dueño (Service.ReplaceItems). Era
	// `EventoLineaCorregida` en el paquete viejo.
	EventLineCorrected = "intake_line_corrected"
	// EventApproved es la aprobación del presupuesto (Service.Approve). Era
	// `EventoAprobado` en el paquete viejo.
	EventApproved = "intake_approved"
	// EventInfoRequested es la petición de información al cliente
	// (Service.RequestInfo). Era `EventoInfoPedida` en el paquete viejo.
	EventInfoRequested = "intake_info_requested"
)

// MetricsPublisher es lo ÚNICO que este dominio necesita de la telemetría: publicar
// UNA medición de una solicitud. Lo satisface el publicador del paquete
// `telemetria`, que la deja en `flow_events`. Era `PublicadorDeMetricas` en el
// paquete viejo, y su método, `PublicarMetrica`.
//
// PublishMetric recibe el tenant, el contacto OPACO de la solicitud, el `name` del
// evento (una de las tres constantes de arriba) y sus contadores. Un error suyo
// significa «no se publicó»: quien lo llama lo avisa y NO lo propaga.
//
// 🔴 POR QUÉ NO PIDE LA FILA ENTERA DE `flow_events`, QUE ERA LO OBVIO. El puerto
// gemelo del pipeline sí la pide, tipada con el store de flujos, y aquí no se puede:
// ese store tiene un test in-package que importa este dominio para atar su prefijo
// reservado al de aquí, y Go no admite el ciclo aunque una de las dos patas sea de
// test. Así que el puerto habla el idioma de ESTE dominio —un tenant, un contacto
// opaco, un nombre y unos contadores— y quien traduce eso a una fila es el
// adaptador. Además de compilar, deja la frontera donde le corresponde: `flow_id`,
// `flow_version` y `kind` son vocabulario de la TABLA (migración 0009), no de la
// bandeja del dueño, y la bandeja no tiene por qué saber que existen.
type MetricsPublisher interface {
	PublishMetric(ctx context.Context, tenantID, contactID, name string, payload map[string]any) error
}
