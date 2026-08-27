package intakes

import (
	"context"
	"strings"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"
)

// ============================================================================
// metricas.go — LAS MÉTRICAS DE LA BANDEJA DEL DUEÑO (Plan 044 · T5.2, design §10)
// ============================================================================
//
// Tres de los cinco eventos de design §10 nacen aquí, y son los TRES que produce
// una persona apretando un botón en su consola: corregir las líneas, aprobar el
// presupuesto y pedirle un dato al cliente. Los otros dos los emite el pipeline
// (internal/intake/stages/draft.go), que es su único productor y donde viven sus
// constantes por la misma regla que se aplica aquí.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EN ESTOS PAYLOADS NO ENTRA NI UNA PALABRA DEL CLIENTE NI DEL DUEÑO
// ════════════════════════════════════════════════════════════════════════════
//
// Son CONTADORES y NÚMEROS DE REVISIÓN, y la forma es la que fija design §10 byte a
// byte. Ni la cotización que escribió el dueño, ni la pregunta que le manda al
// cliente, ni las etiquetas de los artículos, ni el sku: `flow_events` es una tabla
// EN CLARO que se lee entera desde la telemetría y el ADR-0034 no admite ahí ni el
// literal ni nada que identifique. El `contact_id` de la fila es el OPACO de la
// solicitud (ADR-0010/ADR-0017), nunca un número ni un JID.
//
// Lo custodia metricas_test.go, que barre el JSON YA SERIALIZADO —no las claves una
// a una— con los literales de la escena dentro.
//
// ════════════════════════════════════════════════════════════════════════════
// BEST-EFFORT, Y ESO ES EL CONTRATO ENTERO
// ════════════════════════════════════════════════════════════════════════════
//
// Un fallo del emisor se avisa y la operación sigue. Es la MISMA regla que ya
// gobierna al notificador, a los dos recordatorios perezosos y al empuje al CRM
// (notifier.go, regla 1): aprobar un presupuesto NO puede fallar porque una fila de
// telemetría no se escribiera — el pedido ya está confirmado, el cliente ya recibió
// su cotización, y devolverle un 500 al dueño le haría reintentar contra un 422.

// EventoLineaCorregida, EventoAprobado y EventoInfoPedida son los `flow_events.name`
// de las tres acciones del dueño (design §10). Los literales se declaran AQUÍ porque
// aquí está su único productor, que es la misma regla que siguen los efectos de los
// módulos (cart/effects.go) y la métrica del borrador (stages/draft.go): un nombre
// lógico declarado como constante junto a quien lo emite, jamás un literal suelto.
//
// `flow_events.name` es TEXT libre sin CHECK (migración 0009), así que no hay
// migración que dar de alta: lo que hay que dar de alta es la constante.
const (
	// EventoLineaCorregida es el PUT de líneas del dueño (Service.ReplaceItems).
	EventoLineaCorregida = "intake_line_corrected"
	// EventoAprobado es la aprobación del presupuesto (Service.Approve).
	EventoAprobado = "intake_approved"
	// EventoInfoPedida es la petición de información al cliente (Service.RequestInfo).
	EventoInfoPedida = "intake_info_requested"
)

// PublicadorDeMetricas es lo ÚNICO que este dominio necesita de la telemetría:
// publicar UNA medición de una solicitud. Lo satisface `*telemetria.Publicador`
// (internal/intakes/telemetria), que la deja en `flow_events`.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ NO ES `InsertFlowEvent`, QUE ERA LO OBVIO
// ════════════════════════════════════════════════════════════════════════════
//
// El puerto gemelo del pipeline (`stages.EscritorEvento`) sí pide la fila entera,
// tipada con `flujos/store.FlowEvent`, y aquí no se puede: **este paquete no puede
// importar `internal/flujos/store`**. No es una preferencia, es un CICLO que rompe el
// build — `flujos/store` tiene un test IN-PACKAGE (reserved_prefix_test.go) que
// importa `intakes` para atar su prefijo reservado al de aquí, y Go no admite el
// ciclo aunque una de las dos patas sea de test. Verificado: con el puerto tipado,
// `go vet ./...` corta con «import cycle not allowed in test».
//
// Así que el puerto habla el idioma de ESTE dominio —un tenant, un contacto opaco, un
// nombre y unos contadores— y quien traduce eso a una fila de `flow_events` es el
// adaptador. Además de compilar, deja la frontera donde le corresponde: `flow_id`,
// `flow_version` y `kind` son vocabulario de la TABLA (migración 0009), no de la
// bandeja del dueño, y la bandeja no tiene por qué saber que existen.
type PublicadorDeMetricas interface {
	PublicarMetrica(ctx context.Context, tenantID, contactID, name string, payload map[string]any) error
}

