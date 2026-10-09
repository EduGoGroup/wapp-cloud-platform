// Porta internal/intake/pipeline/pipeline.go @ 56097aa (la cadena de un job: sobre, plaza y etapas)

package pipeline

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-shared/llm"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// pipeline_chain.go — LO QUE LE PASA A UN JOB YA RECLAMADO, hasta que la cadena termina o
// tropieza. Sin exportados: el contrato es el de RunOnce (pipeline_loop.go). Los
// desenlaces —y la reanudación y el cronómetro, que son de todas las etapas— están en
// `pipeline_outcome.go`.

// process (antes `procesar`) lleva un job YA RECLAMADO hasta un desenlace. Siempre acaba
// en uno de cuatro sitios, y ninguno deja el job en `processing`:
//
//	done    — la cadena entera salió (finish)
//	pending — tropiezo reintentable, con la marca empujada (stumble → Retry)
//	failed  — job inválido, o techo de intentos agotado (stumble → Fail)
//	nada    — el job se movió bajo los pies (ErrJobNotProcessing): ya lo terminó otro
func (w *Worker) process(ctx context.Context, job intake.ClaimedJob) {
	literal, err := w.literalOf(job)
	if err != nil {
		// 🔴 SE CORTA ANTES DE LLAMAR AL MODELO, sea cual sea el motivo: un prompt sin
		// texto del cliente es el accidente que D-044.24 describe —lo único concreto dentro
		// serían los productos que listamos NOSOTROS— y además tira 22–32 s de la plaza
		// única para nada.
		//
		// Qué desenlace le toca lo decide `stumble`, que ya sabe distinguir el job inválido
		// (sin sobre, no mejora con reintentos) del descifrado que falló (KEK/KMS,
		// transitorio). Se pasa por ahí y no se llama a `kill` directo para que la
		// clasificación viva en UN solo sitio.
		w.stumble(ctx, job, "", 0, err)
		return
	}
	// 🔴 LA PLAZA SE TOMA AQUÍ: DESPUÉS DE ABRIR EL SOBRE Y ANTES DE LA CADENA.
	//
	// Después del sobre porque descifrar es CPU local y no toca el Ollama de nadie: tomar
	// la plaza antes la retendría durante un descifrado que, si falla, ni siquiera va a
	// llamar al modelo.
	//
	// Antes de la cadena —y no por llamada— porque el entero del ADR-0046 cuenta CADENAS
	// DE LOTE, no peticiones. Un aforo por llamada dejaría a N cadenas intercalando sus
	// peticiones sobre la misma plaza, y un turno interactivo volvería a poder quedar
	// detrás de N llamadas.
	release, err := w.acquireSlot(ctx, job)
	if err != nil {
		w.releaseUnpunished(ctx, job, "el worker se apagó esperando plaza")
		return
	}
	defer release()

	draft, err := w.chain(ctx, job, literal)
	if err != nil {
		return // la cadena ya escribió el desenlace
	}
	w.finish(ctx, job, draft)
}

// acquireSlot (antes `tomarPlaza`) resuelve la dirección de la plaza de este job y la
// ocupa. Devuelve SIEMPRE una función de soltar utilizable —vacía cuando no había plaza
// que tomar— y error SOLO si el ctx murió esperando turno.
//
// # LOS TRES CAMINOS SIN PLAZA, Y POR QUÉ NINGUNO DE LOS TRES PARA EL JOB
//
//  1. **Worker sin aforo** (no se cableó WithCapacity). Sigue siendo legal; el Warn del
//     arranque es su señal.
//  2. **El tenant no ocupa plaza**: está en vía API (allí el tope es de precio, no de
//     capacidad) o no tiene ningún Edge conectado. Aquí las dos cosas se ven igual y se
//     tratan igual.
//  3. **No se pudo preguntar** (la config del tenant no se leyó). 🔴 NO se convierte en
//     tropiezo AQUÍ, y es deliberado: si `tenant_llm` no se puede leer, la primera etapa va
//     a fallar por lo mismo dos líneas más abajo y `stumble` lo clasificará con el resto.
//     Clasificarlo también aquí sería la segunda clasificación del mismo error.
func (w *Worker) acquireSlot(ctx context.Context, job intake.ClaimedJob) (func(), error) {
	nothing := func() {}
	if w.capacity == nil || w.slots == nil {
		return nothing, nil
	}
	edgeID, ok, err := w.slots.PlazaDe(ctx, job.Key.TenantID, job.Key.SessionID)
	if err != nil {
		w.log.Warn("pipeline: no se pudo resolver la plaza del job; la cadena sigue SIN aforo",
			"job_id", job.ID, "tenant_id", job.Key.TenantID, "error", err.Error())
		return nothing, nil
	}
	s := Slot{TenantID: job.Key.TenantID, EdgeID: edgeID}
	if !ok || !s.Valid() {
		w.log.Debug("pipeline: el job no ocupa plaza (vía sin plaza, o el tenant no tiene Edge vivo)",
			"job_id", job.ID, "tenant_id", job.Key.TenantID)
		return nothing, nil
	}

	// El log de espera SOLO cuando de verdad hay cola: emitirlo siempre lo volvería ruido y
	// dejaría de significar nada el día que haya que buscarlo.
	if w.capacity.Waiting() > 0 {
		w.log.Info("pipeline: la plaza del Edge está ocupada; este job ESPERA (no falla, no se castiga)",
			"job_id", job.ID, "plaza", s.String(), "esperando", w.capacity.Waiting())
	}
	release, err := w.capacity.Acquire(ctx, s)
	if err != nil {
		return nil, err
	}
	return release, nil
}

