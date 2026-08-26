package stages_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-shared/llm"
	"github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/anclaje"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// draft_test.go — LOS CRITERIOS DE T3.4, uno por test y con literales escritos a
// mano. El caso principal NO fabrica un artefacto de match a mano: hace correr la
// etapa REAL de T3.2 sobre los ítems de P4 del caso Ambar, porque lo que el criterio
// pide es un e2e local y un artefacto escrito a mano probaría el que yo imaginé, no
// el que produce el pipeline.

// ---------------------------------------------------------------------------
// ATREZO
// ---------------------------------------------------------------------------

// elapsedDeAmbar son los 174 s que design §10 usa de ejemplo
// (`{"elapsed_ms": 174000, …}`). El reloj de la etapa se fija en `message_ts` + esto,
// que es lo que permite AFIRMAR el número en vez de comprobar un rango.
const elapsedDeAmbar = 174 * time.Second

// banco es el conjunto de almacenes en memoria contra los que corre la etapa, más el
// log capturado. Va en un struct porque los cuatro se miran en casi todos los tests y
// devolverlos sueltos convertiría cada construcción en una línea de cinco valores.
type banco struct {
	draft *stages.Draft
	// flujos guarda la CABECERA de la solicitud (UpsertIntake) y las filas de
	// flow_events: en producción las dos las sirve el mismo *store.PostgresRepository.
	flujos *store.MemoryRepository
	// solicitudes guarda las revisiones (*intakes.MemoryStore, el mismo doble que usa
	// el proyector del carrito).
	solicitudes *intakes.MemoryStore
	// jobs es la máquina de `intake_jobs`, que valida el artefacto con la MISMA puerta
	// que Postgres (intake.Artifact.Validate).
	jobs *storeFake
	log  *bytes.Buffer
}

// draftDe construye la etapa con el reloj FIJADO en `ahora`.
func draftDe(t *testing.T, ahora time.Time, opts ...stages.OpciónDraft) *banco {
	t.Helper()
	b := &banco{
		flujos:      store.NewMemoryRepository(),
		solicitudes: intakes.NewMemoryStore(),
		jobs:        &storeFake{},
		log:         &bytes.Buffer{},
	}
	opts = append([]stages.OpciónDraft{stages.ConReloj(func() time.Time { return ahora })}, opts...)
	d, err := stages.NewDraft(logger.New(logger.WithWriter(b.log)), b.jobs, b.flujos, b.solicitudes, b.flujos, opts...)
	require.NoError(t, err)
	b.draft = d
	return b
}

// matchDeAmbarSinDeco corre la etapa REAL de T3.2 sobre los ítems de P4 del caso
// Ambar, con el catálogo del fixture MENOS «Decoración infantil».
//
// 🔴 POR QUÉ SIN «DECORACIÓN INFANTIL», Y POR QUÉ ESO NO ES ELEGIR EL CASO CÓMODO.
// Es el escenario EXACTO que retrata design §7.4: cuatro líneas —torta de chocolate
// con sus dos variantes, torta de vainilla sin match, tequeños a $490 y el envío— y
// el añadido convertido en `customization` porque el catálogo no lo vende (D-041.24).
// Es también el que cuadra con la métrica de §10 (`"lines":4,"matched":2,
// "unmatched":1`), que es el otro literal del contrato que este test tiene que
// reproducir. Con el catálogo COMPLETO el añadido es una línea propia con precio y
// salen cinco: ése es el caso de TestDraft_LasLineasSeCopianTALCUAL, que existe justo
// para que no parezca que el borrador solo sabe hacer cuatro.
func matchDeAmbarSinDeco(t *testing.T) *stages.ArtefactoMatch {
	t.Helper()
	return matchDeAmbarCon(t, sinArticulo(catalogoAmbar(), "DECO-INF"))
}

// matchDeAmbarCon corre T3.2 con el catálogo dado.
func matchDeAmbarCon(t *testing.T, cat cart.Catalog) *stages.ArtefactoMatch {
	t.Helper()
	gz := &zonaGrisFalsa{respuestas: map[string]int{"torta de chocolate": 0}}
	m, _ := matchDe(t, stages.ConZonaGris(gz))
	art, err := m.Run(context.Background(), jobAmbarP4(), stages.EntradaMatch{
		Cantidades: p4De(itemsDeAmbar()...),
		Indice:     indiceDe(t, cat),
		Nota:       stages.SinNotaDePedido,
	})
	require.NoError(t, err)
	return art
}

// entradaDeAmbar es la entrada completa de la etapa para el caso: el artefacto del
// match, el literal del cliente, la fecha que calculó P4 y la vía por la que corrió.
func entradaDeAmbar(art *stages.ArtefactoMatch) stages.EntradaDraft {
	return stages.EntradaDraft{
		Match:        art,
		SourceText:   textoAmbar,
		FechaEntrega: "2026-07-22",
		Analisis:     stages.Analisis{Provider: "api", Model: "claude-x"},
	}
}

