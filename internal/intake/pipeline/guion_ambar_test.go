package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cloudlinkv1 "github.com/EduGoGroup/wapp-cloudlink/gen/wapp/cloudlink/v1"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/textmatch"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/casebank"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	flowruntime "github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/catalogo"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/integrations/crmpush"
)

// guion_ambar_test.go — EL GUION COMPLETO DEL CASO AMBAR / FUSIÓN (Plan 044 · Ola 6 ·
// T6.1). Es el único test del repo que recorre el camino ENTERO de un presupuesto:
//
//	captación (agregador + P1) → P2 → P3 → P4 → match → borrador → corrección de la
//	dueña → aprobación → respuesta al cliente → `intake.push` encolado.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 QUÉ MIDE ESTE TEST Y QUÉ **NO** MIDE. LÉASE ANTES DE CITAR UN NÚMERO SUYO.
// ════════════════════════════════════════════════════════════════════════════
//
// Corre con un `llm.LLMProvider` FALSO. Eso significa que:
//
//   - lo que se prueba es EL CABLEADO —que cada etapa recibe lo que la anterior dejó,
//     que el literal viaja entero dentro del sobre, que el orden de las líneas es el
//     contrato, que la revisión 1 nace y las dos siguientes se le encadenan—;
//   - lo que NO se prueba es la CALIDAD del modelo. Las salidas del proveedor falso
//     son la interpretación CURADA del banco de casos (`casebank.EsperadoCasoAmbar`),
//     que es material de calidad C: redactado, no transcrito. Ni un número de calidad
//     puede salir de aquí;
//   - lo que NO se prueba es la LATENCIA REAL. `elapsed_ms` sale del coste de cómputo
//     de esta corrida, que son milisegundos; en producción ese número lo dominan la
//     ventana de agregación (45–120 s) y las cinco llamadas al modelo (22–32 s cada
//     una). El criterio «< 5 min» se comprueba aquí como PROPIEDAD del número —que se
//     publica, que sale de `message_ts` y que no es negativo—, no como medida de campo.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LA COSTURA DE LOS DOS DOBLES EN MEMORIA, QUE EN PRODUCCIÓN ES UNA TABLA
// ════════════════════════════════════════════════════════════════════════════
//
// El guion cruza DOS veces una frontera que en Postgres no existe, y las dos se
// puentean a mano y a la vista:
//
//  1. `intake.MemoryStore` (el que mueve el AGREGADOR) y `pipeline.StoreEnMemoria` (el
//     que mueve el WORKER) son dos dobles de la MISMA tabla `intake_jobs`. En
//     producción el worker reclama la fila que el agregador acaba de cerrar; aquí hay
//     que copiarla. Se copia TAL CUAL —la clave, el sobre, `message_ts` y las refs—,
//     nunca fabricada: si el agregador no hubiera compuesto el sobre, la copia entraría
//     vacía y el worker fallaría el job, que es exactamente lo que pasaría en campo.
//  2. `store.MemoryRepository` (donde el borrador escribe la CABECERA de la solicitud,
//     `public.intakes`) e `intakes.MemoryStore` (la BANDEJA del dueño, la misma tabla
//     vista desde el dominio de solicitudes). En producción son la misma fila; aquí la
//     cabecera se copia de uno a otro. 🔴 Se copia SIN LÍNEAS, y eso no es un atajo: es
//     lo que `UpsertIntake` escribe de verdad (ver `Draft.cabecera` — «Total y
//     CustomerNote quedan a cero»). Un pedido interpretado por el pipeline NO tiene
//     filas en `intake_items` hasta que el dueño corrige, y de ahí sale una conducta
//     del guion que parece rara y es real: aprobar ANTES de corregir daría
//     `ErrEmptyQuote`.
//
// Las dos costuras se afirman, no se suponen: el test comprueba lo que hay a cada lado
// antes de puentear.

// ---------------------------------------------------------------------------
// EL CASO
// ---------------------------------------------------------------------------

const (
	fusTenant   = "tenant-fusion"
	fusSesion   = "sess-fusion"
	fusContacto = "contacto-ambar"
	// fusEvento es el `conversation_events.id` del que cuelga todo. Es un UUID fijo
	// para que el id de la solicitud —que se DERIVA del evento (ver
	// `stages.idDeLaSolicitud`)— sea el mismo en cada corrida.
	fusEvento = "5c1f7d2a-9b34-4a1e-8c67-2f0d4b8e6a55"
	fusKEK    = "kek-fusion-1"
)

// fusPrimerMensaje son las 9:55 del 13 de julio de 2026: la hora de la solicitud real
// (doc 08) y la BASE DE FECHAS del presupuesto (D-044.9). No es `time.Now()` y no puede
// serlo: «el miércoles de la semana que viene» solo vale 22/07 medido desde ese lunes,
// y un test de fechas escrito contra el reloj de la máquina es un flake esperando.
var fusPrimerMensaje = time.Date(2026, 7, 13, 9, 55, 0, 0, time.UTC)

// fusFechaEntrega es lo que `stages.ResolverFecha` tiene que sacar de la pista textual.
// Va escrita a mano y NO recalculada: un test que repitiera la aritmética del código
// pasaría con la aritmética equivocada.
const fusFechaEntrega = "2026-07-22"

// Las evidencias del caso. Son SUBCADENAS LITERALES de `casebank.TextoCasoAmbar` —el
// anclaje de `internal/evidence` las comprueba contra él— y están aquí una sola vez
// porque las usan las tres etapas del modelo falso.
const (
	fusEvidenciaEntrega  = "para el miércoles de la semana que viene"
	fusEvidenciaChoc     = "una torta sería con decoración infantil, de bizcocho húmedo de chocolate"
	fusEvidenciaVainilla = "otra de bizcocho de vainilla que tenga lluvia de colores"
	fusEvidenciaTequenos = "un paquete de tequeños congelados de 30"
)

// Las tres ideas que P2 saca del hilo. Son también la CLAVE con la que el proveedor
// falso elige qué contestar en P3, porque P3 se llama una vez por idea.
const (
	fusIdeaChoc     = "torta de chocolate"
	fusIdeaVainilla = "torta de vainilla"
	fusIdeaTequenos = "tequeños congelados"
)

// fusMensajes es la ráfaga tal como llega por WhatsApp: un mensaje por línea del
// literal del banco. Los `wa_message_id` son opacos y es lo único que entra a
// `source_refs` (D-044.26: por el camino del entrante no viaja contenido).
//
// 🔴 LOS INSTANTES SON LOS TURNOS DOCUMENTADOS del caso (9:55:00, 9:55:30, 9:56:10 y
// 9:56:50, los mismos que `anclaje/anclaje_test.go` toma del doc 08), y el quinto va
// pegado al cuarto porque «me pasas precio» es la coletilla de la misma ráfaga. NO se
// eligieron para que el test salga bien: se eligieron porque son los del caso, y de
// ellos sale una propiedad medida que conviene saber —ver fusCaptar—.
var fusMensajes = []struct {
	id    string
	texto string
	en    time.Time
}{
	{"wamid.A1", "Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene", fusPrimerMensaje},
	{"wamid.A2", "Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate con crema de chocolate, de 10 o 12 porciones", fusPrimerMensaje.Add(30 * time.Second)},
	{"wamid.A3", "Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y merengue, de 25 o 30 porciones", fusPrimerMensaje.Add(70 * time.Second)},
	{"wamid.A4", "También quería un paquete de tequeños congelados de 30", fusPrimerMensaje.Add(110 * time.Second)},
	{"wamid.A5", "Me pasas precio porfa?", fusPrimerMensaje.Add(115 * time.Second)},
}

// ---------------------------------------------------------------------------
// EL GUION
// ---------------------------------------------------------------------------