// releaseUnpunished (antes `soltarSinCastigo`) devuelve el job a `pending` TAL COMO
// ESTABA: sin consumirle el intento y sin empujarle la marca. Es el desenlace de lo que no
// es culpa del job —el worker se apagó esperando plaza, o el flanco a READY ya lo había
// procesado— y por eso usa `Release` y no `Retry`.
//
// 🔴 Y POR ESO MISMO NO SE PUEDE USAR EN UN TROPIEZO. `Release` no toca `next_attempt_at`,
// así que el job es reclamable EN EL ACTO: en un camino que vuelve a fallar, eso es la
// tormenta de la 0078. Aquí es correcto porque los dos llamantes SALEN del bucle
// inmediatamente después.
func (w *Worker) releaseUnpunished(ctx context.Context, job intake.ClaimedJob, reason string) {
	closing, cancel := w.closingCtx(ctx)
	defer cancel()
	applied, err := w.store.Release(closing, job.ID)
	if err != nil {
		w.log.Error("pipeline: no se pudo devolver el job a la cola; queda en processing",
			"job_id", job.ID, "motivo", reason, "error", err.Error())
		return
	}
	if !applied {
		w.log.Info("pipeline: la devolución no aplicó (el job ya no estaba en processing)",
			"job_id", job.ID, "motivo", reason)
		return
	}
	w.log.Info("pipeline: job devuelto a la cola SIN castigo",
		"job_id", job.ID, "motivo", reason)
}

// literalOf (antes `literalDe`) abre el sobre de tres piezas. Distingue los DOS motivos
// por los que puede no haber texto, porque tienen desenlaces distintos:
//
//   - el sobre viene INCOMPLETO ⇒ el compositor del flush no llegó a escribirlo. El job es
//     inválido y no hay reintento que lo arregle. 🔴 D-F7-9: también cae aquí el job
//     reclamado ENTRE el cierre de la ventana y la escritura del sobre (la carrera
//     `CloseWindow` → `PutSourceText`); se porta tal cual y la causa la arregla F8;
//   - el sobre está entero pero NO DESCIFRA ⇒ la KEK no desenvuelve. Eso SÍ puede ser
//     transitorio (KMS caído) y se trata como infraestructura.
//
// 🔴 Un job terminal nunca se reclama, así que un sobre vacío aquí NO es INV-13 en acción:
// es exactamente el primer caso.
func (w *Worker) literalOf(job intake.ClaimedJob) (string, error) {
	if !job.SourceText.Complete() {
		return "", fmt.Errorf("%w (el compositor del flush no llegó a escribir el sobre)", stages.ErrNoLiteral)
	}
	text, err := w.decrypter.Decrypt(job.SourceText.Enc, job.SourceText.DEK, job.SourceText.KEKID)
	if err != nil {
		// El error NO se enriquece con nada del texto: lo que falló es el sobre.
		return "", fmt.Errorf("descifrar el literal del job: %w", err)
	}
	if text == "" {
		return "", fmt.Errorf("%w (el sobre descifró a cadena vacía)", stages.ErrNoLiteral)
	}
	return text, nil
}

