package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// cadena_ola3_test.go — LO QUE T3.8 CABLEA: que la cadena del worker llegue de verdad
// hasta el borrador.
//
// 🔴 POR QUÉ ESTOS TESTS NO EXISTÍAN Y HACÍAN FALTA. La Ola 3 escribió `match` y
// `draft` con sus tests, y esos tests las llamaban a mano. Nadie más las llamaba:
// `stages.NewMatch` y `stages.NewDraft` no tenían un solo call-site de producción y
// `pipeline.go` no nombraba `StageMatch` ni `StageDraft` ni una vez. La suite entera
// estaba verde y cada job real terminaba en `done` sin `intake_id`, sin un error en
// ningún log. Lo que aquí se prueba es exactamente esa costura: que el worker las
// recorre, en orden, y que el id del borrador llega hasta `intake_jobs.intake_id`.

// TestWorker_LaCadenaRecorreLasCincoEtapasYTerminaConIntakeID es el criterio de T3.8.
//
// Mira las TRES cosas que la ausencia del cableado dejaba a cero, y las tres por
// separado porque fallan por motivos distintos: las cinco etapas se llamaron, los
// cinco artefactos quedaron persistidos (que es lo que hace gratis la reanudación) y
// el `intake_id` llegó a la fila.
//
// 🔬 MUTACIÓN EJECUTADA: en `cadena`, cortar después de `w.cantidades(...)` y devolver
// ahí —la cadena de la Ola 2 tal como estaba, sin código inalcanzable, así que `vet` no
// la delata—. RESULTADO: rojo (8 tests de este paquete), y ÉSTE falla por lo que tiene
// que fallar: «la etapa "match" se llamó 0 veces».
func TestWorker_LaCadenaRecorreLasCincoEtapasYTerminaConIntakeID(t *testing.T) {
	b := nuevoBanco(t, Config{})
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())

	f := b.ver(t, id)
	if f.Status != intake.StatusDone {
		t.Fatalf("el job debía terminar bien, quedó %q (%s)", f.Status, b.log.volcado())
	}
	// (1) LAS CINCO ETAPAS SE LLAMARON, cada una UNA vez.
	for nombre, llamadas := range map[string]int{
		"p2": b.p2.count(), "p3": b.p3.count(), "p4": b.p4.count(),
		"match": b.match.count(), "draft": b.draft.count(),
	} {
		if llamadas != 1 {
			t.Fatalf("la etapa %q se llamó %d veces; la cadena la recorre UNA (%s)", nombre, llamadas, b.log.volcado())
		}
	}
	// (2) LOS CINCO ARTEFACTOS QUEDARON PERSISTIDOS. Es lo que hace que una redelivery
	// no repita nada, y lo que `stage` refleja: la máquina no puede retroceder.
	for _, etapa := range []string{intake.StageP2, intake.StageP3, intake.StageP4,
		intake.StageMatch, intake.StageDraft} {
		if raw, hay := f.Artifacts[etapa]; !hay || len(raw) == 0 {
			t.Fatalf("falta el artefacto de la etapa %q; hay %v", etapa, clavesDe(f.Artifacts))
		}
	}
	if f.Stage != intake.StageDraft {
		t.Fatalf("la última etapa del job es %q y tenía que ser %q", f.Stage, intake.StageDraft)
	}
	// (3) EL `intake_id`, QUE ES LA ENTREGA. Se compara contra el valor EXACTO que
	// devolvió el draft: un «no vacío» lo satisfaría también un id inventado por el
	// store, y lo que se prueba es que viaja de la etapa a la fila sin perderse.
	if f.IntakeID != intakeIDDePrueba {
		t.Fatalf("el job terminó con intake_id=%q y tenía que ser %q: el id del borrador NO llegó a Finish",
			f.IntakeID, intakeIDDePrueba)
	}
}