// TestFus_GuionCompletoDelCasoAmbar es el criterio de T6.1 de punta a punta.
//
// # QUÉ TENDRÍA QUE PASAR PARA QUE FALLARA
//
// Cualquier eslabón que dejara de pasarle a su vecino lo que produjo: un sobre que no
// se compone al cerrar la ventana (el worker fallaría el job por falta de literal), un
// `message_ts` que se pisara con el reloj del servidor (la fecha saldría del día
// equivocado o no saldría), un match que perdiera el orden de las líneas, un borrador
// que no derivara su id del evento (la corrección aterrizaría en otra solicitud), o un
// `Approve` que no encolara el `intake.push` (el CRM se quedaría con el pedido sin
// confirmar).
//
// 💥 MUTACIONES EJECUTADAS, las OCHO rojas (las ocho COMPILAN):
//
//   - `stages.Draft.Run`: `ElapsedMS: 0` en el artefacto ⇒ el KPI deja de medirse. 🔴
//     ESTA SOBREVIVÍA hasta que el assert pasó a ser una IGUALDAD: con la desigualdad
//     «< 5 min», un cero la cumplía. Ver fusEsperaDelCliente;
//   - `stages.Draft.publicarMetrica`: `"elapsed_ms": int64(0)` ⇒ la telemetría y el
//     artefacto publican números distintos para el mismo dato;
//   - `stages.P4.fechar`: `base := time.Now().In(s.zona)` en vez de `job.MessageTS`
//     ⇒ la fecha sale del día de la corrida («2026-09-02») y no del mensaje;
//   - `runtime.IntakeAggregator.Observe`: quitar `s.requestAhead(ref)` ⇒ P1 no recibe
//     ni un mensaje que clasificar;
//   - `runtime.IntakeAggregator.due`: `return true` ⇒ la ráfaga recién terminada se
//     cierra ya, o sea el pedido partido en varios presupuestos;
//   - `runtime.IntakeAggregator.venceElTecho`: `return false` ⇒ el plazo que gobierna
//     esta ráfaga no vence y no se cierra ninguna ventana;
//   - `intakes.Service.Approve`: quitar `PushRevisionToCRM` ⇒ se encola UN
//     `intake.push` en vez de dos, y el CRM se queda con el pedido sin confirmar;
//   - `stages.lineaDelProducto`: `ok = false` tras `buscarProducto` ⇒ los tres
//     productos salen `unmatched` con la etiqueta del cliente.
//
// 💥 Y UNA SOBRE EL FIXTURE, que prueba la regla del anclaje: sustituir
// `fusEvidenciaChoc` por la `evidence` de ejemplo de `design.md` §7.3 —«de 10 o 12
// porciones aprox», que NO aparece en ningún texto del cliente— ⇒ P2 descarta la idea y
// P3 se llama 2 veces en vez de 3. El propio ejemplo del diseño no pasa el anclaje que
// esta ola implementa (tasks.md:4162-4168), y por eso las `evidence` de este fichero
// son SUBCADENAS LITERALES de `casebank.TextoCasoAmbar` y no paráfrasis.
func TestFus_GuionCompletoDelCasoAmbar(t *testing.T) {
	ctx := context.Background()
	log := &captor{}

	// ═══ ACTO 0 · CAPTACIÓN ══════════════════════════════════════════════
	// Los cinco mensajes entran en UNA ventana; P1 recibe los cinco y su respuesta
	// llega tarde, así que la ventana la cierra el RELOJ. El compositor sella el
	// literal. Todo el porqué, en fusCaptar.
	jobs, clave := fusCaptar(ctx, t, log)
	abierto := fusExigeLaVentanaCerrada(t, jobs)

	// ═══ COSTURA 1 + ACTO 1 · EL PIPELINE ════════════════════════════════
	esc := fusCorrerLaCadena(ctx, t, log, clave, abierto)

	presupuesto := fusExigeElPresupuesto(t, esc)
	fusExigeMatchDirecto(t, presupuesto)
	fusExigeCeroDatosFaltantes(t, log, presupuesto)
	fusExigeLaRevisionUno(t, esc)
	fusExigeElKPI(t, esc)
	fusApuntarLosTiempos(t, log, esc)

	// ═══ COSTURA 2 · de `public.intakes` a la bandeja del dueño ══════════
	svc, cola, envios := fusMontarLaBandeja(t, log, esc)

	// ═══ ACTOS 2 y 3 ═════════════════════════════════════════════════════
	fusCorregirComoLaDueña(ctx, t, svc, esc, cola)
	fusAprobarYResponder(ctx, t, svc, esc, cola, envios)
}

// ---------------------------------------------------------------------------
// ACTO 1 — LA CADENA, Y LO QUE DEJA EN PIE
// ---------------------------------------------------------------------------

// fusEsperaDelCliente es lo que Ambar espera desde que escribió hasta que su borrador
// existe, y es el valor con el que se AFIRMA el KPI del plan.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 POR QUÉ ES UN VALOR INYECTADO Y POR QUÉ SE AFIRMA CON IGUALDAD
// ════════════════════════════════════════════════════════════════════════════
//
// El criterio estrella de T6.1 es «borrador < 5 min», y un `elapsed_ms` clavado a CERO
// lo cumple: es la tautología con números contra la que avisa la casa —un assert que no
// distingue «se midió y salió rápido» de «no se midió nada»—. Está comprobado: con
// `ElapsedMS: 0` en `stages/draft.go`, la desigualdad `< 5 min` seguía VERDE.
//
// Por eso el reloj del borrador se inyecta (`stages.ConReloj`) devolviendo un instante
// FIJO a `fusEsperaDelCliente` del `message_ts`, y el test exige IGUALDAD EXACTA contra
// ese número. Así caen las dos familias de fallo:
//
//   - un `elapsed_ms` que no se calcula (0, o cualquier constante) ⇒ rojo;
//   - un `elapsed_ms` medido desde otro origen —el reloj del servidor en vez de
//     `message_ts`— ⇒ rojo, porque daría meses y no 90 s.
//
// La desigualdad `< 5 min` se conserva ADEMÁS de la igualdad, porque es la que cita el
// plan; lo que no puede es ser la única.
//
// ⚠️ 90 s es una espera SIMULADA, no medida: con proveedor falso no hay latencia que
// medir. Lo que este test acredita es que el número se calcula y de dónde sale, jamás
// cuánto tarda el pipeline en campo. El número elegido no coincide con ningún otro del
// fichero, para que un golden no pueda pasar con los campos cruzados.
const fusEsperaDelCliente = 90 * time.Second

// fusEscenario es lo que el Acto 1 deja en pie y los actos siguientes consumen. Viaja
// en un struct porque son siete piezas y pasarlas sueltas por seis funciones convertiría
// cada firma en una lista donde el orden se equivoca en silencio.
type fusEscenario struct {
	almacen  *StoreEnMemoria
	flujos   *store.MemoryRepository
	bandeja  *intakes.MemoryStore
	prov     *fusProveedor
	fila     Fila
	intakeID string
	coste    time.Duration
}

// fusExigeLaVentanaCerrada comprueba lo que el Acto 0 tenía que dejar: UNA ventana
// cerrada, con las cinco referencias, la hora del cliente y el sobre sellado.
func fusExigeLaVentanaCerrada(t *testing.T, jobs *intake.MemoryStore) intake.Job {
	t.Helper()
	abierto := fusUnicoJob(t, jobs)
	if abierto.Status != intake.StatusPending {
		t.Fatalf("la ventana tenía que quedar CERRADA (`pending`) al vencer su plazo; quedó %q", abierto.Status)
	}
	if len(abierto.SourceRefs) != len(fusMensajes) {
		t.Fatalf("la ventana tenía que acumular %d referencias (una por mensaje) y tiene %d: %v",
			len(fusMensajes), len(abierto.SourceRefs), abierto.SourceRefs)
	}
	if !abierto.MessageTS.Equal(fusPrimerMensaje) {
		t.Fatalf("`message_ts` tiene que ser el del PRIMER mensaje (%s) y es %s: la base de fechas se perdió",
			fusPrimerMensaje, abierto.MessageTS)
	}
	if !abierto.SourceText.Complete() {
		t.Fatal("la ventana cerró SIN sobre: el compositor del flush no escribió el literal y el pipeline no tendría nada que interpretar")
	}
	return abierto
}

// fusCorrerLaCadena puentea la COSTURA 1 —copia la fila cerrada al store del worker— y
// corre las cinco etapas REALES con el proveedor falso detrás.
//
// La copia es TAL CUAL: si el agregador no hubiera compuesto el sobre, entraría vacío y
// el worker fallaría el job, que es lo que pasaría en campo. Ver la cabecera del fichero.
func fusCorrerLaCadena(ctx context.Context, t *testing.T, log *captor,
	clave intake.WindowKey, abierto intake.Job) fusEscenario {
	t.Helper()

	almacen := NuevoStoreEnMemoria(time.Now)
	jobID := almacen.Sembrar(Fila{
		Key:        clave,
		MessageTS:  abierto.MessageTS,
		SourceRefs: abierto.SourceRefs,
		SourceText: abierto.SourceText,
		CreatedAt:  abierto.CreatedAt,
	})

	prov := &fusProveedor{}
	flujos := store.NewMemoryRepository()
	bandeja := intakes.NewMemoryStore()
	// El reloj del borrador: instante FIJO, a `fusEsperaDelCliente` del mensaje. Ver
	// allí por qué no es `time.Now` ni una función del coste real.
	relojDelBorrador := func() time.Time { return fusPrimerMensaje.Add(fusEsperaDelCliente) }

	p2, err := stages.NewP2(log, selectorFijo{prov: prov}, almacen)
	if err != nil {
		t.Fatalf("construir P2: %v", err)
	}
	p3, err := stages.NewP3(log, selectorFijo{prov: prov}, almacen)
	if err != nil {
		t.Fatalf("construir P3: %v", err)
	}
	p4, err := stages.NewP4(log, selectorFijo{prov: prov}, almacen, stages.ZonaPorDefecto)
	if err != nil {
		t.Fatalf("construir P4: %v", err)
	}
	etapaMatch, err := stages.NewMatch(log, almacen)
	if err != nil {
		t.Fatalf("construir el match: %v", err)
	}
	etapaDraft, err := stages.NewDraft(log, almacen, flujos, bandeja, flujos, stages.ConReloj(relojDelBorrador))
	if err != nil {
		t.Fatalf("construir el borrador: %v", err)
	}
	w, err := NewWorker(log, almacen, p2, p3, p4, etapaMatch, etapaDraft,
		fusCatalogoDeFusion(t), fusCifra{}, Config{}, ConZonasDeEnvio(bandeja))
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}

	arranque := time.Now()
	w.Drenar(ctx)
	coste := time.Since(arranque)

	fila := fusFila(t, almacen, jobID)
	if fila.Status != intake.StatusDone {
		t.Fatalf("el job tenía que terminar en `done` y quedó %q (error %q): %s",
			fila.Status, fila.Error, log.volcado())
	}
	if fila.IntakeID == "" {
		t.Fatalf("el job terminó SIN `intake_id`: no nació ninguna solicitud. %s", log.volcado())
	}
	// Las cinco etapas corrieron UNA vez cada una y con el proveedor de verdad detrás.
	if got := prov.cuentas(); got != (fusCuentas{Ideas: 1, Specs: 3, Cantidades: 1}) {
		t.Fatalf("las llamadas al modelo son %+v; se esperaba 1 a P2, 3 a P3 (una por idea) y 1 a P4", got)
	}

	return fusEscenario{
		almacen: almacen, flujos: flujos, bandeja: bandeja, prov: prov,
		fila: fila, intakeID: fila.IntakeID, coste: coste,
	}
}