// WithMetrics cablea la telemetría de la bandeja (Plan 044 · T5.2). Sin esta opción
// el servicio funciona igual y NO publica nada: es lo mismo que prometen WithNotifier
// y WithCRMPusher, y es lo que mantiene honestos a los tests de dominio que solo
// quieren mover una solicitud.
//
// LLEVA EL LOG COMO SEGUNDO ARGUMENTO, y no es adorno: el emisor puede fallar y su
// fallo NO puede propagarse (ver la cabecera). Un colaborador best-effort sin dónde
// avisar es un fallo silencioso, que es exactamente la clase de defecto que este plan
// lleva olas pagando. Va en la MISMA opción —y no en una segunda que haya que
// recordar— para que no exista el estado «emisor cableado, log olvidado». Un `log`
// nil deja el `logger.Default()` que pone NewService.
func WithMetrics(publicador PublicadorDeMetricas, log logger.Logger) Option {
	return func(s *Service) {
		s.metricas = publicador
		if log != nil {
			s.log = log
		}
	}
}

// WithMetricsClock sustituye el reloj con el que se mide `elapsed_from_draft_ms`.
// Existe para los tests, por el mismo motivo que `stages.ConReloj`: ese campo es la
// resta de dos instantes y con `time.Now` de verdad no se puede afirmar un número
// —solo un rango—, que es justo la clase de aserción que deja pasar un cero.
//
// Pasar nil no hace nada: el servicio no se queda sin reloj.
func WithMetricsClock(ahora func() time.Time) Option {
	return func(s *Service) {
		if ahora != nil {
			s.ahora = ahora
		}
	}
}

// publicarMetrica publica UNA medición. BEST-EFFORT: sin publicador cableado no hace
// nada, y un fallo suyo se avisa y no se propaga (ver la cabecera).
//
// El `name` y el `payload` los pone cada llamante porque son su contrato; lo que este
// método garantiza es lo COMÚN —que el contacto que viaja es el OPACO de la solicitud
// y jamás otro dato— para que ninguna de las tres puertas pueda firmarla distinto.
func (s *Service) publicarMetrica(ctx context.Context, tenantID string, in Intake, name string, payload map[string]any) {
	if s.metricas == nil {
		return
	}
	if err := s.metricas.PublicarMetrica(ctx, tenantID, in.ContactID, name, payload); err != nil {
		// Warn y no Error: lo que se pierde es UNA fila de telemetría, y la acción del
		// dueño ocurrió entera. El intake_id va en el log porque es lo único con lo que
		// se puede reconstruir a mano lo que faltó en un panel.
		s.log.Warn("intakes: no se pudo publicar la métrica de la bandeja; la acción del dueño SÍ se aplicó",
			"name", name, "intake_id", in.ID, "error", err.Error())
	}
}

// métricaDeCorrección publica `intake_line_corrected` con la forma de design §10:
// `{"lines_corrected": 2, "lines_total": 4}`.
func (s *Service) métricaDeCorrección(ctx context.Context, tenantID string, in Intake, antes, después []Item) {
	corregidas, total := recuentoDeCorrección(antes, después)
	s.publicarMetrica(ctx, tenantID, in, EventoLineaCorregida, map[string]any{
		"lines_corrected": corregidas,
		"lines_total":     total,
	})
}

// métricaDeAprobación publica `intake_approved` con la forma de design §10:
// `{"rev": 3, "elapsed_from_draft_ms": 1900000}`.
//
// 🔴 LA GUARDA SE REPITE AQUÍ, Y NO ES REDUNDANTE: Go evalúa los ARGUMENTOS antes de
// entrar en la función, así que el mapa —y con él el recorrido de las revisiones de
// `desdeElBorrador`— se construía ENTERO aunque `publicarMetrica` fuera a salir por
// su propia guarda. Eso hacía dos cosas mal: un servicio sin telemetría cableada
// calculaba igual el KPI (contradiciendo lo que promete WithMetrics) y encima podía
// dejar rastro en el log de algo que nadie iba a publicar.
//
// Sus dos hermanas no la llevan porque sus payloads son literales y contadores ya
// calculados: ahí «evaluar el argumento» no cuesta nada. Esta es la única que lee.
func (s *Service) métricaDeAprobación(ctx context.Context, tenantID string, in Intake, revisionNo int, revisiones []Revision) {
	if s.metricas == nil {
		return
	}
	s.publicarMetrica(ctx, tenantID, in, EventoAprobado, map[string]any{
		"rev":                   revisionNo,
		"elapsed_from_draft_ms": s.desdeElBorrador(in, revisiones).Milliseconds(),
	})
}