// payloadDeLaRevision decodifica el payload de la única revisión de la solicitud.
func payloadDeLaRevision(t *testing.T, b *banco, intakeID string) (intakes.Revision, stages.PayloadRevision) {
	t.Helper()
	revs := b.solicitudes.Revisions(intakeID)
	require.Len(t, revs, 1, "la solicitud tiene que tener EXACTAMENTE una revisión")
	var p stages.PayloadRevision
	require.NoError(t, json.Unmarshal(revs[0].Payload, &p))
	return revs[0], p
}

// laSolicitud devuelve la única cabecera escrita.
func laSolicitud(t *testing.T, b *banco) store.Intake {
	t.Helper()
	todas := b.flujos.Intakes()
	require.Len(t, todas, 1, "el borrador tiene que parir UNA solicitud, ni cero ni dos")
	return todas[0]
}

// elEvento devuelve la única fila de flow_events escrita.
func elEvento(t *testing.T, b *banco) store.FlowEvent {
	t.Helper()
	evs := b.flujos.FlowEvents()
	require.Len(t, evs, 1, "el borrador publica UNA métrica")
	return evs[0]
}

// ---------------------------------------------------------------------------
// EL CRITERIO PRINCIPAL — EL E2E DEL CASO AMBAR
// ---------------------------------------------------------------------------