// fusExigeElPresupuesto comprueba las CUATRO líneas y su contenido: tres productos en
// orden y el envío el último. Contar líneas no basta —un artefacto con cuatro renglones
// vacíos también tiene cuatro—, así que cada una se mira por sku y etiqueta.
func fusExigeElPresupuesto(t *testing.T, esc fusEscenario) stages.ArtefactoMatch {
	t.Helper()
	var presupuesto stages.ArtefactoMatch
	if err := json.Unmarshal(esc.fila.Artifacts[intake.StageMatch], &presupuesto); err != nil {
		t.Fatalf("el artefacto del match no decodifica: %v", err)
	}
	if len(presupuesto.Lines) != 4 {
		t.Fatalf("el presupuesto tenía que salir con 3 líneas de producto + envío y tiene %d: %+v",
			len(presupuesto.Lines), presupuesto.Lines)
	}
	fusExigeLinea(t, presupuesto.Lines[0], stages.KindMatched, "TORTA-CHOC", "Torta de chocolate")
	fusExigeLinea(t, presupuesto.Lines[1], stages.KindMatched, "TORTA-VAIN", "Torta de vainilla")
	fusExigeLinea(t, presupuesto.Lines[2], stages.KindMatched, "TEQ-30", "Tequeños congelados")
	if presupuesto.Lines[3].Kind != stages.KindShipping {
		t.Fatalf("la última línea tiene que ser la de envío y es %q", presupuesto.Lines[3].Kind)
	}

	// El pack del cliente sobrevive a tomar el nombre del catálogo, y el añadido que
	// el catálogo NO tiene cae a personalización en vez de inventar una línea con
	// precio (D-044.14 / REQ-17).
	if teq := presupuesto.Lines[2]; teq.UnitKind != llm.UnitKindPackage || teq.PackageSize != 30 {
		t.Fatalf("los tequeños perdieron el paquete: unit_kind=%q package_size=%d", teq.UnitKind, teq.PackageSize)
	}
	if choc := presupuesto.Lines[0]; !strings.Contains(choc.Customization, "decoración infantil") {
		t.Fatalf("«decoración infantil» no está en el catálogo: tenía que caer a la personalización de su línea y la línea dice %q",
			choc.Customization)
	}
	return presupuesto
}

// fusExigeMatchDirecto es el criterio «match directo 3 de 3».
//
// 🔴 SE MIRA LA PROCEDENCIA, NO EL `kind`. Afirmar solo `matched` dejaría pasar un match
// resuelto por la ZONA GRIS —el escalón caro, el que llama al modelo—, y eso no es
// «directo»: es lo contrario. Por eso se exige la estrategia determinista y, además,
// que el escalón caro no se haya llamado ni una vez.
func fusExigeMatchDirecto(t *testing.T, presupuesto stages.ArtefactoMatch) {
	t.Helper()
	directos := 0
	for _, l := range presupuesto.Lines[:3] {
		if l.Match != nil && l.Match.Strategy == "exact" && l.Match.Confidence == 1.0 {
			directos++
		}
	}
	if directos != 3 {
		t.Fatalf("match directo %d de 3; las procedencias son %s", directos, fusProcedencias(presupuesto.Lines[:3]))
	}
	if presupuesto.GrayZoneCalls != 0 {
		t.Fatalf("el escalón caro no tenía que hacer falta y se llamó %d veces", presupuesto.GrayZoneCalls)
	}
}

// fusExigeCeroDatosFaltantes es el criterio «datos faltantes en la 1.ª pasada = 0».
func fusExigeCeroDatosFaltantes(t *testing.T, log *captor, presupuesto stages.ArtefactoMatch) {
	t.Helper()
	if faltan := fusDatosFaltantes(t, log, presupuesto); faltan != 0 {
		t.Fatalf("datos faltantes en la 1.ª pasada = %d, y el criterio exige 0: %s", faltan, log.volcado())
	}
}

// fusExigeLaRevisionUno mira LA entrega del pipeline: la revisión interpretada, con su
// fecha calculada, su literal intacto y sus dos preguntas preparadas.
func fusExigeLaRevisionUno(t *testing.T, esc fusEscenario) {
	t.Helper()
	revisiones := esc.bandeja.Revisions(esc.intakeID)
	if len(revisiones) != 1 {
		t.Fatalf("el pipeline escribe UNA revisión y hay %d", len(revisiones))
	}
	if revisiones[0].Kind != intakes.RevisionKindInterpreted || revisiones[0].CreatedBy != intakes.RevisionBySystem {
		t.Fatalf("la revisión 1 es %q firmada por %q; tenía que ser %q/%q",
			revisiones[0].Kind, revisiones[0].CreatedBy, intakes.RevisionKindInterpreted, intakes.RevisionBySystem)
	}
	var borrador stages.PayloadRevision
	if err := json.Unmarshal(revisiones[0].Payload, &borrador); err != nil {
		t.Fatalf("el payload de la revisión 1 no decodifica: %v", err)
	}
	if borrador.DeliveryDate != fusFechaEntrega {
		t.Fatalf("la fecha de entrega es %q y tenía que ser %q: «%s» contra el %s",
			borrador.DeliveryDate, fusFechaEntrega, fusEvidenciaEntrega, fusPrimerMensaje.Format(time.DateOnly))
	}
	if !borrador.MessageTS.Equal(fusPrimerMensaje) {
		t.Fatalf("la revisión pinta la hora del cliente y trae %s en vez de %s", borrador.MessageTS, fusPrimerMensaje)
	}
	if borrador.SourceText != casebank.TextoCasoAmbar {
		t.Fatal("el literal que el dueño ve al lado de la interpretación NO es el que entró: el original se perdió por el camino")
	}
	if len(borrador.SuggestedQuestions) != 2 {
		t.Fatalf("el borrador tenía que llevar 2 preguntas preparadas y lleva %d: %q",
			len(borrador.SuggestedQuestions), borrador.SuggestedQuestions)
	}
}

// fusExigeElKPI afirma el número del criterio estrella, y lo afirma DOS veces sobre los
// DOS sitios donde vive: el artefacto del job y la fila de telemetría.
//
// 🔴 LOS DOS, Y NO UNO. `draft.go` publica `elapsed_ms` en `artifacts.draft` y en
// `flow_events` desde la MISMA variable; comprobar solo uno dejaría que se
// desincronizaran sin que nada lo dijera, que es el defecto «dos números para el mismo
// dato» que la casa ya tiene documentado.
func fusExigeElKPI(t *testing.T, esc fusEscenario) {
	t.Helper()
	esperado := fusEsperaDelCliente.Milliseconds()

	var artDraft stages.ArtefactoDraft
	if err := json.Unmarshal(esc.fila.Artifacts[intake.StageDraft], &artDraft); err != nil {
		t.Fatalf("el artefacto del borrador no decodifica: %v", err)
	}
	if artDraft.ElapsedMS != esperado {
		t.Fatalf("`elapsed_ms` vale %d ms y tenía que valer EXACTAMENTE %d (la espera del cliente desde "+
			"`message_ts`). Un valor distinto significa que el número no se calcula, o que no se calcula "+
			"desde `message_ts`", artDraft.ElapsedMS, esperado)
	}
	// Y el criterio tal como lo cita el plan, que se conserva ADEMÁS de la igualdad.
	if artDraft.ElapsedMS < 0 || artDraft.ElapsedMS >= (5*time.Minute).Milliseconds() {
		t.Fatalf("`elapsed_ms` vale %d ms y el KPI del plan es < 5 min (300000 ms)", artDraft.ElapsedMS)
	}

	eventos := esc.flujos.FlowEvents()
	if len(eventos) != 1 {
		t.Fatalf("el borrador publica UNA fila de telemetría y hay %d", len(eventos))
	}
	enLaMetrica, ok := eventos[0].Payload["elapsed_ms"].(int64)
	if !ok {
		t.Fatalf("la fila de telemetría no trae `elapsed_ms` como entero: %#v", eventos[0].Payload["elapsed_ms"])
	}
	if enLaMetrica != esperado {
		t.Fatalf("la telemetría publica elapsed_ms=%d y el artefacto %d: dos números para el mismo dato",
			enLaMetrica, artDraft.ElapsedMS)
	}
}