// métricaDeInformación publica `intake_info_requested` con la forma de design §10:
// `{"questions": 1}`.
//
// EL 1 ES LITERAL Y ESO ES CORRECTO: `RequestInfo` manda UNA pregunta y solo una
// —«jamás sale sola» es su criterio, y una lista de preguntas no cabe en su firma—,
// así que el campo cuenta las preguntas de ESTE acto y hoy siempre vale uno. Se
// publica igualmente porque el KPI se calcula con `SUM(questions)` sobre las filas del
// periodo: contar filas y sumar el campo dan lo mismo hoy y seguirían dándolo el día
// que la puerta admita varias.
func (s *Service) métricaDeInformación(ctx context.Context, tenantID string, in Intake) {
	s.publicarMetrica(ctx, tenantID, in, EventoInfoPedida, map[string]any{
		"questions": 1,
	})
}

// desdeElBorrador mide cuánto tardó el dueño en aprobar DESDE QUE TUVO EL BORRADOR
// DELANTE, que es el KPI que design §10 cuelga de este evento.
//
// ════════════════════════════════════════════════════════════════════════════
// LOS DOS EXTREMOS, DICHOS ENTEROS
// ════════════════════════════════════════════════════════════════════════════
//
//	INICIO → el `created_at` de la PRIMERA revisión `interpreted`, que es el instante
//	         en que el pipeline dejó el borrador escrito. Lo pone la BD.
//	FIN    → `s.ahora()`, el reloj de Go del proceso que atiende el POST.
//
// Son DOS RELOJES y se dice en vez de esconderse, exactamente como en
// `stages.Draft.transcurrido`: se restan igual porque la magnitud que se mide es de
// minutos u horas (el dueño mirando su bandeja) y la deriva entre dos relojes con NTP
// es ruido frente a eso. Lo que NO se hace con estos dos instantes es DECIDIR nada.
//
// 🔴 SIN REVISIÓN `interpreted` DEVUELVE 0, Y NO ES UNA ANOMALÍA. Una solicitud que NO
// nació del pipeline —el cierre de un carrito la crea directamente— no tiene borrador
// que cronometrar, y el único valor honesto es cero: inventar el `created_at` de la
// cabecera mediría «desde que existe el pedido», que es otra cosa con el mismo nombre.
// El runbook filtra por `> 0` justamente por esto.
//
// 🔧 ESE CASO SE REGISTRA EN DEBUG Y NO EN WARN, y la corrección importa: es el curso
// NORMAL de toda solicitud que viene del carrito, así que un Warn le pondría una línea
// de alarma a cada aprobación de un pedido perfectamente sano — ruido que enseña a
// ignorar el log. Warn se queda para lo que sí es raro: el negativo de abajo.
//
// Un resultado NEGATIVO tampoco se publica: se recorta a 0 y SÍ se avisa. Un tiempo
// negativo en un panel no se lee como «relojes desajustados», se lee como un bug.
func (s *Service) desdeElBorrador(in Intake, revisiones []Revision) time.Duration {
	inicio, hay := instanteDelBorrador(revisiones)
	if !hay {
		s.log.Debug("intakes: la solicitud aprobada no nació del pipeline (no tiene revisión "+
			"interpretada), así que elapsed_from_draft_ms se publica como 0",
			"name", EventoAprobado, "intake_id", in.ID)
		return 0
	}
	d := s.ahora().Sub(inicio)
	if d < 0 {
		s.log.Warn("intakes: elapsed_from_draft_ms salió NEGATIVO; el reloj de la base y el del proceso están desalineados",
			"name", EventoAprobado, "intake_id", in.ID, "desfase_ms", d.Milliseconds())
		return 0
	}
	return d
}