// TestWorker_ElMatchRecibeElCatalogoYLasZonasQueElWorkerLEYO fija la otra mitad del
// cableado: que las dos lecturas POR JOB llegan a la etapa.
//
// 🔴 SIN ESTO, UN WORKER QUE LLAMARA A `match` CON LA ENTRADA VACÍA PASARÍA EL TEST DE
// ARRIBA. La cadena se recorrería igual, los cinco artefactos estarían igual y el
// `intake_id` llegaría igual — y en campo cada borrador saldría con todo `unmatched`
// (la etapa real devuelve ErrSinCatalogo) o sin la tarifa de envío del tenant. Son
// dos fallos que no se ven desde el desenlace del job.
func TestWorker_ElMatchRecibeElCatalogoYLasZonasQueElWorkerLEYO(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.zonas.SetShippingZones("tenant-1", intakes.ShippingZone{Code: "z1", Label: "Providencia", Price: 3000})
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())
	if f := b.ver(t, id); f.Status != intake.StatusDone {
		t.Fatalf("el job debía terminar bien, quedó %q", f.Status)
	}

	in, llamada := b.match.vioLaEntrada()
	if !llamada {
		t.Fatal("la etapa `match` no se llamó: no hay entrada que mirar")
	}
	if in.Indice == nil {
		t.Fatal("el match recibió el índice del catálogo a NIL: con eso la etapa real devuelve ErrSinCatalogo y el job muere por infraestructura")
	}
	if in.Cantidades == nil {
		t.Fatal("el match recibió las cantidades a NIL: se está llamando sin lo que P4 dejó")
	}
	if len(in.Zonas) != 1 || in.Zonas[0].Label != "Providencia" {
		t.Fatalf("el match recibió %v como zonas de envío; tenía que llegarle la del tenant, "+
			"que es la que dicta la tarifa plana de la línea de envío", in.Zonas)
	}
	// La nota del pedido entero NO la produce nadie todavía, y eso es un hueco
	// declarado: el día que alguien la produzca, esta aserción se cae y se entera.
	if in.Nota != stages.SinNotaDePedido {
		t.Fatalf("el match recibió una nota de pedido (%q) y hoy NADIE la produce: "+
			"si ya hay productor, `stages.NotaDePedido` y este test están desactualizados", in.Nota)
	}
}

// TestWorker_ElDraftRecibeElLiteralLaFechaYElArtefactoDelMatch cierra la costura por el
// otro extremo: lo que el worker le pone delante a la última etapa.
//
// Los tres primeros campos son cableado que puede perderse SIN ERROR —un borrador sin
// `source_text` sigue naciendo, y sin fecha también—, y los dos últimos son los HUECOS
// DECLARADOS de esta tarea. Se comprueban en el mismo sitio a propósito: quien venga a
// cablear el anclaje de adjuntos (T3.3) o la vía del análisis (D-044.15) va a poner
// rojo este test, y el mensaje le dirá que lo que tiene que actualizar es esto y la
// cabecera del paquete, no que ha roto nada.
func TestWorker_ElDraftRecibeElLiteralLaFechaYElArtefactoDelMatch(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.p4.fecha = "2026-09-04"
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())
	if f := b.ver(t, id); f.Status != intake.StatusDone {
		t.Fatalf("el job debía terminar bien, quedó %q", f.Status)
	}

	in, llamada := b.draft.vioLaEntrada()
	if !llamada {
		t.Fatal("la etapa `draft` no se llamó: no hay entrada que mirar")
	}
	if in.Match == nil {
		t.Fatal("el draft recibió el artefacto del match a NIL: la etapa real devuelve ErrSinMatch y no habría borrador")
	}
	// El literal es el MISMO que abrió el sobre y que interpretaron P2-P4: es lo que el
	// dueño lee al lado de la interpretación para validarla (§7.6), y es la mitad de la
	// razón de ser de la revisión.
	if in.SourceText != "quiero una torta de chocolate para el viernes" {
		t.Fatalf("el draft recibió %q como literal; tenía que llegarle el que descifró el worker", in.SourceText)
	}
	if in.FechaEntrega != "2026-09-04" {
		t.Fatalf("el draft recibió %q como fecha de entrega; la que dejó P4 era 2026-09-04 y se perdió por el camino",
			in.FechaEntrega)
	}
	// 🔴 LOS DOS HUECOS DECLARADOS DE T3.8, fijados para que se vean cuando dejen de
	// serlo (ver la cabecera de pipeline.go).
	if len(in.Media.PorLinea) != 0 || len(in.Media.Solicitud) != 0 {
		t.Fatalf("el draft recibió un reparto de adjuntos (%v) y T3.8 NO cablea el anclaje: "+
			"si ya hay quien lea los media refs del hilo con sus instantes, actualiza este test y "+
			"la cabecera de pipeline.go, que dicen lo contrario", in.Media)
	}
	if in.Analisis.Provider != "" {
		t.Fatalf("el draft recibió la vía del análisis (%q) y T3.8 NO la cablea: la vía vive dentro "+
			"de llmvia.Selector y no sale por ningún puerto. Si ya sale, actualiza este test y la "+
			"cabecera de pipeline.go", in.Analisis.Provider)
	}
}