// TestDraft_ElCasoAmbar_CuatroLineasYDosPreguntas es el criterio de T3.4 entero: del
// artefacto de P4 de Ambar nace UNA solicitud en `pending_approval` con la revisión 1
// `interpreted`, cuatro líneas y dos preguntas; el `contact_id` es opaco en las dos
// tablas y la métrica trae el `elapsed_ms` medido desde `message_ts`.
//
// 🔴 CADA ASERCIÓN MIRA EL CONTENIDO, NO EL RECUENTO. «Cuatro líneas» lo cumple
// también un payload con cuatro renglones vacíos, y en la Ola 2 ya pasó una vez que
// un `done` con `items=0` se diera por bueno.
func TestDraft_ElCasoAmbar_CuatroLineasYDosPreguntas(t *testing.T) {
	assertFixtureLejosDeHoy(t, messageTSDeAmbar)
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	art := matchDeAmbarSinDeco(t)

	out, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(art))
	require.NoError(t, err)

	// (1) LA CABECERA — una solicitud, esperando al dueño, colgada de su evento.
	sol := laSolicitud(t, b)
	require.Equal(t, out.IntakeID, sol.ID)
	require.Equal(t, intakes.StatusPendingApproval, sol.Status,
		"el borrador nace esperando al dueño; `open` es «carrito en curso» y aquí no hay carrito")
	require.Equal(t, evento, sol.EventID, "la solicitud declara al evento del que nació (D-043.21)")
	require.Equal(t, tenant, sol.TenantID)
	require.Equal(t, sesion, sol.SessionID)
	require.Equal(t, cliente, sol.ContactID, "el contacto viaja OPACO tal como llegó en la clave de ventana")
	require.Zero(t, sol.Total, "intakes.total es la suma de intake_items, y el borrador no escribe ninguna")
	require.Empty(t, sol.CustomerNote)

	// (2) LA REVISIÓN — la 1, interpretada, escrita por el sistema.
	rev, p := payloadDeLaRevision(t, b, sol.ID)
	require.Equal(t, 1, rev.RevisionNo)
	require.Equal(t, 1, out.RevisionNo)
	require.Equal(t, intakes.RevisionKindInterpreted, rev.Kind)
	require.Equal(t, intakes.RevisionBySystem, rev.CreatedBy,
		"created_by es un ROL, jamás una persona")
	require.Empty(t, rev.RenderedText, "la revisión interpretada no mandó ningún texto al cliente")

	// (3) EL PAYLOAD §7.4 — cabecera del contrato.
	require.Equal(t, intakes.RevisionPayloadVersion, p.Version)
	require.Equal(t, textoAmbar, p.SourceText, "el original va entero: el dueño lo compara con la interpretación (§7.6)")
	require.True(t, p.MessageTS.Equal(messageTSDeAmbar), "message_ts es el del PRIMER mensaje, no el de la creación")
	require.Equal(t, "2026-07-22", p.DeliveryDate)
	require.Equal(t, stages.Analisis{
		Provider: "api", Model: "claude-x", Source: stages.OrigenHiloDelEvento, ReanalyzedFrom: nil,
	}, p.Analysis, "el rastro de quién interpretó; en la revisión 1 el origen es el hilo y no viene de ningún re-análisis")
	require.Empty(t, p.MediaRefs)
	require.Empty(t, p.Warnings, "ningún ítem se degradó en este caso")

	// (4) LAS CUATRO LÍNEAS, una a una.
	require.Len(t, p.Lines, 4)

	choc := p.Lines[0]
	require.Equal(t, stages.KindMatched, choc.Kind)
	require.Equal(t, "TORTA-CHOC", choc.SKU)
	require.Equal(t, "Torta chocolate húmedo + crema choc.", choc.Label)
	require.Nil(t, choc.UnitPrice, "el rango cruza dos variantes: el precio lo pone el dueño")
	require.Equal(t, "sin lactosa, decoración infantil", choc.Customization,
		"el añadido que el catálogo no vende es indicación, no renglón (D-041.24)")
	require.Equal(t, []stages.OpcionVariante{
		{SKU: "TORTA-CHOC#10", Label: "Torta chocolate húmedo + crema choc. — 10 porciones", Price: 2100},
		{SKU: "TORTA-CHOC#12", Label: "Torta chocolate húmedo + crema choc. — 12 porciones", Price: 2400},
	}, choc.VariantOptions)
	require.Equal(t, evidenciaTortaChocolate, choc.Evidence)

	vainilla := p.Lines[1]
	require.Equal(t, stages.KindUnmatched, vainilla.Kind)
	require.Empty(t, vainilla.SKU)
	require.Nil(t, vainilla.UnitPrice)
	require.Equal(t, "torta de vainilla con lluvia de colores", vainilla.Label)

	teq := p.Lines[2]
	require.Equal(t, stages.KindMatched, teq.Kind)
	require.Equal(t, "TEQ-30", teq.SKU)
	require.NotNil(t, teq.UnitPrice)
	require.Equal(t, 490.0, *teq.UnitPrice)
	require.Equal(t, "package", teq.UnitKind)
	require.Equal(t, 30, teq.PackageSize)

	envio := p.Lines[3]
	require.Equal(t, stages.KindShipping, envio.Kind)
	require.Equal(t, intakes.ShippingSKU, envio.SKU)
	require.Nil(t, envio.UnitPrice)

	// (5) LAS DOS PREGUNTAS, con su texto exacto.
	require.Equal(t, []string{
		"¿Confirmas el tamaño de «Torta chocolate húmedo + crema choc.»: 10 o 12 porciones?",
		"¿Zona de entrega para calcular el envío?",
	}, p.SuggestedQuestions,
		"se pregunta lo que SOLO el cliente puede contestar: la variante y la zona. El precio de la vainilla es tarea del dueño")

	// (6) LA MÉTRICA — la forma literal de design §10.
	ev := elEvento(t, b)
	require.Equal(t, stages.EventoBorradorCreado, ev.Name)
	require.Equal(t, tenant, ev.TenantID)
	require.Equal(t, cliente, ev.ContactID, "el contact_id de la métrica es el OPACO de la clave de ventana")
	require.Equal(t, map[string]any{
		"elapsed_ms": int64(174000),
		"lines":      4,
		"matched":    2,
		"unmatched":  1,
	}, ev.Payload, "el envío no es matched ni unmatched: no sale del catálogo, lo pone la plataforma")

	// (7) Y LA ETAPA QUEDÓ MARCADA en el job, que es lo que impide repetirla al reanudar.
	require.Len(t, b.jobs.guardados, 1)
	require.Equal(t, intake.StageDraft, b.jobs.guardados[0].Stage)
	var releido stages.ArtefactoDraft
	require.NoError(t, json.Unmarshal(b.jobs.guardados[0].Payload, &releido))
	require.Equal(t, stages.ArtefactoDraft{
		Version: 1, IntakeID: sol.ID, RevisionNo: 1, Lines: 4, ElapsedMS: 174000,
	}, releido)
}

// ---------------------------------------------------------------------------
// `contact_id` OPACO Y CERO TEXTO LIBRE EN LA MÉTRICA
// ---------------------------------------------------------------------------