// fusApuntarLosTiempos deja el desglose en la salida del test, para la bitácora. Van por
// `t.Logf` y no por aserción: con un proveedor falso son coste de cómputo, no una medida
// de campo.
//
// ⚠️ `elapsed_ms` del log de producción es un ENTERO DE MILISEGUNDOS, así que las cinco
// etapas salen a 0: cada una cuesta menos de 1 ms sin modelo detrás. El número con
// resolución de verdad es el de la cadena entera.
func fusApuntarLosTiempos(t *testing.T, log *captor, esc fusEscenario) {
	t.Helper()
	t.Logf("TIEMPOS POR ETAPA, del log de produccion (proveedor FALSO; no es medida de campo): %s", fusTiempos(t, log))
	t.Logf("COSTE REAL DE LA CADENA P2->draft con proveedor FALSO: %s", esc.coste)
	t.Logf("BORRADOR: elapsed_ms afirmado por IGUALDAD contra %d ms (espera SIMULADA desde message_ts=%s)",
		fusEsperaDelCliente.Milliseconds(), fusPrimerMensaje)
}

// ---------------------------------------------------------------------------
// COSTURA 2 Y LOS ACTOS DEL DUEÑO
// ---------------------------------------------------------------------------

// fusMontarLaBandeja puentea la COSTURA 2: la cabecera que el borrador escribió en
// `public.intakes` pasa a la bandeja del dueño, que en producción es la MISMA fila.
//
// 🔴 SE COPIA SIN LÍNEAS, y no es un atajo: es lo que `UpsertIntake` escribe de verdad.
// Ver la cabecera del fichero y lo que eso implica para aprobar.
func fusMontarLaBandeja(t *testing.T, log *captor, esc fusEscenario) (*intakes.Service, *fusCola, *fusEnvios) {
	t.Helper()
	cabeceras := esc.flujos.Intakes()
	if len(cabeceras) != 1 || cabeceras[0].ID != esc.intakeID {
		t.Fatalf("tenía que nacer UNA cabecera con el id del job y hay %d: %+v", len(cabeceras), cabeceras)
	}
	if cabeceras[0].EventID != fusEvento {
		t.Fatalf("la solicitud tiene que declarar su evento padre (%s) y declara %q", fusEvento, cabeceras[0].EventID)
	}
	esc.bandeja.Add(fusTenant, intakes.Intake{
		ID:        cabeceras[0].ID,
		ContactID: cabeceras[0].ContactID,
		SessionID: cabeceras[0].SessionID,
		Status:    cabeceras[0].Status,
	})
	esc.bandeja.BindEvent(esc.intakeID, fusEvento)
	esc.bandeja.SetDepositTemplate(fusTenant,
		"Para reservar la fecha te pedimos una seña. Total del pedido: {total}. Tenés {plazo} días para transferirla.", 3)

	cola := &fusCola{}
	envios := &fusEnvios{}
	notificador := intakes.NewNotifier(envios, fusDestinos{}, esc.bandeja, log)
	empuje := crmpush.NewRevisionPusher(crmpush.NewPusher(log, cola, fusPuerta{}), log)
	svc := intakes.NewService(esc.bandeja,
		intakes.WithNotifier(notificador),
		intakes.WithQuoteSender(notificador),
		intakes.WithCRMPusher(empuje),
	)
	return svc, cola, envios
}

// fusTotalCorregido es el total tras la corrección de la dueña: 2900 + 3900 + 490 + 600.
const fusTotalCorregido = 7890.0

// fusCorregirComoLaDueña es el ACTO 2: 10-12 → 15 porciones, + oreo, y los precios que
// el catálogo no podía poner.
func fusCorregirComoLaDueña(ctx context.Context, t *testing.T, svc *intakes.Service,
	esc fusEscenario, cola *fusCola) {
	t.Helper()
	corregidas := []intakes.Item{
		{SKU: "TORTA-CHOC#15", Label: "Torta de chocolate — 15 porciones", Qty: 1, UnitPrice: 2900,
			Customization: "decoración infantil"},
		{SKU: "TORTA-VAIN", Label: "Torta de vainilla", Qty: 1, UnitPrice: 3900,
			Customization: "lluvia de colores, dulce de leche y merengue"},
		{SKU: "TEQ-30", Label: "Tequeños congelados", Qty: 1, UnitPrice: 490},
		{SKU: "TOP-OREO", Label: "Topping de Oreo", Qty: 1, UnitPrice: 600},
	}

	detalle, err := svc.ReplaceItems(ctx, fusTenant, esc.intakeID, corregidas, intakes.EditAsCorrection)
	if err != nil {
		t.Fatalf("la corrección de la dueña falló: %v", err)
	}
	if detalle.Total != fusTotalCorregido {
		t.Fatalf("el total tras la corrección es %v y tenía que ser %v", detalle.Total, fusTotalCorregido)
	}
	corr := fusUltimaRevision(t, esc.bandeja, esc.intakeID)
	if corr.Kind != intakes.RevisionKindCorrected || corr.RevisionNo != 2 {
		t.Fatalf("la corrección tenía que dejar la revisión 2 `%s` y dejó la %d `%s`",
			intakes.RevisionKindCorrected, corr.RevisionNo, corr.Kind)
	}
	if corr.CreatedBy != intakes.RevisionByOwner {
		t.Fatalf("la corrección la firma el ROL `%s` y la firmó %q", intakes.RevisionByOwner, corr.CreatedBy)
	}
	if n := cola.cuantos(crmpush.Kind); n != 1 {
		t.Fatalf("la corrección tenía que encolar UN `%s` y encoló %d", crmpush.Kind, n)
	}
}

// fusTextoDeLaDuena es la cotización con el formato de la dueña, palabra por palabra.
const fusTextoDeLaDuena = "¡Hola Ambar! Te paso el presupuesto para el miércoles 22/07:\n" +
	"• Torta de chocolate 15 porciones (decoración infantil): $2900\n" +
	"• Torta de vainilla con lluvia de colores: $3900\n" +
	"• Paquete de tequeños congelados x30: $490\n" +
	"• Topping de Oreo: $600\n" +
	"Total: $7890"

// fusAprobarYResponder es el ACTO 3: aprobar, responderle al cliente por la sesión del
// negocio y dejar el rastro de lo que se envió.
func fusAprobarYResponder(ctx context.Context, t *testing.T, svc *intakes.Service,
	esc fusEscenario, cola *fusCola, envios *fusEnvios) {
	t.Helper()
	aprobada, err := svc.Approve(ctx, fusTenant, esc.intakeID, fusTextoDeLaDuena)
	if err != nil {
		t.Fatalf("aprobar falló: %v", err)
	}
	if aprobada.Status != intakes.StatusConfirmed {
		t.Fatalf("la solicitud quedó en %q y tenía que quedar en %q", aprobada.Status, intakes.StatusConfirmed)
	}
	apr := fusUltimaRevision(t, esc.bandeja, esc.intakeID)
	if apr.Kind != intakes.RevisionKindApproved || apr.RevisionNo != 3 {
		t.Fatalf("aprobar tenía que dejar la revisión 3 `%s` y dejó la %d `%s`",
			intakes.RevisionKindApproved, apr.RevisionNo, apr.Kind)
	}

	// LA RESPUESTA SALE POR LA SESIÓN DEL NEGOCIO, con el texto de la dueña BYTE A
	// BYTE al principio y la plantilla de seña del tenant detrás.
	if len(envios.salidas) != 1 {
		t.Fatalf("tenía que salir UN mensaje al cliente y salieron %d: %+v", len(envios.salidas), envios.salidas)
	}
	salida := envios.salidas[0]
	if salida.sesion != fusSesion {
		t.Fatalf("la cotización salió por la sesión %q y tenía que salir por la del negocio (%q)", salida.sesion, fusSesion)
	}
	if !strings.HasPrefix(salida.texto, fusTextoDeLaDuena) {
		t.Fatalf("el mensaje NO empieza por el texto de la dueña; salió:\n%s", salida.texto)
	}
	if !strings.Contains(salida.texto, "seña") || !strings.Contains(salida.texto, "$7890.00") {
		t.Fatalf("la plantilla de seña no se adjuntó o no se rellenó el total; salió:\n%s", salida.texto)
	}
	if apr.RenderedText != salida.texto {
		t.Fatal("la revisión `approved` no guarda EXACTAMENTE lo que salió por el cable: el registro deja de ser lo que se envió")
	}
	fusExigeElPushDeLaAprobacion(t, esc, cola)
}

// fusExigeElPushDeLaAprobacion mira el SEGUNDO `intake.push`: el de la aprobación, con
// el estado REAL y el número de revisión que la base acaba de asignar.
func fusExigeElPushDeLaAprobacion(t *testing.T, esc fusEscenario, cola *fusCola) {
	t.Helper()
	empujes := cola.filas(crmpush.Kind)
	if len(empujes) != 2 {
		t.Fatalf("el guion tenía que encolar DOS `%s` (corrección y aprobación) y encoló %d", crmpush.Kind, len(empujes))
	}
	var doc struct {
		Verb            string  `json:"verb"`
		IntakeID        string  `json:"intake_id"`
		LifecycleStatus string  `json:"lifecycle_status"`
		RevisionNo      int     `json:"revision_no"`
		Total           float64 `json:"total"`
	}
	if err := json.Unmarshal(empujes[1], &doc); err != nil {
		t.Fatalf("el documento encolado no decodifica: %v", err)
	}
	if doc.Verb != crmpush.Verb || doc.IntakeID != esc.intakeID ||
		doc.LifecycleStatus != intakes.StatusConfirmed || doc.RevisionNo != 3 || doc.Total != fusTotalCorregido {
		t.Fatalf("el `%s` de la aprobación salió mal: %+v", crmpush.Kind, doc)
	}
}