// chain (antes `cadena`) encadena P2 → P3 → P4 → match → draft y devuelve el artefacto de
// la última etapa —el que lleva el `intake_id` con el que se cierra el job— o error si
// alguna no pudo producir el suyo. Cuando devuelve error, el desenlace YA está escrito.
//
// 🔴 EL CTX QUE VIAJA AQUÍ NO LLEVA DEADLINE, Y ES DELIBERADO. El plazo se acota POR
// LLAMADA dentro de cada etapa (`stages.WithCallTimeout`), no por job ni por etapa: un
// deadline de job se REPARTE entre las N llamadas del fan-out de P3 —la primera se lleva
// casi todo y la última casi nada— y el `timeout_ms` que llega al Edge deja de describir
// una llamada de lote. Ver CallTimeoutFloor.
func (w *Worker) chain(ctx context.Context, job intake.ClaimedJob, literal string) (*stages.DraftArtifact, error) {
	ideas, err := w.ideas(ctx, job, literal)
	if err != nil {
		return nil, err
	}
	if len(ideas.Wants) == 0 {
		w.noIdeas(job)
	}
	specs, err := w.specs(ctx, job, literal, ideas.Wants)
	if err != nil {
		return nil, err
	}
	quantities, err := w.quantities(ctx, job, literal, specs.Items, ideas.DeliveryHint)
	if err != nil {
		return nil, err
	}
	quote, err := w.lines(ctx, job, quantities)
	if err != nil {
		return nil, err
	}
	return w.draftOf(ctx, job, literal, quote, quantities.DeliveryDate)
}

// noIdeas (antes `sinIdeas`) es el aviso del job que llega a P3 con CERO ideas vivas.
//
// # LA DECISIÓN, ESCRITA AQUÍ PORQUE NO TENÍA DUEÑO
//
// `llm.ParseMainIdeas` declara VÁLIDA una lista de `wants` vacía, así que un job al que el
// anclaje le tire TODAS las ideas persiste `{"version":1,"wants":[]}` y sigue camino.
// **La cadena NO se corta, y este aviso es el precio.** Por qué no se corta:
//
//  1. el diseño lo declara no fatal y el plan manda CONSERVADOR: una salida mala nunca
//     descarta la solicitud del cliente;
//  2. es un caso LEGÍTIMO y frecuente: la ventana de agregación se abre con cualquier
//     mensaje, así que un «hola» produce un job cuyo P2 honesto no tiene nada que extraer.
//     Matarlo llenaría la bandeja del dueño de `failed` que no son fallos de nadie;
//  3. no cuesta plaza: con cero ideas, P3 no llama al modelo y P4 tampoco.
//
// 🔴 EL AVISO NOMBRA LOS DOS ORÍGENES Y NO AFIRMA NINGUNO: el worker recibe la lista YA
// anclada y no puede distinguir «el modelo no devolvió ideas» de «el anclaje las descartó
// todas». Quien SÍ lo sabe es P2, que registra `ideas` e `ideas_descartadas` para el mismo
// `job_id` — por eso el mensaje manda a buscar allí.
func (w *Worker) noIdeas(job intake.ClaimedJob) {
	w.log.Warn("pipeline: P2 no dejó ni una idea viva; el job sigue y terminará con un borrador VACÍO",
		"job_id", job.ID, "stage", intake.StageP2,
		"causa", "indeterminada_desde_aqui",
		"donde_mirar", "el log de p2 para este job_id trae `ideas` e `ideas_descartadas`: si `ideas_descartadas` es 0, el modelo no devolvió ninguna; si no, las descartó el anclaje")
}

// ideas ejecuta P2 — o se la salta si el artefacto ya está persistido.
func (w *Worker) ideas(ctx context.Context, job intake.ClaimedJob, literal string) (*llm.MainIdeas, error) {
	if art, ok := resume[llm.MainIdeas](w, job, intake.StageP2); ok {
		return art, nil
	}
	start := w.now()
	out, err := w.p2.Run(ctx, job, literal)
	return outcome(ctx, w, job, intake.StageP2, start, out, err)
}

// specs (antes `especificaciones`) ejecuta P3 — o se la salta si el artefacto ya está
// persistido.
func (w *Worker) specs(ctx context.Context, job intake.ClaimedJob,
	literal string, wants []llm.Want) (*stages.P3Artifact, error) {
	if art, ok := resume[stages.P3Artifact](w, job, intake.StageP3); ok {
		return art, nil
	}
	start := w.now()
	out, err := w.p3.Run(ctx, job, literal, wants)
	return outcome(ctx, w, job, intake.StageP3, start, out, err)
}

// quantities (antes `cantidades`) ejecuta P4 — o se la salta si el artefacto ya está
// persistido.
func (w *Worker) quantities(ctx context.Context, job intake.ClaimedJob, literal string,
	items []llm.ItemSpec, delivery *llm.Hint) (*llm.Quantities, error) {
	if art, ok := resume[llm.Quantities](w, job, intake.StageP4); ok {
		return art, nil
	}
	start := w.now()
	out, err := w.p4.Run(ctx, job, literal, items, delivery)
	return outcome(ctx, w, job, intake.StageP4, start, out, err)
}