// TestDraft_LaMetricaNoLlevaNiUnaPalabraDelCliente barre el payload del evento YA
// SERIALIZADO buscando fragmentos del texto del cliente, de sus evidencias y de las
// etiquetas de los productos.
//
// Se barre el JSON entero y no los campos uno a uno a propósito: lo que hay que
// garantizar no es «este campo está limpio», es que NO HAY DÓNDE se haya colado. Un
// test que mirara claves conocidas se quedaría ciego el día que alguien añada una
// quinta.
func TestDraft_LaMetricaNoLlevaNiUnaPalabraDelCliente(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	art := matchDeAmbarSinDeco(t)

	_, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(art))
	require.NoError(t, err)

	ev := elEvento(t, b)
	crudo, err := json.Marshal(ev.Payload)
	require.NoError(t, err)

	for _, prohibido := range []string{
		"torta", "tequeños", "vainilla", "chocolate", "porciones", "lactosa",
		evidenciaTortaChocolate, evidenciaTequenos, textoAmbar,
		"TORTA-CHOC", "TEQ-30", "Envío",
	} {
		require.NotContainsf(t, strings.ToLower(string(crudo)), strings.ToLower(prohibido),
			"la métrica lleva %q: flow_events es una tabla EN CLARO y ahí no entra ni el literal ni el catálogo (ADR-0034)", prohibido)
	}

	// Y las claves son EXACTAMENTE las cuatro del contrato: ni una de más.
	claves := make([]string, 0, len(ev.Payload))
	for k := range ev.Payload {
		claves = append(claves, k)
	}
	require.ElementsMatch(t, []string{"elapsed_ms", "lines", "matched", "unmatched"}, claves)

	// El contacto es el opaco, y ni el evento ni la cabecera lo enriquecen.
	require.Equal(t, cliente, ev.ContactID)
	require.Equal(t, cliente, laSolicitud(t, b).ContactID)
	require.NotContains(t, ev.ContactID, "@", "un JID llevaría arroba; el opaco no")
	require.NotContains(t, ev.ContactID, "+", "un teléfono en E.164 llevaría un +")
}

// ---------------------------------------------------------------------------
// `elapsed_ms` — LOS DOS EXTREMOS Y SUS DOS RELOJES
// ---------------------------------------------------------------------------

// TestDraft_ElapsedMS_SaleDelMessageTS cubre las tres conductas del cronómetro con el
// reloj FALSO, que es la única forma de afirmar un número:
//
//   - el caso del plan: 174 s desde el mensaje del cliente ⇒ 174000;
//   - el job REANUDADO horas después: el número CRECE, porque mide la espera del
//     cliente y no el tiempo de proceso — si `elapsed_ms` saliera de un cronómetro
//     interno de la etapa, este subtest daría lo mismo que el anterior;
//   - los relojes desalineados: un resultado negativo NO se publica.
func TestDraft_ElapsedMS_SaleDelMessageTS(t *testing.T) {
	casos := []struct {
		nombre   string
		ahora    time.Time
		esperado int64
	}{
		{"el caso de design §10", messageTSDeAmbar.Add(elapsedDeAmbar), 174000},
		{"el job se reanuda tres horas después y la espera del cliente es MAYOR",
			messageTSDeAmbar.Add(3 * time.Hour), 3 * 60 * 60 * 1000},
		{"🔴 el reloj del Edge va por delante del cloud: nunca un negativo",
			messageTSDeAmbar.Add(-90 * time.Second), 0},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			b := draftDe(t, c.ahora)
			out, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(matchDeAmbarSinDeco(t)))
			require.NoError(t, err)
			require.Equal(t, c.esperado, out.ElapsedMS)
			require.Equal(t, c.esperado, elEvento(t, b).Payload["elapsed_ms"])
		})
	}
}

// TestDraft_SinMessageTS_ElNumeroNoMienteYSeAvisa: un job sin instante del cliente no
// tiene desde dónde medir. Se publica 0 —el único valor honesto— y el log dice que ese
// 0 no mide la espera, para que nadie lea un panel lleno de ceros como «va rapidísimo».
func TestDraft_SinMessageTS_ElNumeroNoMienteYSeAvisa(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	out, err := b.draft.Run(context.Background(), jobDeAmbar(), entradaDeAmbar(matchDeAmbarSinDeco(t)))
	require.NoError(t, err)
	require.Zero(t, out.ElapsedMS)
	require.Contains(t, b.log.String(), "no trae message_ts")
}