// instanteDelBorrador devuelve el `created_at` de la revisión `interpreted` de número
// MÁS BAJO. Se busca por `RevisionNo` y no por la posición en el slice: el orden del
// slice es cosa de cada store y este cálculo no puede depender de él.
//
// Se toma la PRIMERA y no la última a propósito: un re-análisis escribe una segunda
// revisión `interpreted` horas después, y medir desde ella diría que el dueño aprobó
// en dos minutos un pedido que llevaba dos días en su bandeja.
func instanteDelBorrador(revisiones []Revision) (time.Time, bool) {
	var (
		mejor Revision
		hay   bool
	)
	for _, rev := range revisiones {
		if rev.Kind != RevisionKindInterpreted || rev.CreatedAt.IsZero() {
			continue
		}
		if !hay || rev.RevisionNo < mejor.RevisionNo {
			mejor, hay = rev, true
		}
	}
	return mejor.CreatedAt, hay
}

// recuentoDeCorrección compara las líneas de CLIENTE de antes y de después de una
// edición manual y devuelve cuántas cambiaron sobre cuántas hubo implicadas.
//
// ════════════════════════════════════════════════════════════════════════════
// LA DEFINICIÓN, QUE ES LA TAREA ENTERA (el KPI es «% de líneas corregidas»)
// ════════════════════════════════════════════════════════════════════════════
//
//	lines_total     = max(|antes|, |después|) — las líneas IMPLICADAS en el acto.
//	lines_corrected = lines_total − |intersección| — las que no sobrevivieron iguales.
//
// La intersección es de MULTICONJUNTOS sobre (sku, etiqueta, personalización,
// cantidad, precio): dos líneas pueden compartir sku y diferenciarse solo por la
// personalización (D-041.20), así que no hay clave por línea y comparar por índice
// sería inventarse una.
//
// Con esa forma, los tres actos de REQ-36 cuentan y ninguno se sale del denominador:
//
//   - CORREGIR una de 4 líneas ⇒ 1 de 4 (la vieja no está en el conjunto nuevo);
//   - QUITAR una de 4 ⇒ 1 de 4 (el total lo pone el lado grande, el de antes);
//   - AÑADIR una a 3 ⇒ 1 de 4 (el total lo pone el lado grande, el de después).
//
// Tomar `len(después)` como denominador —lo primero que uno escribe— rompería el
// segundo caso: un dueño que borra una línea daría `1/3`, o peor, `lines_corrected`
// mayor que `lines_total` si borrara dos. Un porcentaje por encima de 100 en un panel
// no se lee como «esta métrica está mal definida», se lee como un dato roto.
//
// LA LÍNEA DE ENVÍO NO CUENTA por ninguno de los dos lados: es de la plataforma
// (ReservedSKUPrefix, D-041.11), sobrevive intacta a toda edición y contarla metería
// en el KPI una línea que el LLM nunca interpretó ni el dueño puede corregir. Del
// lado de `después` ya no puede llegar —la validación rechaza el prefijo reservado—,
// pero se filtra igual: el filtro tiene que decir lo mismo en los dos lados o el
// recuento dependería de una validación que vive en otro fichero.
func recuentoDeCorrección(antes, después []Item) (corregidas, total int) {
	pendientes := map[claveDeLínea]int{}
	var deAntes int
	for _, it := range antes {
		if esLíneaDePlataforma(it) {
			continue
		}
		deAntes++
		pendientes[claveDe(it)]++
	}

	var deDespués, iguales int
	for _, it := range después {
		if esLíneaDePlataforma(it) {
			continue
		}
		deDespués++
		if k := claveDe(it); pendientes[k] > 0 {
			pendientes[k]--
			iguales++
		}
	}

	total = max(deAntes, deDespués)
	return total - iguales, total
}

// claveDeLínea es la identidad de una línea A EFECTOS DE ESTE RECUENTO: los cinco
// campos que el dueño puede tocar desde su consola.
//
// 🔴 `AddedAt` NO ENTRA, y no es un olvido: las líneas que llegan del PUT vienen con
// el cero-valor y las que están guardadas traen su fecha real, así que incluirla haría
// que NINGUNA línea casara nunca y el evento publicaría siempre «todo corregido».
type claveDeLínea struct {
	sku           string
	label         string
	customization string
	qty           int
	unitPrice     float64
}

func claveDe(it Item) claveDeLínea {
	return claveDeLínea{
		sku:           it.SKU,
		label:         it.Label,
		customization: it.Customization,
		qty:           it.Qty,
		unitPrice:     it.UnitPrice,
	}
}

// esLíneaDePlataforma dice si la línea la puso wApp y no el cliente (hoy, el envío).
func esLíneaDePlataforma(it Item) bool {
	return strings.HasPrefix(it.SKU, ReservedSKUPrefix)
}