// lines (antes `lineas`) ejecuta `match` — o se la salta si el artefacto ya está
// persistido.
//
// 🔴 EL CRONÓMETRO ARRANCA ANTES DE LEER EL CATÁLOGO, Y NO ES UN DESCUIDO. Las dos lecturas
// por job (índice y zonas) son parte del coste de esta etapa: si un día el `SELECT` del
// documento se vuelve el cuello, el `elapsed_ms` de `match` tiene que enseñarlo.
func (w *Worker) lines(ctx context.Context, job intake.ClaimedJob,
	quantities *llm.Quantities) (*stages.MatchArtifact, error) {
	if art, ok := resume[stages.MatchArtifact](w, job, intake.StageMatch); ok {
		return art, nil
	}
	start := w.now()
	out, err := w.crossWithCatalog(ctx, job, quantities)
	return outcome(ctx, w, job, intake.StageMatch, start, out, err)
}

// crossWithCatalog (antes `cruzarConElCatalogo`) hace las DOS lecturas por job y llama a
// la etapa.
//
// # POR QUÉ EL CATÁLOGO MATA EL INTENTO Y LAS ZONAS NO
//
// No es simetría rota: son dos daños distintos. Sin índice NINGÚN ítem puede casar y el
// borrador entero saldría `unmatched`, o sea afirmando que el tenant no vende nada de lo
// que el cliente pidió — una mentira sobre el catálogo. Sin zonas se pierde UNA línea de
// precio, la del envío, y el borrador sale con «Envío por confirmar», que es la línea
// legítima de la inmensa mayoría de tenants.
//
// Por eso el catálogo se propaga como error —tropiezo, backoff, y el job vuelve— y las
// zonas se degradan con un Warn. Tirar el pedido de un cliente porque `tenant_settings` no
// contestó sería que la unidad de daño fuese el pedido y no el dato que falló
// (DEUDA-044.16).
func (w *Worker) crossWithCatalog(ctx context.Context, job intake.ClaimedJob,
	quantities *llm.Quantities) (*stages.MatchArtifact, error) {
	index, err := w.catalogs.Obtener(ctx, job.Key.TenantID)
	if err != nil {
		// El error NO cita el documento: dentro van los nombres y precios del tenant.
		return nil, fmt.Errorf("match: leer el catálogo del tenant: %w", err)
	}
	return w.match.Run(ctx, job, stages.MatchInput{
		Quantities: quantities,
		Index:      index,
		Zones:      w.zonesOf(ctx, job),
		// 🔴 HOY NADIE PRODUCE LA NOTA DEL PEDIDO ENTERO, y el tipo propio de
		// `stages.OrderNote` existe para que el hueco se vea en ESTA línea. No es un
		// default cómodo: ninguna etapa anterior la emite y llenarla pide una decisión de
		// producto.
		Note: stages.NoOrderNote,
	})
}

// zonesOf (antes `zonasDe`) lee las zonas de envío del tenant, o devuelve ninguna. NUNCA
// falla: ver el bloque de crossWithCatalog.
func (w *Worker) zonesOf(ctx context.Context, job intake.ClaimedJob) []intakes.ShippingZone {
	if w.zones == nil {
		return nil // el worker sin lector; ya lo gritó el arranque (ver Run).
	}
	zones, err := w.zones.ShippingZones(ctx, job.Key.TenantID)
	if err != nil {
		w.log.Warn("pipeline: no se pudieron leer las zonas de envío; el borrador sale con la línea de envío SIN precio",
			"job_id", job.ID, "stage", intake.StageMatch,
			"tenant_id", job.Key.TenantID, "error", err.Error())
		return nil
	}
	return zones
}

// draftOf (antes `borrador`) ejecuta `draft` — o se la salta si el artefacto ya está
// persistido. 🔴 Aquí saltársela no es un ahorro: `draft` NO es idempotente en la revisión
// (repetirla le cuelga OTRA revisión al mismo borrador), y lo que lo evita es este salto.
//
// 🔴 `Media` VA EN CERO Y `Analysis` TAMBIÉN, y las dos ausencias están explicadas en la
// cabecera del paquete. Se pasan explícitas y no por omisión del struct para que las dos
// se vean aquí el día que alguien vaya a cablearlas.
func (w *Worker) draftOf(ctx context.Context, job intake.ClaimedJob, literal string,
	quote *stages.MatchArtifact, deliveryDate string) (*stages.DraftArtifact, error) {
	if art, ok := resume[stages.DraftArtifact](w, job, intake.StageDraft); ok {
		return art, nil
	}
	start := w.now()
	out, err := w.draft.Run(ctx, job, stages.DraftInput{
		Match:        quote,
		SourceText:   literal,
		DeliveryDate: deliveryDate,
		Media:        anclaje.Distribution{},
		Analysis:     stages.Analysis{},
	})
	return outcome(ctx, w, job, intake.StageDraft, start, out, err)
}