// TestWorker_ElCatalogoSeLeeUNAVezPorJob es el criterio (a) de T3.7 medido desde el
// llamante: el índice se construye una vez y se le pasa a la etapa, en vez de que cada
// ítem dispare un SELECT.
//
// Se siembra un pedido de VARIOS ítems a propósito: con uno solo, «una lectura por
// job» y «una por ítem» dan el mismo número y el test no distinguiría nada.
func TestWorker_ElCatalogoSeLeeUNAVezPorJob(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.p3.items = itemsDePrueba(7)
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())
	if f := b.ver(t, id); f.Status != intake.StatusDone {
		t.Fatalf("el job debía terminar bien, quedó %q", f.Status)
	}

	in, _ := b.match.vioLaEntrada()
	if in.Cantidades == nil || len(in.Cantidades.Items) != 7 {
		t.Fatalf("el escenario no tiene los 7 ítems que este test necesita para distinguir "+
			"«una lectura por job» de «una por ítem»: llegaron %v", in.Cantidades)
	}
	if got := b.catalogos.Lecturas(); got != 1 {
		t.Fatalf("el catálogo se leyó %d veces para UN job de 7 ítems; tiene que leerse 1", got)
	}
}

// TestWorker_ElCatalogoQueNoSeLeeEsUnTropiezoDeInfra: sin índice NINGÚN ítem puede
// casar, así que el job vuelve a la cola castigado en vez de parir un borrador que
// afirmaría que el tenant no vende nada de lo que el cliente pidió.
//
// 🔬 MUTACIÓN EJECUTADA: en `cruzarConElCatalogo`, ignorar el error de `Obtener`
// (`indice, _ := …`) y seguir con el índice a nil. RESULTADO: rojo — «el job debe
// volver a PENDING, quedó "done"»: con la etapa falsa el borrador nace sobre un
// catálogo que no se pudo leer.
func TestWorker_ElCatalogoQueNoSeLeeEsUnTropiezoDeInfra(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.catalogos.RomperLaLectura(errors.New("tenant_content no contesta"))
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())

	f := b.ver(t, id)
	if f.Status != intake.StatusPending {
		t.Fatalf("un catálogo ilegible es infraestructura: el job debe volver a PENDING, quedó %q", f.Status)
	}
	if f.Attempts != 1 {
		t.Fatalf("el intento debe cobrarse; attempts=%d", f.Attempts)
	}
	if got := b.match.count(); got != 0 {
		t.Fatalf("la etapa `match` NO debe llegar a correr sin índice; se llamó %d veces", got)
	}
	if got := b.draft.count(); got != 0 {
		t.Fatalf("el borrador NO debe nacer si el match no corrió; se llamó %d veces", got)
	}
	if f.IntakeID != "" {
		t.Fatalf("el job no terminó y aun así tiene intake_id=%q", f.IntakeID)
	}
}

// TestWorker_LasZonasQueNoSeLeenNO_MatanElJob es la asimetría deliberada del otro lado:
// `tenant_settings` que no contesta cuesta UNA línea de precio, no el pedido.
//
// 🔴 ES LA MISMA ARITMÉTICA DE DEUDA-044.16: la unidad de daño tiene que ser el dato
// que falló, no la solicitud del cliente. Un borrador con «Envío por confirmar» es la
// línea legítima de la mayoría de tenants; un job muerto es un pedido perdido.
func TestWorker_LasZonasQueNoSeLeenNO_MatanElJob(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.w.zonas = zonasRotas{err: errors.New("tenant_settings no contesta")}
	id := b.sembrarSano("")

	b.w.Drenar(context.Background())

	f := b.ver(t, id)
	if f.Status != intake.StatusDone {
		t.Fatalf("unas zonas ilegibles NO pueden matar el pedido de un cliente; el job quedó %q", f.Status)
	}
	if f.IntakeID != intakeIDDePrueba {
		t.Fatalf("el borrador tenía que nacer igual; intake_id=%q", f.IntakeID)
	}
	in, _ := b.match.vioLaEntrada()
	if len(in.Zonas) != 0 {
		t.Fatalf("sin lectura no hay zonas que pasar; llegaron %v", in.Zonas)
	}
	// El aviso es el ÚNICO rastro de que se perdió la tarifa: sin él, la degradación
	// sería indistinguible de un tenant sin zonas configuradas.
	if l := b.log.buscar("no se pudieron leer las zonas de envío"); len(l) != 1 {
		t.Fatalf("la degradación tiene que dejar EXACTAMENTE un aviso, hay %d: %s", len(l), b.log.volcado())
	}
}