// ---------------------------------------------------------------------------
// ACTO 0 — LA CAPTACIÓN, APARTE
// ---------------------------------------------------------------------------

// fusCaptar mete los cinco mensajes de Ambar por el agregador, le pide a P1 la
// clasificación y cierra la ventana POR RELOJ. Devuelve el store del agregador y la
// clave.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 EL CIERRE ES POR VENTANA, NO POR INTENT, Y ESO ES EL DISEÑO
// ════════════════════════════════════════════════════════════════════════════
//
// La señal de P1 PUEDE NO LLEGAR NUNCA —vía caída, cola llena, catálogo sin publicar,
// respuesta que llega tarde—, así que el adelanto por intent es un ATAJO y los dos
// plazos de la ventana son el único camino garantizado. `aggregator.go` lo dice con
// todas las letras y T1.7 manda probar el flush por ventana como camino PRINCIPAL. Un
// guion que cerrara por intent estaría retratando el caso afortunado y llamándolo
// «el guion completo».
//
// Aquí se retrata entero y en tres pasadas:
//
//  1. barrido con la ráfaga recién terminada ⇒ CERO ventanas cerradas. Es la afirmación
//     con contenido: sin pista y sin plazo vencido no se cierra nada, y los cinco
//     mensajes siguen acumulándose en UNA sola ventana;
//  2. se adelanta el reloj al plazo ⇒ UNA cerrada, y con ella el sobre sellado;
//  3. P1 contesta DESPUÉS, ya con la ventana cerrada ⇒ inocuo: ni reabre, ni duplica.
//
// # LOS NÚMEROS DE ESTA RÁFAGA, QUE SON UN DATO DEL CASO
//
// La ventana nace a las 9:55:00 y el último mensaje llega a los 115 s. De los dos
// plazos, el que vence primero es el TECHO (`aggregation_max_seconds`, 120 s desde que
// nació) y no el silencio (45 s desde el último mensaje, que caería a los 160 s). O
// sea: en campo esta ráfaga la cierra el techo, cinco segundos después del último
// mensaje, y aun así los CINCO caben dentro — que es justo lo que la ventana híbrida
// de T1.8-1 existe para conseguir. Por eso el paso 2 avanza al techo y no al silencio:
// es el plazo que de verdad gobierna este caso.
func fusCaptar(ctx context.Context, t *testing.T, log *captor) (*intake.MemoryStore, intake.WindowKey) {
	t.Helper()

	// El reloj lo comparten el store y el agregador: con dos, el plazo se mediría
	// restando instantes de relojes distintos, que es el defecto que la casa ya tiene
	// documentado.
	rel := &reloj{t: fusPrimerMensaje}
	jobs := intake.NewMemoryStore(rel.ahora)
	ents := entitlements.NewFake()
	ents.Enable(fusTenant, entitlements.FeatureLLMIntake)

	clave := intake.WindowKey{
		TenantID: fusTenant, SessionID: fusSesion, ContactID: fusContacto, EventID: fusEvento,
	}
	p1 := &fusClasificador{}
	agg := flowruntime.NewIntakeAggregator(log, jobs, store.NewMemoryRepository(), ents,
		flowruntime.WithAggregatorClock(rel.ahora),
		flowruntime.WithSourceComposer(fusCompositor{jobs: jobs}),
		flowruntime.WithAheadRequester(p1))
	p1.agg = agg

	// La ráfaga, cada mensaje en SU instante: `updated_at` —el ancla del silencio— lo
	// escribe el store en cada `OpenOrAppend`, así que observar los cinco con el reloj
	// parado mediría un plazo que en campo no existe.
	for _, m := range fusMensajes {
		rel.avanzar(m.en.Sub(rel.ahora()))
		agg.Observe(ctx, flowruntime.IncomingRef{
			Key: clave, WaMessageID: m.id, MessageTS: m.en, Text: m.texto,
		})
	}
	if p1.pedidas() != len(fusMensajes) {
		t.Fatalf("P1 tenía que recibir los %d mensajes para clasificar y recibió %d", len(fusMensajes), p1.pedidas())
	}

	// (1) TODAVÍA NO. Ni pista ni plazo vencido: la ventana sigue viva con los cinco.
	if cerradas := agg.Sweep(ctx); cerradas != 0 {
		t.Fatalf("el barrido cerró %d ventanas con la ráfaga recién terminada y sin señal de P1; "+
			"tenía que cerrar CERO, o la ráfaga se parte en varios presupuestos", cerradas)
	}

	// (2) EL PLAZO. Se avanza al techo, que para esta ráfaga es el que vence primero.
	rel.avanzar(fusPrimerMensaje.Add(store.DefaultAggregationMax).Sub(rel.ahora()))
	if cerradas := agg.Sweep(ctx); cerradas != 1 {
		t.Fatalf("al vencer el plazo de la ventana tenía que cerrarse UNA y se cerraron %d", cerradas)
	}

	// (3) P1 CONTESTA TARDE. Es un caso NORMAL y no una avería: la inferencia dura
	// segundos y la ventana ya cerró. Su pista no casa con ninguna ventana viva, así
	// que no puede reabrir nada ni parir un segundo job.
	p1.contestar()
	if cerradas := agg.Sweep(ctx); cerradas != 0 {
		t.Fatalf("la clasificación que llega DESPUÉS del cierre cerró %d ventanas más; tenía que ser inocua", cerradas)
	}
	if p1.disparos() != 1 {
		t.Fatalf("P1 tenía que clasificar UNA vez `%s` y clasificó %d", flowruntime.IntentIntakeRequest, p1.disparos())
	}
	return jobs, clave
}

// fusUnicoJob exige que el agregador haya producido EXACTAMENTE una ventana. Los cinco
// mensajes de una misma ráfaga son UN presupuesto: dos jobs aquí serían el pedido
// partido en dos, que es el defecto que la ventana híbrida cerró.
func fusUnicoJob(t *testing.T, jobs *intake.MemoryStore) intake.Job {
	t.Helper()
	todos := jobs.Jobs()
	if len(todos) != 1 {
		t.Fatalf("los %d mensajes tenían que caer en UNA ventana y hay %d jobs", len(fusMensajes), len(todos))
	}
	return todos[0]
}

// ---------------------------------------------------------------------------
// LOS ADJUNTOS — LO QUE SÍ SE REPARTE Y LO QUE HOY **NO** LLEGA AL BORRADOR
// ---------------------------------------------------------------------------

// TestFus_LosAdjuntosDeAmbarSeAnclanBienPeroElWorkerNoLosPasa cubre la última frase del
// enunciado de T6.1 —«fotos ancladas a la línea de la torta 1; audio como adjunto sin
// procesar»— y declara, en vez de disimular, dónde se corta hoy ese camino.
//
// Son TRES afirmaciones y la tercera es la incómoda:
//
//  1. `anclaje.Repartir` cuelga las dos fotos de la línea de la torta 1 y manda el
//     audio a la cabecera con su etiqueta (regla 1: un audio no se ancla nunca);
//  2. la etapa `draft`, si le dan ese reparto, lo escribe donde el contrato §7.4 dice;
//  3. 🔴 EL WORKER NO SE LO DA. `Worker.borrador` pasa `anclaje.Reparto{}` literal, y
//     su propio comentario lo dice: «Media VA EN CERO … el anclaje de adjuntos (T3.3) no
//     tiene de dónde leer los `media refs` con sus instantes». Así que en producción,
//     hoy, NINGÚN borrador del pipeline lleva adjuntos. Esto no es un test que celebre
//     el hueco: es el que lo hace visible y el que se pondrá rojo el día que alguien
//     cablee la lectura y se olvide de este fichero.
//
// 💥 MUTACIÓN EJECUTADA, roja (COMPILA): en `anclaje.Repartir`, apagar la regla 1
// (`if false && esAudio(ref.Kind)`) ⇒ la nota de voz deja de subir a la cabecera y se
// cuelga de una línea de producto, que es justo lo que REQ-29 prohíbe.
func TestFus_LosAdjuntosDeAmbarSeAnclanBienPeroElWorkerNoLosPasa(t *testing.T) {
	ctx := context.Background()
	log := &captor{}

	reparto := fusExigeElReparto(t)
	fusExigeElBorradorConAdjuntos(ctx, t, log, reparto)
	fusExigeQueElWorkerNoPaseLosAdjuntos(ctx, t)
}

// fusConversacionConAdjuntos son los turnos de Ambar con las dos fotos y la nota de voz
// intercaladas, cada adjunto SIN texto —que es como llega una foto suelta por WhatsApp—.
// Los instantes salen de los mismos mensajes del caso, no de un reloj.
func fusConversacionConAdjuntos() []anclaje.Turno {
	return []anclaje.Turno{
		{Seq: 1, En: fusMensajes[0].en, Texto: fusMensajes[0].texto},
		{Seq: 2, En: fusMensajes[1].en, Texto: fusMensajes[1].texto},
		{Seq: 3, En: fusMensajes[1].en.Add(10 * time.Second), Texto: ""},
		{Seq: 4, En: fusMensajes[1].en.Add(15 * time.Second), Texto: ""},
		{Seq: 5, En: fusMensajes[2].en, Texto: fusMensajes[2].texto},
		{Seq: 6, En: fusMensajes[2].en.Add(20 * time.Second), Texto: ""},
		{Seq: 7, En: fusMensajes[3].en, Texto: fusMensajes[3].texto},
	}
}