// TestDraft_ElRelojSoloEntraPorElConstructor es la regla escrita sobre el CÓDIGO, no
// sobre la conducta: `draft.go` puede leer el reloj —es la única etapa que
// legítimamente lo necesita, porque `elapsed_ms` es una duración hasta AHORA—, pero
// solo como valor por defecto de la dependencia inyectable.
//
// Es la hermana de TestStages_NoLeenElReloj (p4_test.go), que prohíbe `time.Now` en
// p2/p3/p4/fechas. Allí la prohibición es total porque la FECHA DE ENTREGA no puede
// depender de cuándo se recoja el job (D-044.9); aquí no se puede prohibir, así que se
// acota: un `time.Now()` suelto en medio de la lógica haría el `elapsed_ms`
// inafirmable en un test, que es exactamente como se dejan de vigilar los números.
//
// 💥 MUTACIÓN EJECUTADA: cambiar `s.ahora()` por `time.Now()` en transcurrido ⇒ rojo
// con el fichero y la línea (y además caen los tres subtests del cronómetro).
func TestDraft_ElRelojSoloEntraPorElConstructor(t *testing.T) {
	fset := token.NewFileSet()
	arbol, err := parser.ParseFile(fset, "draft.go", nil, 0)
	require.NoError(t, err)

	prohibidos := map[string]bool{"Now": true, "Since": true}
	var enConstructor bool
	ast.Inspect(arbol, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		esConstructor := fn.Name.Name == "NewDraft"
		ast.Inspect(fn, func(m ast.Node) bool {
			sel, ok := m.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "time" || !prohibidos[sel.Sel.Name] {
				return true
			}
			if !esConstructor {
				t.Errorf("%s: %s lee el RELOJ (time.%s) fuera de NewDraft. El reloj de esta etapa entra "+
					"por ConReloj; si se lee a mano, `elapsed_ms` deja de poder afirmarse en un test.",
					fset.Position(sel.Pos()), fn.Name.Name, sel.Sel.Name)
				return true
			}
			enConstructor = true
			return true
		})
		return true
	})
	require.True(t, enConstructor,
		"NewDraft ya no fija `time.Now` por defecto: o el reloj se inyecta siempre (y producción se quedó sin uno), o este test dejó de vigilar")
}

// ---------------------------------------------------------------------------
// EL ESTADO DE NACIMIENTO
// ---------------------------------------------------------------------------

// TestDraft_ElEstadoDeNacimientoEsUnaClaveViva: `pending_approval` no es un literal
// suelto. Tiene que ser una clave que la máquina de estados RECONOZCA y de la que se
// pueda SALIR, porque las cuatro salidas son las acciones que la bandeja le ofrece al
// dueño (T4.3/T4.4). Una solicitud que naciera en un estado terminal —o en uno que
// `IsStatus` no reconociera— sería un pedido inmortal e inaccesible, y no lo notaría
// ningún test de conducta del borrador.
func TestDraft_ElEstadoDeNacimientoEsUnaClaveViva(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	_, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(matchDeAmbarSinDeco(t)))
	require.NoError(t, err)

	nacimiento := laSolicitud(t, b).Status
	require.True(t, intakes.IsStatus(nacimiento), "el borrador nació en un estado que la máquina no conoce: %q", nacimiento)
	require.Equal(t, []string{"cancelled", "confirmed", "needs_info", "rejected"},
		intakes.AllowedTransitions(nacimiento),
		"las salidas de `pending_approval` son las cuatro acciones de la bandeja; si esta lista cambia, la bandeja cambia con ella")
}

// ---------------------------------------------------------------------------
// LAS LÍNEAS SE COPIAN TAL CUAL
// ---------------------------------------------------------------------------

// TestDraft_LasLineasSeCopianTALCUAL: con el catálogo COMPLETO el añadido facturable
// es una línea propia y salen CINCO. El borrador no decide cuántas líneas hay —eso lo
// decidió el match— y su trabajo es no perder ninguna ni cambiarles nada.
//
// La comparación es contra el artefacto de entrada, campo a campo, y no contra una
// lista escrita a mano: lo que se afirma es la IDENTIDAD entre lo que entró y lo que
// se persistió.
func TestDraft_LasLineasSeCopianTALCUAL(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	art := matchDeAmbarCon(t, catalogoAmbar())
	require.Len(t, art.Lines, 5, "con «Decoración infantil» en el catálogo el añadido es su propia línea")

	out, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(art))
	require.NoError(t, err)

	_, p := payloadDeLaRevision(t, b, out.IntakeID)
	require.Len(t, p.Lines, 5)
	for i, l := range p.Lines {
		require.Equalf(t, art.Lines[i], l.Linea, "la línea %d llegó cambiada al borrador", i)
	}
	require.Equal(t, 5, elEvento(t, b).Payload["lines"])
	require.Equal(t, 3, elEvento(t, b).Payload["matched"], "torta, decoración y tequeños")
	require.Equal(t, 1, elEvento(t, b).Payload["unmatched"])
}

// ---------------------------------------------------------------------------
// LOS ADJUNTOS
// ---------------------------------------------------------------------------