// TestWorker_Redelivery_NoRepiteNiElMatchNiElDraft extiende la reanudación por estado a
// las dos etapas nuevas.
//
// 🔴 EN EL DRAFT NO ES UN AHORRO, ES CORRECCIÓN. `match` repetido costaría CPU; `draft`
// repetido vuelve a escribir la cabecera de la solicitud y le cuelga OTRA revisión al
// mismo borrador — el dueño vería dos interpretaciones del mismo mensaje. Que la marca
// `artifacts.draft` sea lo ÚLTIMO que la etapa escribe es lo que hace esto seguro.
//
// Los artefactos no se fabrican a mano: los deja la PRIMERA corrida, que cae en el
// draft. Un `artifacts` escrito a mano afirmaría algo sobre un estado que la máquina
// podría no producir nunca.
func TestWorker_Redelivery_NoRepiteNiElMatchNiElDraft(t *testing.T) {
	b := nuevoBanco(t, Config{})
	b.draft.guion = []guionEtapa{{err: errorDeRedReal(t)}, {}}
	id := b.sembrarSano("")

	b.w.Drenar(context.Background()) // 1.ª corrida: todo sale menos el draft
	if f := b.ver(t, id); f.Status != intake.StatusPending {
		t.Fatalf("tras caer el draft el job debe quedar pending, quedó %q", f.Status)
	}
	if _, ok := b.ver(t, id).Artifacts[intake.StageMatch]; !ok {
		t.Fatal("la 1.ª corrida debía dejar el artefacto del match persistido y no lo dejó: el test no mira nada")
	}

	b.rel.avanzar(BackoffTopePorDefecto * 2)
	b.w.Drenar(context.Background()) // 2.ª corrida: solo el draft

	f := b.ver(t, id)
	if f.Status != intake.StatusDone {
		t.Fatalf("la 2.ª corrida debe terminar el job, quedó %q", f.Status)
	}
	if f.IntakeID != intakeIDDePrueba {
		t.Fatalf("el job reanudado tiene que terminar con su intake_id; quedó %q", f.IntakeID)
	}
	if got := b.match.count(); got != 1 {
		t.Fatalf("el match NO debe repetirse en la redelivery: se llamó %d veces", got)
	}
	if got := b.draft.count(); got != 2 {
		t.Fatalf("el draft sí debe repetirse (no llegó a persistir): se llamó %d veces", got)
	}
	if got := b.catalogos.Lecturas(); got != 1 {
		t.Fatalf("la 2.ª corrida NO debe volver a leer el catálogo (el match se salta entero): %d lecturas", got)
	}
}