// fusExigeElReparto es la parte (1): dos fotos a la línea de la torta 1 por PROXIMIDAD
// —llegan pegadas al mensaje que la describe— y el audio a la cabecera por la regla 1,
// que no admite excepción.
func fusExigeElReparto(t *testing.T) anclaje.Reparto {
	t.Helper()
	turnos := fusConversacionConAdjuntos()
	lineas := []anclaje.Linea{
		{Idx: 0, Evidencia: fusEvidenciaChoc, Etiqueta: "Torta de chocolate"},
		{Idx: 1, Evidencia: fusEvidenciaVainilla, Etiqueta: "Torta de vainilla"},
		{Idx: 2, Evidencia: fusEvidenciaTequenos, Etiqueta: "Tequeños congelados"},
		// La de envío va con evidencia VACÍA a propósito: no sale de ninguna frase del
		// cliente y tiene que ser incapaz de atraer una foto.
		{Idx: 3, Evidencia: "", Etiqueta: "Envío"},
	}
	refs := []anclaje.MediaRef{
		{Ref: "wapp/media/torta-1a.jpg", Kind: anclaje.KindImage, Seq: 3, En: turnos[2].En},
		{Ref: "wapp/media/torta-1b.jpg", Kind: anclaje.KindImage, Seq: 4, En: turnos[3].En},
		{Ref: "wapp/media/nota-de-voz.ogg", Kind: anclaje.KindPTT, Seq: 6, En: turnos[5].En},
	}

	reparto := anclaje.Repartir(turnos, lineas, refs, anclaje.Opciones{})
	if got := reparto.PorLinea[0]; len(got) != 2 || got[0].Ref != refs[0].Ref || got[1].Ref != refs[1].Ref {
		t.Fatalf("las dos fotos tenían que colgar de la línea de la torta 1 y el reparto dejó %+v (mapa: %+v)",
			got, reparto.PorLinea)
	}
	if len(reparto.Solicitud) != 1 || reparto.Solicitud[0].Ref != refs[2].Ref {
		t.Fatalf("el audio va SIEMPRE a la cabecera y la cabecera tiene %+v", reparto.Solicitud)
	}
	if reparto.Solicitud[0].Label != anclaje.EtiquetaAudio {
		t.Fatalf("el audio sale sin su etiqueta (%q): el dueño no sabría que hay algo que escuchar", reparto.Solicitud[0].Label)
	}
	return reparto
}

// fusExigeElBorradorConAdjuntos es la parte (2): la etapa `draft`, si le dan el reparto,
// lo escribe donde el contrato §7.4 dice. Se la llama DIRECTAMENTE porque es el único
// modo de ejercitar este camino mientras el worker no lo cablee.
func fusExigeElBorradorConAdjuntos(ctx context.Context, t *testing.T, log *captor, reparto anclaje.Reparto) {
	t.Helper()
	almacen := NuevoStoreEnMemoria(time.Now)
	flujos := store.NewMemoryRepository()
	bandeja := intakes.NewMemoryStore()
	draft, err := stages.NewDraft(log, almacen, flujos, bandeja, flujos)
	if err != nil {
		t.Fatalf("construir el borrador: %v", err)
	}
	jobID := almacen.Sembrar(Fila{
		Key: intake.WindowKey{TenantID: fusTenant, SessionID: fusSesion,
			ContactID: fusContacto, EventID: fusEvento},
		SourceText: intake.SourceText{Enc: []byte("x"), DEK: []byte("y"), KEKID: fusKEK},
		MessageTS:  fusPrimerMensaje,
	})
	job, hubo, err := almacen.ClaimNext(ctx)
	if err != nil || !hubo || job.ID != jobID {
		t.Fatalf("reclamar el job de prueba: hubo=%v err=%v", hubo, err)
	}

	art, err := draft.Run(ctx, job, stages.EntradaDraft{
		Match:        fusMatchDeCuatroLineas(),
		SourceText:   casebank.TextoCasoAmbar,
		FechaEntrega: fusFechaEntrega,
		Media:        reparto,
	})
	if err != nil {
		t.Fatalf("el borrador con adjuntos falló: %v", err)
	}
	var payload stages.PayloadRevision
	if err := json.Unmarshal(bandeja.Revisions(art.IntakeID)[0].Payload, &payload); err != nil {
		t.Fatalf("el payload de la revisión no decodifica: %v", err)
	}
	if len(payload.Lines[0].MediaRefs) != 2 {
		t.Fatalf("la línea de la torta 1 tenía que llevar sus 2 fotos y lleva %d", len(payload.Lines[0].MediaRefs))
	}
	if len(payload.MediaRefs) != 1 || payload.MediaRefs[0].Label != anclaje.EtiquetaAudio {
		t.Fatalf("la cabecera tenía que llevar el audio rotulado y lleva %+v", payload.MediaRefs)
	}
}

// fusExigeQueElWorkerNoPaseLosAdjuntos es la parte (3), la incómoda: el worker construye
// la entrada del borrador con el reparto VACÍO, así que hoy NINGÚN borrador del pipeline
// lleva adjuntos. Ver el docstring del test.
func fusExigeQueElWorkerNoPaseLosAdjuntos(ctx context.Context, t *testing.T) {
	t.Helper()
	almacen := NuevoStoreEnMemoria(time.Now)
	testigo := &draftFalso{store: almacen, intakeID: intakeIDDePrueba}
	fusWorkerConDraft(t, almacen, testigo).Drenar(ctx)

	entrada, recibio := testigo.vioLaEntrada()
	if !recibio {
		t.Fatal("el borrador no llegó a ejecutarse: el test no está mirando nada")
	}
	if len(entrada.Media.PorLinea) != 0 || len(entrada.Media.Solicitud) != 0 {
		t.Fatalf("🎉 el worker YA cablea el anclaje de adjuntos (%+v). Es una BUENA noticia y este test es lo que hay "+
			"que actualizar: quítese este bloque y afírmese el reparto real contra el borrador que produce el worker.",
			entrada.Media)
	}
}

// fusWorkerConDraft arma un worker con las etapas del modelo falsas y el borrador que se
// le pase, sobre el store que se le pase. No comparte código con el guion a propósito:
// aquél prueba el RESULTADO de la cadena, éste una ENTRADA concreta del borrador.
//
// El store viaja por parámetro y no se construye aquí porque quien llama tiene que
// poder dárselo también al doble del borrador: con dos stores, el doble guardaría su
// artefacto en una máquina y el worker leería la otra.
func fusWorkerConDraft(t *testing.T, almacen *StoreEnMemoria, draft EtapaDraft) *Worker {
	t.Helper()
	log := &captor{}
	p2 := &p2Falsa{store: almacen, wants: wantsDePrueba(1)}
	p3 := &p3Falsa{store: almacen, items: itemsDePrueba(1)}
	p4 := &p4Falsa{store: almacen}
	m := &matchFalso{store: almacen}

	w, err := NewWorker(log, almacen, p2, p3, p4, m, draft, catalogoDePrueba(t), cifraFalsa{}, Config{})
	if err != nil {
		t.Fatalf("cablear el worker: %v", err)
	}
	almacen.Sembrar(Fila{
		Key: intake.WindowKey{TenantID: fusTenant, SessionID: fusSesion,
			ContactID: fusContacto, EventID: fusEvento},
		SourceText: intake.SourceText{Enc: []byte("x"), DEK: []byte("y"), KEKID: fusKEK},
		MessageTS:  fusPrimerMensaje,
		CreatedAt:  fusPrimerMensaje,
	})
	return w
}

// fusMatchDeCuatroLineas es el presupuesto de Ambar en la forma que el borrador
// consume. Está escrito a mano y no recalculado por el match: este test es del
// ANCLAJE, y hacerlo depender de la cascada lo pondría rojo por motivos ajenos.
func fusMatchDeCuatroLineas() *stages.ArtefactoMatch {
	precio := func(v float64) *float64 { return &v }
	return &stages.ArtefactoMatch{
		Version: llm.ArtifactVersion,
		Lines: []stages.Linea{
			{Kind: stages.KindMatched, SKU: "TORTA-CHOC", Label: "Torta de chocolate", Qty: 1,
				Evidence: fusEvidenciaChoc},
			{Kind: stages.KindMatched, SKU: "TORTA-VAIN", Label: "Torta de vainilla", Qty: 1,
				UnitPrice: precio(3900), Evidence: fusEvidenciaVainilla},
			{Kind: stages.KindMatched, SKU: "TEQ-30", Label: "Tequeños congelados", Qty: 1,
				UnitPrice: precio(490), Evidence: fusEvidenciaTequenos},
			{Kind: stages.KindShipping, SKU: intakes.ShippingSKU, Label: "Envío", Qty: 1},
		},
	}
}