// TestDraft_LosAdjuntosNiSePierdenNiSeDuplican: el reparto de T3.3 entra por líneas y
// por cabecera, y la etapa lo copia sin inventar. El tercer adjunto va anclado a una
// línea que NO EXISTE —un reparto construido contra otro artefacto— y la conducta
// exigida es la del propio anclaje: sin certeza, a la cabecera. Perder un audio del
// cliente es peor que enseñarlo en el sitio genérico.
func TestDraft_LosAdjuntosNiSePierdenNiSeDuplican(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	art := matchDeAmbarSinDeco(t)

	in := entradaDeAmbar(art)
	in.Media = anclaje.Reparto{
		PorLinea: map[int][]anclaje.MediaRef{
			0:  {{Ref: "wapp/foto-torta.jpg", Kind: anclaje.KindImage}},
			99: {{Ref: "wapp/huerfana.jpg", Kind: anclaje.KindImage}},
		},
		Solicitud: []anclaje.MediaRef{{Ref: "wapp/audio1.ogg", Kind: anclaje.KindAudio, Label: anclaje.EtiquetaAudio}},
	}

	out, err := b.draft.Run(context.Background(), jobAmbarP4(), in)
	require.NoError(t, err)
	_, p := payloadDeLaRevision(t, b, out.IntakeID)

	require.Equal(t, []anclaje.MediaRef{{Ref: "wapp/foto-torta.jpg", Kind: anclaje.KindImage}}, p.Lines[0].MediaRefs)
	for i := 1; i < len(p.Lines); i++ {
		require.Emptyf(t, p.Lines[i].MediaRefs, "la línea %d no tenía adjuntos y le aparecieron", i)
	}
	require.Equal(t, []anclaje.MediaRef{
		{Ref: "wapp/audio1.ogg", Kind: anclaje.KindAudio, Label: anclaje.EtiquetaAudio},
		{Ref: "wapp/huerfana.jpg", Kind: anclaje.KindImage},
	}, p.MediaRefs, "la huérfana sube a la cabecera, detrás de las que ya estaban")
	require.Contains(t, b.log.String(), "una línea que no existe")

	// EL INVARIANTE CONTABLE: entraron tres refs, salen tres.
	total := len(p.MediaRefs)
	for _, l := range p.Lines {
		total += len(l.MediaRefs)
	}
	require.Equal(t, 3, total, "ni se pierde ni se duplica un adjunto")
}

// ---------------------------------------------------------------------------
// LOS AVISOS DEL MATCH LLEGAN A LA BANDEJA
// ---------------------------------------------------------------------------

// TestDraft_LosAvisosDelMatchViajanEnLaRevision. La decisión de degradar un ítem en
// vez de tirar el borrador (DEUDA-044.16) se sostiene sobre que «el dueño lo ve en la
// bandeja y lo arregla», y la bandeja lee el payload de la revisión. Si los avisos se
// quedaran en `intake_jobs.artifacts`, esa frase sería falsa.
func TestDraft_LosAvisosDelMatchViajanEnLaRevision(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	art := matchDeAmbarSinDeco(t)
	art.Warnings = []stages.Aviso{{ItemPos: 1, Reason: stages.MotivoRangoSinVariante}}

	out, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(art))
	require.NoError(t, err)

	_, p := payloadDeLaRevision(t, b, out.IntakeID)
	require.Equal(t, []stages.Aviso{{ItemPos: 1, Reason: stages.MotivoRangoSinVariante}}, p.Warnings)
	require.Len(t, p.Lines, 4, "un aviso no quita ni añade líneas")
}

// ---------------------------------------------------------------------------
// LA NOTA DEL PEDIDO ENTERO
// ---------------------------------------------------------------------------

// TestDraft_LaNotaDePedidoNoSePersisteYSeAVISA. Hoy nadie produce la nota, así que
// este camino no lo recorre ningún job real; el test existe porque el día que alguien
// la produzca, `UpsertIntake` no sabe escribir `intakes.customer_note` —solo la
// escribe el cierre del carrito— y el texto se perdería SIN ERROR.
//
// 🔴 Y el aviso NO cita la nota: es texto del cliente (ADR-0034). Eso también se
// afirma aquí, porque un log que resolviera el problema filtrando el literal sería
// peor que el problema.
func TestDraft_LaNotaDePedidoNoSePersisteYSeAVISA(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	art := matchDeAmbarSinDeco(t)
	art.CustomerNote = "dejarlo en portería, calle Mayor 14"

	_, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(art))
	require.NoError(t, err)

	require.Empty(t, laSolicitud(t, b).CustomerNote)
	require.Contains(t, b.log.String(), "NO la puede persistir")
	require.NotContains(t, b.log.String(), "portería", "el aviso NO cita la nota del cliente")
	require.NotContains(t, b.log.String(), "Mayor 14")
}

// ---------------------------------------------------------------------------
// LA MÉTRICA ES BEST-EFFORT
// ---------------------------------------------------------------------------

// eventosQueFallan es el outbox caído.
type eventosQueFallan struct{ err error }

func (e eventosQueFallan) InsertFlowEvent(context.Context, store.FlowEvent) error { return e.err }