// TestWorker_ConLasEtapasREALES_LaSolicitudNaceEnLaBANDEJA es el e2e local del
// cableado: las dos etapas de la Ola 3 REALES, sobre los almacenes en memoria del
// dominio, movidas por el worker de producción.
//
// 🔴 POR QUÉ NO BASTA CON LOS DOBLES. Los tests de arriba prueban que el worker LLAMA a
// las etapas y que el id viaja; con dobles, el `intake_id` lo inventa el doble. Lo que
// esto añade es que las piezas ENCAJAN: que los puertos que `bootstrap.go` cablea
// (`*store.MemoryRepository` como escritor de solicitudes y de eventos,
// `*intakes.MemoryStore` como escritor de revisiones — los gemelos declarados de los
// dos Postgres de producción) son los que las etapas reales necesitan, y que al final
// hay UNA solicitud con UNA revisión y su fila de telemetría.
func TestWorker_ConLasEtapasREALES_LaSolicitudNaceEnLaBANDEJA(t *testing.T) {
	rel := nuevoReloj()
	almacen := NuevoStoreEnMemoria(rel.ahora)
	log := &captor{}
	flujos := store.NewMemoryRepository()
	solicitudes := intakes.NewMemoryStore()

	etapaMatch, err := stages.NewMatch(logger.New(), almacen)
	if err != nil {
		t.Fatalf("construir el match real: %v", err)
	}
	etapaDraft, err := stages.NewDraft(logger.New(), almacen, flujos, solicitudes, flujos)
	if err != nil {
		t.Fatalf("construir el draft real: %v", err)
	}
	catalogos, err := NuevoCatalogoEnMemoria("Torta de chocolate")
	if err != nil {
		t.Fatalf("construir el catálogo: %v", err)
	}

	p2 := &p2Falsa{etapaBase: etapaBase{rel: rel}, store: almacen, wants: wantsDePrueba(1)}
	p3 := &p3Falsa{etapaBase: etapaBase{rel: rel}, store: almacen, items: itemsDePrueba(1)}
	p4 := &p4Falsa{etapaBase: etapaBase{rel: rel}, store: almacen}

	w, err := NewWorker(log, almacen, p2, p3, p4, etapaMatch, etapaDraft, catalogos,
		cifraFalsa{}, Config{}, ConZonasDeEnvio(solicitudes))
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	w.ahora = rel.ahora

	const evento = "22222222-2222-2222-2222-222222222222"
	id := almacen.Sembrar(Fila{
		Key: intake.WindowKey{TenantID: "tenant-1", SessionID: "sess-1",
			ContactID: "contacto-1", EventID: evento},
		SourceText: intake.SourceText{Enc: []byte("cifrado"), DEK: []byte("dek"), KEKID: "kek-1"},
		MessageTS:  rel.ahora().Add(-2 * time.Minute),
		CreatedAt:  rel.ahora(),
	})

	w.Drenar(context.Background())

	f, _ := almacen.Ver(id)
	if f.Status != intake.StatusDone {
		t.Fatalf("el job debía terminar bien, quedó %q: %s", f.Status, log.volcado())
	}
	if f.IntakeID == "" {
		t.Fatalf("el job terminó SIN intake_id con las etapas reales: %s", log.volcado())
	}
	// La cabecera de la solicitud, escrita por `UpsertIntake` en el store de flujos.
	cabeceras := flujos.Intakes()
	if len(cabeceras) != 1 {
		t.Fatalf("tenía que nacer UNA solicitud, hay %d", len(cabeceras))
	}
	if cabeceras[0].ID != f.IntakeID {
		t.Fatalf("la solicitud creada (%s) NO es la que el job apunta (%s): el id se perdió por el camino",
			cabeceras[0].ID, f.IntakeID)
	}
	if cabeceras[0].Status != intakes.StatusPendingApproval {
		t.Fatalf("un borrador interpretado nace esperando al dueño; nació en %q", cabeceras[0].Status)
	}
	// La revisión 1, que es LA entrega del draft: una cabecera sin revisión sería una
	// solicitud vacía en la bandeja.
	revs := solicitudes.Revisions(f.IntakeID)
	if len(revs) != 1 {
		t.Fatalf("tenía que haber UNA revisión interpretada, hay %d", len(revs))
	}
	if revs[0].Kind != intakes.RevisionKindInterpreted {
		t.Fatalf("la revisión 1 es %q y tenía que ser %q", revs[0].Kind, intakes.RevisionKindInterpreted)
	}
	if len(flujos.FlowEvents()) != 1 {
		t.Fatalf("el borrador publica UNA fila de telemetría, hay %d", len(flujos.FlowEvents()))
	}
}

// ---------------------------------------------------------------------------
// ATREZO
// ---------------------------------------------------------------------------

// zonasRotas es el lector de zonas que no contesta. Se declara aquí y no en
// dobles_test.go porque solo lo usa un test: un doble compartido que solo tiene un
// llamante es un doble que nadie va a recordar que existe.
type zonasRotas struct{ err error }

func (z zonasRotas) ShippingZones(_ context.Context, _ string) ([]intakes.ShippingZone, error) {
	return nil, z.err
}

// clavesDe lista las etapas con artefacto, para que un fallo diga qué SÍ había.
func clavesDe(arts map[string]json.RawMessage) []string {
	out := make([]string, 0, len(arts))
	for k := range arts {
		out = append(out, k)
	}
	return out
}