// ---------------------------------------------------------------------------
// ATREZO — EL MODELO FALSO
// ---------------------------------------------------------------------------

// fusCuentas son las llamadas por etapa. Es lo que permite afirmar «una por job en P2 y
// P4, una POR ÍTEM en P3» sin leer el código de las etapas.
type fusCuentas struct{ Ideas, Specs, Cantidades int }

// fusProveedor es el modelo bien portado del caso Ambar: contesta lo que la
// interpretación curada de `casebank` dice que hay que contestar, y NADA que no esté en
// el literal.
//
// 🔴 EMBEBE `llm.LLMProvider` Y SOLO IMPLEMENTA TRES MÉTODOS. Es el truco de la casa: el
// puerto tiene cinco y este doble solo usa los tres del pipeline de presupuestos, así
// que llamar a cualquiera de los otros dos revienta con un nil de verdad en vez de
// devolver un silencio que el test tomaría por bueno.
//
// 🔴 COMPRUEBA EL LITERAL QUE LE LLEGA. Si el sobre no se compuso, o se descifró a otra
// cosa, o alguna etapa recortó el texto, aquí sale un error de calidad y el job muere:
// sin esta comprobación el guion podría pasar con un literal vacío, porque las
// respuestas están escritas de antemano.
type fusProveedor struct {
	llm.LLMProvider
	mu sync.Mutex
	c  fusCuentas
}

func (p *fusProveedor) cuentas() fusCuentas {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.c
}

func (p *fusProveedor) exigeElLiteral(texto string) error {
	if texto != casebank.TextoCasoAmbar {
		return fmt.Errorf("%w: el literal que llegó al modelo NO es el del caso (%d bytes)", llm.ErrLLMQuality, len(texto))
	}
	return nil
}

func (p *fusProveedor) ExtractMainIdeas(_ context.Context, in llm.ExtractMainIdeasInput,
	_ llm.Options) (json.RawMessage, error) {
	if err := p.exigeElLiteral(in.SourceText); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.c.Ideas++
	p.mu.Unlock()
	return fusJSON(map[string]any{
		"version": llm.ArtifactVersion,
		"wants": []any{
			map[string]any{"idea": fusIdeaChoc, "evidence": fusEvidenciaChoc},
			map[string]any{"idea": fusIdeaVainilla, "evidence": fusEvidenciaVainilla},
			map[string]any{"idea": fusIdeaTequenos, "evidence": fusEvidenciaTequenos},
		},
		"delivery_hint": map[string]any{
			"text":     "el miércoles de la semana que viene",
			"evidence": fusEvidenciaEntrega,
		},
	})
}

func (p *fusProveedor) ExtractItemSpecs(_ context.Context, in llm.ExtractItemSpecsInput,
	_ llm.Options) (json.RawMessage, error) {
	if err := p.exigeElLiteral(in.SourceText); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.c.Specs++
	p.mu.Unlock()

	var item map[string]any
	switch in.Idea {
	case fusIdeaChoc:
		item = map[string]any{
			"product": fusIdeaChoc, "variant": "10 o 12 porciones",
			"addon_candidates": []string{"decoración infantil"},
			"notes":            "bizcocho húmedo de chocolate con crema de chocolate",
			"evidence":         fusEvidenciaChoc,
		}
	case fusIdeaVainilla:
		item = map[string]any{
			"product": fusIdeaVainilla, "variant": "25 o 30 porciones",
			"customizations": []string{"lluvia de colores, dulce de leche y merengue"},
			"evidence":       fusEvidenciaVainilla,
		}
	case fusIdeaTequenos:
		item = map[string]any{"product": fusIdeaTequenos, "evidence": fusEvidenciaTequenos}
	default:
		// Una idea que P2 no emitió es un fan-out que se inventó un ítem: se denuncia
		// aquí y no se contesta con algo plausible.
		return nil, fmt.Errorf("%w: P3 preguntó por una idea que P2 no dejó viva", llm.ErrLLMQuality)
	}
	return fusJSON(map[string]any{"version": llm.ArtifactVersion, "items": []any{item}})
}

func (p *fusProveedor) NormalizeQuantities(_ context.Context, in llm.NormalizeQuantitiesInput,
	_ llm.Options) (json.RawMessage, error) {
	if err := p.exigeElLiteral(in.SourceText); err != nil {
		return nil, err
	}
	if !in.MessageTS.Equal(fusPrimerMensaje) {
		// El prompt de P4 imprime esta fecha como referencia. Que llegue mal no rompe
		// la fecha final —la calcula Go—, pero sí el contexto del modelo.
		return nil, fmt.Errorf("%w: P4 recibió %s como referencia y el mensaje es de %s",
			llm.ErrLLMQuality, in.MessageTS, fusPrimerMensaje)
	}
	p.mu.Lock()
	p.c.Cantidades++
	p.mu.Unlock()
	return fusJSON(map[string]any{
		"version":       llm.ArtifactVersion,
		"delivery_date": fusFechaEntrega,
		"items": []any{
			map[string]any{"product": fusIdeaChoc, "qty": 1, "evidence": fusEvidenciaChoc,
				"range": map[string]any{"min": 10, "max": 12, "unit": "porciones"}},
			map[string]any{"product": fusIdeaVainilla, "qty": 1, "evidence": fusEvidenciaVainilla,
				"range": map[string]any{"min": 25, "max": 30, "unit": "porciones"}},
			map[string]any{"product": fusIdeaTequenos, "qty": 1, "evidence": fusEvidenciaTequenos,
				"unit_kind": llm.UnitKindPackage, "package_size": 30},
		},
	})
}

// fusJSON serializa la respuesta del modelo falso. El error se propaga en vez de
// entrar en pánico: una respuesta que no serializa es un fallo del test, y el job
// tiene que morir por donde moriría en producción.
func fusJSON(v map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: la respuesta del modelo falso no serializa: %w", llm.ErrLLMQuality, err)
	}
	return raw, nil
}

// ---------------------------------------------------------------------------
// ATREZO — EL RESTO DEL DECORADO
// ---------------------------------------------------------------------------

// fusCifra abre el sobre del literal. Comprueba el `kek_id` porque el sobre viaja con
// él y una KEK que no se mira es una KEK que nadie echaría de menos.
type fusCifra struct{}

func (fusCifra) Decrypt(_, _ []byte, keyID string) (string, error) {
	if keyID != fusKEK {
		return "", fmt.Errorf("fus: el sobre viene con la KEK %q y la que abre es %q", keyID, fusKEK)
	}
	return casebank.TextoCasoAmbar, nil
}

// fusCompositor es el SourceComposer del flush: sella el literal del caso sobre la
// ventana que se acaba de cerrar. Es el papel de `runtime.SourceTextComposer` sin su
// lectura del hilo ni su envelope real, que tienen sus propios tests.
type fusCompositor struct{ jobs *intake.MemoryStore }

func (c fusCompositor) ComposeAtFlush(ctx context.Context, key intake.WindowKey) error {
	_, err := c.jobs.PutSourceText(ctx, key, intake.SourceText{
		Enc: []byte("sobre-sellado-del-caso-ambar"), DEK: []byte("dek"), KEKID: fusKEK,
	})
	return err
}

// fusClasificador es P1: recibe el texto que el agregador le pasa y devuelve la
// clasificación por el camino de vuelta (`OnClassified`), igual que el pool real.
//
// 🔴 NO CONTESTA DENTRO DE `Request`, Y ESA ES LA MITAD DEL DOBLE. El pool real encola
// la petición y la resuelve en OTRA goroutine, segundos después; un doble que
// contestara en línea convertiría una llamada al modelo en algo instantáneo y dejaría
// pasar cualquier diseño que dependa de esa instantaneidad —justo lo que REQ-35
// prohíbe—. Aquí la respuesta se guarda y el test decide CUÁNDO llega, que es la única
// forma de retratar el caso normal: llega tarde.
//
// Clasifica `intake_request` cuando el mensaje pide un presupuesto o un precio, y no
// por posición: un doble que contestara «sí» a todo no distinguiría un saludo de una
// solicitud, que es la mitad de lo que P1 existe para hacer.
type fusClasificador struct {
	agg       *flowruntime.IntakeAggregator
	mu        sync.Mutex
	n         int
	ok        int
	pendiente *intake.WindowKey
}

func (c *fusClasificador) Request(key intake.WindowKey, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	if strings.Contains(text, "presupuesto") || strings.Contains(text, "precio") {
		// La ráfaga trae DOS mensajes que piden precio; el pool contestaría a los dos,
		// pero para el disparo da igual cuál llegue: la pista es de la VENTANA, no del
		// mensaje. Se guarda una sola y así el recuento de disparos dice algo.
		k := key
		c.pendiente = &k
	}
}

// contestar entrega la clasificación pendiente. La llama el test cuando toca.
func (c *fusClasificador) contestar() {
	c.mu.Lock()
	k := c.pendiente
	if k != nil {
		c.pendiente = nil
		c.ok++
	}
	c.mu.Unlock()
	if k != nil {
		c.agg.OnClassified(*k, flowruntime.IntentIntakeRequest, 0.95)
	}
}

func (c *fusClasificador) pedidas() int  { c.mu.Lock(); defer c.mu.Unlock(); return c.n }
func (c *fusClasificador) disparos() int { c.mu.Lock(); defer c.mu.Unlock(); return c.ok }