// TestDraft_SiLaMetricaFallaElBorradorVIVE: perder una fila de telemetría no puede
// costar el pedido de un cliente. La etapa termina bien, la solicitud y su revisión
// están escritas, y el log dice qué se perdió.
func TestDraft_SiLaMetricaFallaElBorradorVIVE(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	roto, err := stages.NewDraft(logger.New(logger.WithWriter(b.log)), b.jobs, b.flujos, b.solicitudes,
		eventosQueFallan{err: errors.New("la base no está")},
		stages.ConReloj(func() time.Time { return messageTSDeAmbar.Add(elapsedDeAmbar) }))
	require.NoError(t, err)

	out, err := roto.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(matchDeAmbarSinDeco(t)))
	require.NoError(t, err, "la métrica es best-effort: su fallo NO tumba la etapa")
	require.NotEmpty(t, out.IntakeID)
	require.Len(t, b.solicitudes.Revisions(out.IntakeID), 1)
	require.Empty(t, b.flujos.FlowEvents())
	require.Contains(t, b.log.String(), "el borrador SÍ está creado")
}

// ---------------------------------------------------------------------------
// REINTENTOS Y CABLEADO
// ---------------------------------------------------------------------------

// TestDraft_DosPasadasDelMismoJob_NoParenDosSolicitudes. El id de la solicitud se
// DERIVA del evento, así que un reintento re-escribe la misma fila en vez de chocar
// contra `intakes_event_id_uidx` (que es lo que haría un id sorteado: violación de
// integridad, clase 23, que no cede reintentando).
//
// ⚠️ La revisión SÍ se duplica, y está escrito a propósito: `InsertRevision` no es
// idempotente por diseño —«dos actos iguales sobre el mismo pedido son dos hechos
// distintos y la auditoría debe verlos»— y el que evita que esto ocurra en producción
// es el artefacto `draft`, que hace que el worker se salte la etapa al reanudar.
func TestDraft_DosPasadasDelMismoJob_NoParenDosSolicitudes(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	art := matchDeAmbarSinDeco(t)

	primera, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(art))
	require.NoError(t, err)
	segunda, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(art))
	require.NoError(t, err)

	require.Equal(t, primera.IntakeID, segunda.IntakeID, "el mismo evento da la misma solicitud")
	require.Len(t, b.flujos.Intakes(), 1, "un evento tiene A LO SUMO un contenido durable (E-8)")
	require.Equal(t, 2, segunda.RevisionNo, "el store numera la revisión; la segunda pasada deja rastro, no pisa")
}

// TestDraft_NoSeConstruyeAMedias: cinco piezas, cinco formas de negarse a nacer. Un
// draft sin escritor de revisiones crearía solicitudes vacías y nadie lo notaría hasta
// que un dueño abriera la bandeja.
func TestDraft_NoSeConstruyeAMedias(t *testing.T) {
	log := logger.New(logger.WithWriter(&bytes.Buffer{}))
	jobs := &storeFake{}
	flujos := store.NewMemoryRepository()
	sols := intakes.NewMemoryStore()

	casos := []struct {
		nombre string
		falta  func() (*stages.Draft, error)
	}{
		{"sin log", func() (*stages.Draft, error) { return stages.NewDraft(nil, jobs, flujos, sols, flujos) }},
		{"sin store de jobs", func() (*stages.Draft, error) { return stages.NewDraft(log, nil, flujos, sols, flujos) }},
		{"sin escritor de solicitudes", func() (*stages.Draft, error) { return stages.NewDraft(log, jobs, nil, sols, flujos) }},
		{"sin escritor de revisiones", func() (*stages.Draft, error) { return stages.NewDraft(log, jobs, flujos, nil, flujos) }},
		{"sin escritor de eventos", func() (*stages.Draft, error) { return stages.NewDraft(log, jobs, flujos, sols, nil) }},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			d, err := c.falta()
			require.ErrorIs(t, err, stages.ErrDraftSinCablear)
			require.Nil(t, d)
		})
	}
}

// TestDraft_LasDosEntradasQueNoPuedenFaltar. Los dos mensajes describen el CASO REAL y
// no una familia: «no llegó el artefacto del match» y «el job no trae evento» piden
// cosas distintas a quien lee el log.
func TestDraft_LasDosEntradasQueNoPuedenFaltar(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))

	t.Run("sin artefacto del match", func(t *testing.T) {
		_, err := b.draft.Run(context.Background(), jobAmbarP4(), stages.EntradaDraft{})
		require.ErrorIs(t, err, stages.ErrSinMatch)
		require.Empty(t, b.flujos.Intakes(), "no se pare una solicitud sin líneas que ponerle")
	})

	t.Run("el job no declara evento", func(t *testing.T) {
		job := jobAmbarP4()
		job.Key.EventID = ""
		_, err := b.draft.Run(context.Background(), job, entradaDeAmbar(matchDeAmbarSinDeco(t)))
		require.ErrorIs(t, err, stages.ErrJobSinEvento)
		require.Contains(t, err.Error(), jobID, "el error dice de QUÉ job se trata")
		require.Empty(t, b.flujos.Intakes())
	})
}