// fusCatalogoDeFusion es el catálogo del tenant, y es LA VARIABLE del criterio «match
// directo 3 de 3»: los tres productos que Ambar pide existen en la carta con el nombre
// con el que ella los nombra, así que el escalón EXACTO los resuelve.
//
// ⚠️ Con otra carta el mismo texto da otro resultado, y está medido en el repo: con el
// `catalogoAmbar()` de `stages` —donde la torta de vainilla no existe y la de chocolate
// se llama «Torta chocolate húmedo + crema choc.»— el replay del mismo caso da UN match
// exacto y dos ítems que solo resuelve el escalón caro (`TestMatch_ReplayDeAmbar`). El
// 3 de 3 es una propiedad del PAR (interpretación, catálogo), no del pipeline solo.
//
// La torta de chocolate se vende por PRESENTACIONES a propósito: es lo que hace que el
// rango 10-12 deje la línea sin precio con dos opciones, y de ahí sale una de las dos
// preguntas preparadas del borrador (la otra es la del envío).
func fusCatalogoDeFusion(t *testing.T) *fusCatalogo {
	t.Helper()
	idx, err := catalogo.Construir(cart.Catalog{Categories: []cart.Category{
		{Code: "1", Label: "Tortas", Items: []cart.Article{
			{Code: "1", SKU: "TORTA-CHOC", Label: "Torta de chocolate", Variants: []cart.Variant{
				{Code: "10", Label: "10 porciones", Price: 2100},
				{Code: "12", Label: "12 porciones", Price: 2400},
				{Code: "25", Label: "25 porciones", Price: 3900},
			}},
			{Code: "2", SKU: "TORTA-VAIN", Label: "Torta de vainilla", Price: 3900},
		}},
		{Code: "2", Label: "Congelados", Items: []cart.Article{
			{Code: "1", SKU: "TEQ-30", Label: "Tequeños congelados", Price: 490},
		}},
	}}, textmatch.Normalize)
	if err != nil {
		t.Fatalf("construir el catálogo de Fusión: %v", err)
	}
	return &fusCatalogo{idx: idx}
}

// fusCatalogo satisface el puerto `Catalogos` del worker.
type fusCatalogo struct{ idx *catalogo.Indice }

func (c *fusCatalogo) Obtener(_ context.Context, _ string) (*catalogo.Indice, error) {
	return c.idx, nil
}

// fusEnvio es UN mensaje que salió hacia el cliente.
type fusEnvio struct{ sesion, destino, texto string }

// fusEnvios es el Gateway: guarda lo que se le manda. Es la única forma de afirmar
// «salió UN mensaje, por la sesión del negocio, con este texto exacto».
type fusEnvios struct {
	mu      sync.Mutex
	salidas []fusEnvio
}

func (e *fusEnvios) SendText(_ context.Context, sessionID, to, text string) (*cloudlinkv1.Ack, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.salidas = append(e.salidas, fusEnvio{sesion: sessionID, destino: to, texto: text})
	return &cloudlinkv1.Ack{Ok: true}, nil
}

// fusDestinos es la vía custodiada de PII: traduce el contact_id opaco a algo
// direccionable. Devuelve un número fijo y no toca ninguna KEK.
type fusDestinos struct{}

func (fusDestinos) Destino(_ context.Context, _, _ string) (contact.Ref, error) {
	return contact.Ref{Kind: contact.KindPhoneE164, Value: "59891000000"}, nil
}

// fusPuerta es el gate del puente CRM del tenant: abierto. El caso «cerrado» ya lo
// prueban los tests de `crmpush`; aquí lo que se quiere ver es el documento.
type fusPuerta struct{}

func (fusPuerta) Enabled(_ context.Context, _ string) (bool, error) { return true, nil }

// fusEncolado es UNA fila de `webhook_outbox`.
type fusEncolado struct {
	kind    string
	payload json.RawMessage
}

// fusCola es `webhook_outbox`: se queda con lo encolado para poder mirarlo.
type fusCola struct {
	mu        sync.Mutex
	encoladas []fusEncolado
}

func (c *fusCola) EnqueueWebhook(_ context.Context, _, kind string, payload json.RawMessage) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.encoladas = append(c.encoladas, fusEncolado{kind: kind, payload: payload})
	return int64(len(c.encoladas)), nil
}

// filas devuelve los documentos encolados con ese `kind`, en orden de encolado.
func (c *fusCola) filas(kind string) []json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]json.RawMessage, 0, len(c.encoladas))
	for _, f := range c.encoladas {
		if f.kind == kind {
			out = append(out, f.payload)
		}
	}
	return out
}

func (c *fusCola) cuantos(kind string) int { return len(c.filas(kind)) }

// ---------------------------------------------------------------------------
// ATREZO — LAS AYUDAS DE ASERCIÓN
// ---------------------------------------------------------------------------

// fusFila devuelve la fila del job o falla.
func fusFila(t *testing.T, almacen *StoreEnMemoria, id string) Fila {
	t.Helper()
	f, ok := almacen.Ver(id)
	if !ok {
		t.Fatalf("la fila %s no existe", id)
	}
	return f
}

// fusExigeLinea comprueba una línea del presupuesto por CONTENIDO. Contar líneas es
// exactamente lo que dejó pasar un `done` con cero ítems en la Ola 2.
func fusExigeLinea(t *testing.T, l stages.Linea, kind, sku, label string) {
	t.Helper()
	if l.Kind != kind || l.SKU != sku || l.Label != label {
		t.Fatalf("línea inesperada: kind=%q sku=%q label=%q; se esperaba kind=%q sku=%q label=%q",
			l.Kind, l.SKU, l.Label, kind, sku, label)
	}
}

// fusProcedencias describe de dónde salió cada match, para que un fallo diga QUÉ pasó.
func fusProcedencias(lineas []stages.Linea) string {
	var b strings.Builder
	for i, l := range lineas {
		if i > 0 {
			b.WriteString(", ")
		}
		if l.Match == nil {
			fmt.Fprintf(&b, "%s=<sin match>", l.Label)
			continue
		}
		fmt.Fprintf(&b, "%s=%s(%.2f)", l.Label, l.Match.Strategy, l.Match.Confidence)
	}
	return b.String()
}

// fusUltimaRevision devuelve la revisión de número más alto.
func fusUltimaRevision(t *testing.T, bandeja *intakes.MemoryStore, intakeID string) intakes.Revision {
	t.Helper()
	revs := bandeja.Revisions(intakeID)
	if len(revs) == 0 {
		t.Fatalf("la solicitud %s no tiene ninguna revisión", intakeID)
	}
	ultima := revs[0]
	for _, r := range revs[1:] {
		if r.RevisionNo > ultima.RevisionNo {
			ultima = r
		}
	}
	return ultima
}

// fusDatosFaltantes cuenta lo que el pipeline DECLARA no haber podido interpretar en la
// primera pasada. Sale de lo que cada etapa registra —no de una impresión sobre el
// resultado— porque cada uno de esos números tiene un dueño distinto:
//
//	P2  · `ideas_descartadas`   → el anclaje tiró una idea que el modelo se inventó;
//	P3  · `items_aislados`      → un ítem que no se pudo especificar;
//	P3  · `items_sobre_tope`    → un ítem que ni se intentó (más de MaxItemsPorPedido);
//	match · `Warnings`          → un ítem degradado (DEUDA-044.16).
//
// La línea de envío SIN precio no cuenta y no es una omisión: es la línea legítima de
// todo tenant sin zonas configuradas (D-041.11), y contarla convertiría el caso normal
// en un defecto.
func fusDatosFaltantes(t *testing.T, log *captor, m stages.ArtefactoMatch) int {
	t.Helper()
	total := len(m.Warnings)
	total += fusCampoEntero(t, log, "p2: ideas principales", "ideas_descartadas")
	total += fusCampoEntero(t, log, "p3: especificaciones", "items_aislados")
	total += fusCampoEntero(t, log, "p3: especificaciones", "items_sobre_tope")
	return total
}

// fusCampoEntero saca un contador de LA línea de log de una etapa. Falla si la línea no
// existe: un contador que no se emitió no es un cero, es una etapa que no corrió.
func fusCampoEntero(t *testing.T, log *captor, fragmento, campo string) int {
	t.Helper()
	l := log.unica(t, fragmento)
	v, ok := l.campos[campo]
	if !ok {
		t.Fatalf("la línea %q no trae el campo %q: %v", fragmento, campo, l.campos)
	}
	n, ok := v.(int)
	if !ok {
		t.Fatalf("el campo %q de %q no es un entero, es %T", campo, fragmento, v)
	}
	return n
}

// fusTiempos compone el desglose por etapa a partir de las líneas de `desenlace`, que es
// el punto ÚNICO por el que pasa toda etapa ejecutada.
func fusTiempos(t *testing.T, log *captor) string {
	t.Helper()
	lineas := log.buscar("etapa completada")
	if len(lineas) != 5 {
		t.Fatalf("tenían que completarse las CINCO etapas y el log trae %d: %s", len(lineas), log.volcado())
	}
	var b strings.Builder
	for i, l := range lineas {
		if i > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(&b, "%v=%v ms", l.campos["stage"], l.campos["elapsed_ms"])
	}
	return b.String()
}