// TestDraft_ElJobQueSeSalioDeProcessing: si el UPDATE del artefacto no toca ninguna
// fila es que el job ya no está en `processing` —lo soltó el watchdog, o lo terminó
// otro—, y la etapa lo dice con el error del paquete en vez de fingir que cerró.
func TestDraft_ElJobQueSeSalioDeProcessing(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	b.jobs.perdido = true

	_, err := b.draft.Run(context.Background(), jobAmbarP4(), entradaDeAmbar(matchDeAmbarSinDeco(t)))
	require.ErrorIs(t, err, stages.ErrJobFueraDeProcessing)
}

// TestDraft_SinViaDeAnalisis_SeAvisaYElBorradorSIGUE: el metadato de proceso importa
// (sin él no se puede comparar local contra api, D-044.15), pero un borrador sin
// metadato sigue siendo el pedido de un cliente. Se avisa y se sigue.
func TestDraft_SinViaDeAnalisis_SeAvisaYElBorradorSIGUE(t *testing.T) {
	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	in := entradaDeAmbar(matchDeAmbarSinDeco(t))
	in.Analisis = stages.Analisis{}

	out, err := b.draft.Run(context.Background(), jobAmbarP4(), in)
	require.NoError(t, err)
	_, p := payloadDeLaRevision(t, b, out.IntakeID)
	require.Equal(t, stages.OrigenHiloDelEvento, p.Analysis.Source,
		"en la revisión 1 el material solo puede venir del hilo del evento")
	require.Empty(t, p.Analysis.Provider)
	require.Contains(t, b.log.String(), "SIN vía de análisis")
}

// TestDraft_LasOtrasDosFormasDeLaPreguntaPorLaVariante cubre las dos ramas que el caso
// Ambar no toca, y que existen porque el match las produce de verdad:
//
//   - SIN RANGO: el artículo tiene variantes y el cliente no dijo el tamaño
//     (`variantesEnRango` devuelve nil ⇒ se le enseñan TODAS). No hay números que
//     citar, así que la pregunta cambia de forma en vez de escribir «0 o 0».
//   - CON RANGO Y SIN UNIDAD: el cliente dijo «10 o 12» y no dijo de qué. La unidad NO
//     se inventa: la pregunta queda «: 10 o 12?» y no «: 10 o 12 unidades?».
//
// El artefacto se escribe a mano AQUÍ —y no se saca de una corrida del match— porque
// lo que se está fijando es el contrato de la pregunta ante una línea con esa forma,
// no cómo se llega a ella.
func TestDraft_LasOtrasDosFormasDeLaPreguntaPorLaVariante(t *testing.T) {
	art := &stages.ArtefactoMatch{
		Version: 1,
		Lines: []stages.Linea{
			{
				Kind: stages.KindMatched, SKU: "TORTA-CHOC", Label: "Torta de chocolate", Qty: 1,
				VariantOptions: []stages.OpcionVariante{
					{SKU: "TORTA-CHOC#10", Label: "10 porciones", Price: 2100},
					{SKU: "TORTA-CHOC#12", Label: "12 porciones", Price: 2400},
				},
			},
			{
				Kind: stages.KindMatched, SKU: "EMPANADAS", Label: "Empanadas", Qty: 1,
				Range: &llm.Range{Min: 10, Max: 12},
				VariantOptions: []stages.OpcionVariante{
					{SKU: "EMPANADAS#10", Label: "10", Price: 900},
					{SKU: "EMPANADAS#12", Label: "12", Price: 1000},
				},
			},
			{Kind: stages.KindShipping, SKU: intakes.ShippingSKU, Label: "Envío — Providencia", Qty: 1, UnitPrice: &precioDelEnvio},
		},
	}

	b := draftDe(t, messageTSDeAmbar.Add(elapsedDeAmbar))
	out, err := b.draft.Run(context.Background(), jobAmbarP4(), stages.EntradaDraft{Match: art})
	require.NoError(t, err)

	_, p := payloadDeLaRevision(t, b, out.IntakeID)
	require.Equal(t, []string{
		"¿Cuál de las presentaciones de «Torta de chocolate» necesitas?",
		"¿Confirmas el tamaño de «Empanadas»: 10 o 12?",
	}, p.SuggestedQuestions,
		"y el envío YA precificado no se pregunta: la zona estaba resuelta")
}

// precioDelEnvio es la tarifa de una zona resuelta. Es variable y no literal porque
// `Linea.UnitPrice` es puntero: un envío con precio y uno «por confirmar» se
// distinguen por el nil, no por el número.
var precioDelEnvio = 3000.0
