package stages_test

// draft_metrica_reanalisis_test.go — EL EVENTO `intake_reanalyzed` (Plan 044 · T5.2,
// design §10).
//
// Es el quinto y último evento de §10 con productor, y el único de los cinco que NO
// mide un acto del dueño en su bandeja sino el DESENLACE de uno: el re-análisis que
// él pidió y que este pipeline acaba de consumar. Por eso se emite aquí y no en
// `internal/reanalisis`, que abre el job y no puede conocer el `to_rev`.
//
// Lo que se prueba:
//   · el payload es EXACTAMENTE el de design §10 —cuatro claves, ni una más—, con los
//     cuatro valores REALES del job y de la revisión recién numerada;
//   · un job del pipeline NORMAL no publica ni una fila de este evento;
//   · en el payload no entra ni una palabra del cliente.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake/stages"
)

// elEventoLlamado devuelve la ÚNICA fila de flow_events con ese `name`, y falla si
// hay más de una o ninguna.
//
// Existe porque desde T5.2 un job de re-análisis publica DOS filas y `elEvento` —que
// exige exactamente una— dejó de servir para esos casos. Los tests del pipeline
// normal siguen usando aquél a propósito: ahí «una fila y nada más» es parte de lo
// que hay que sostener.
func elEventoLlamado(t *testing.T, b *banco, name string) store.FlowEvent {
	t.Helper()
	var encontrados []store.FlowEvent
	for _, ev := range b.flujos.FlowEvents() {
		if ev.Name == name {
			encontrados = append(encontrados, ev)
		}
	}
	require.Lenf(t, encontrados, 1, "quiero UNA fila de %q; las escritas fueron %v",
		name, nombresDeLosEventos(b))
	return encontrados[0]
}

// nombresDeLosEventos es para el mensaje de error del helper de arriba: sin él, un
// fallo diría «quiero 1, hay 0» sin decir qué SÍ se escribió.
func nombresDeLosEventos(b *banco) []string {
	out := make([]string, 0, len(b.flujos.FlowEvents()))
	for _, ev := range b.flujos.FlowEvents() {
		out = append(out, ev.Name)
	}
	return out
}

// ---------------------------------------------------------------------------
// EL GOLDEN
// ---------------------------------------------------------------------------

// TestDraft_ElEventoDelReanalisis_ContratoDeDesign10 fija el payload ENTERO contra el
// literal de design §10 (`{"via":"api","from_rev":1,"to_rev":2,"source":"event_thread"}`).
//
// 🔴 SE COMPARA EL MAPA COMPLETO Y NO CLAVE A CLAVE: lo que hay que sostener no es
// «estas cuatro están», es que no hay una quinta. Un evento de telemetría que gana
// campos en silencio es el que acaba llevando algo que no debía.
//
// 🔴 Y LA CLAVE ES `via`, NUNCA `provider`. Se renombró el 2026-08-23 porque `"api"`
// no es un proveedor sino un transporte (ADR-0044), y se hizo ANTES de que existiera
// el productor porque era la única ventana barata: con filas escritas, cambiarla
// rompe consultas y paneles. Este test es lo que hace que el rename se note si alguien
// lo deshace.
//
// 🔴 EL FIXTURE LE DA HISTORIA A LA SOLICITUD, Y ESO ES PARTE DEL TEST. Sin ella
// `from_rev` y `to_rev` valdrían LOS DOS 1, y comparar el mapa entero dejaría de
// proteger la ASIGNACIÓN de cada valor a su clave: dos campos intercambiados pasarían
// el golden sin que saltara nada, porque los valores serían idénticos. Es la misma
// familia de defecto que ya mordió a este plan —un literal que acierta por casualidad
// mientras los dos lados coinciden—, así que aquí los dos números son 1 y 3.
func TestDraft_ElEventoDelReanalisis_ContratoDeDesign10(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	// Dos pasadas del pipeline normal sobre el mismo evento: revisiones 1 y 2. El
	// re-análisis lo pidió el dueño cuando la vigente era la 1 (`From: 1`, en
	// jobDeReanalisis), y su job escribe la 3.
	correrDraft(t, b, jobAmbarP4())
	correrDraft(t, b, jobAmbarP4())
	art := correrDraft(t, b, jobDeReanalisis())
	require.Equal(t, 3, art.RevisionNo)

	ev := elEventoLlamado(t, b, stages.EventoReanalizado)
	require.Equal(t, map[string]any{
		"via":      "local",
		"from_rev": 1,
		"to_rev":   art.RevisionNo,
		"source":   stages.OrigenHiloDelEvento,
	}, ev.Payload)
	require.NotEqual(t, ev.Payload["from_rev"], ev.Payload["to_rev"],
		"si los dos números coinciden, este golden NO puede cazar un intercambio de campos")

	// La fila va firmada con el flujo SINTÉTICO del pipeline y como telemetría, igual
	// que la del borrador: las dos salen del mismo productor y se leen juntas.
	require.Equal(t, stages.FlujoCaptacion, ev.FlowID)
	require.Equal(t, stages.VersionFlujoCaptacion, ev.FlowVersion)
	require.Equal(t, "event", ev.Kind)
	require.Equal(t, cliente, ev.ContactID)
}

// TestDraft_ElToRevEsElNumeroREALDeLaRevision: `to_rev` sale del store, que es el
// único que puede numerar sin carrera — no de un literal ni de `from_rev + 1`.
//
// Se comprueba sobre una solicitud que YA tiene una revisión, así que el número real
// es 2: con el pipeline en su primer job los dos caminos darían 1 y el test no vería
// la diferencia.
func TestDraft_ElToRevEsElNumeroREALDeLaRevision(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	// Dos pasadas del pipeline normal sobre el MISMO evento: revisiones 1 y 2.
	require.Equal(t, 1, correrDraft(t, b, jobAmbarP4()).RevisionNo)
	require.Equal(t, 2, correrDraft(t, b, jobAmbarP4()).RevisionNo)

	// El re-análisis lo pidió el dueño cuando la vigente era la 1 (`From: 1`), y para
	// cuando su job corre el store ya va por la 3. Los dos números TIENEN que ser
	// distintos y no consecutivos: con `from_rev + 1` —o con un literal— el test
	// pasaría igual si la solicitud no tuviera historia, y este plan ya ha parido dos
	// campos clavados a 1 por exactamente esa razón.
	tercera := correrDraft(t, b, jobDeReanalisis())
	require.Equal(t, 3, tercera.RevisionNo, "el store numera la siguiente, no el llamante")

	ev := elEventoLlamado(t, b, stages.EventoReanalizado)
	require.Equal(t, 3, ev.Payload["to_rev"], "to_rev es el número REAL que devolvió el store")
	require.Equal(t, 1, ev.Payload["from_rev"], "from_rev es la revisión VIGENTE cuando se pidió, no la que se escribe")
	require.NotEqual(t, ev.Payload["from_rev"], ev.Payload["to_rev"])
}

// ---------------------------------------------------------------------------
// LA MITAD QUE DE VERDAD PROTEGE: EL PIPELINE NORMAL
// ---------------------------------------------------------------------------

// TestDraft_ElPipelineNormalNoPublicaReanalisis: sin la marca del job no hay evento.
//
// Es la mitad que importa. Los cuatro campos son cero-valor fuera del re-análisis
// (intake/machine.go), así que emitir siempre llenaría la tabla de filas con `via` y
// `source` vacíos y el KPI —«cuántas veces se equivocó el LLM»— contaría como error
// cada pedido que salió bien a la primera.
func TestDraft_ElPipelineNormalNoPublicaReanalisis(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	correrDraft(t, b, jobAmbarP4())

	for _, ev := range b.flujos.FlowEvents() {
		require.NotEqualf(t, stages.EventoReanalizado, ev.Name,
			"el pipeline normal publicó %q: no re-analizó nada, es la primera pasada", ev.Name)
	}
	// Y la que SÍ tiene que estar sigue estando: el test no pasa por no publicar nada.
	require.Equal(t, stages.EventoBorradorCreado, elEvento(t, b).Name)
}

// ---------------------------------------------------------------------------
// CERO TEXTO LIBRE
// ---------------------------------------------------------------------------

// TestDraft_ElEventoDelReanalisisNoLlevaNiUnaPalabraDelCliente barre el payload YA
// SERIALIZADO, igual que su hermano de la métrica del borrador: lo que hay que
// garantizar no es «este campo está limpio», es que no hay DÓNDE se haya colado.
//
// 🔴 EL FIXTURE SÍ METE TEXTO DEL CLIENTE: `correrDraft` pasa `textoAmbar` entero como
// SourceText y el artefacto trae las evidencias y las etiquetas del catálogo. Sin eso
// el barrido sería un assert VACUO — buscar lo que nunca entró y salir verde.
func TestDraft_ElEventoDelReanalisisNoLlevaNiUnaPalabraDelCliente(t *testing.T) {
	t.Parallel()
	b := draftDe(t, ahoraDeAmbar())

	correrDraft(t, b, jobDeReanalisis())

	// (1) el control: el texto del cliente SÍ llegó a la etapa y SÍ quedó guardado en
	// la revisión. Sin esta comprobación, las ausencias de abajo no prueban nada.
	art := b.solicitudes.Revisions(laSolicitud(t, b).ID)
	require.NotEmpty(t, art, "el fixture no escribió revisión: el barrido no probaría nada")

	ev := elEventoLlamado(t, b, stages.EventoReanalizado)
	crudo, err := json.Marshal(ev.Payload)
	require.NoError(t, err)

	for _, prohibido := range []string{
		"torta", "tequeños", "vainilla", "chocolate", "porciones", "lactosa",
		evidenciaTortaChocolate, evidenciaTequenos, textoAmbar,
		"TORTA-CHOC", "TEQ-30", "Envío",
	} {
		require.NotContainsf(t, strings.ToLower(string(crudo)), strings.ToLower(prohibido),
			"el evento del re-análisis lleva %q: flow_events es una tabla EN CLARO y ahí no entra "+
				"ni el literal ni el catálogo (ADR-0034)", prohibido)
	}

	// Las claves son EXACTAMENTE las cuatro del contrato: ni una de más.
	claves := make([]string, 0, len(ev.Payload))
	for k := range ev.Payload {
		claves = append(claves, k)
	}
	require.ElementsMatch(t, []string{"via", "from_rev", "to_rev", "source"}, claves)

	// El contacto es el opaco, y el evento no lo enriquece.
	require.Equal(t, cliente, ev.ContactID)
	require.NotContains(t, ev.ContactID, "@", "un JID llevaría arroba; el opaco no")
	require.NotContains(t, ev.ContactID, "+", "un teléfono en E.164 llevaría un +")
	// Ni el rol del que lo pidió: `requested_by` es de la métrica del borrador, y
	// duplicarlo aquí sería un segundo sitio que mantener sincronizado.
	require.NotContains(t, ev.Payload, "requested_by")
	require.NotContains(t, string(crudo), intake.RequestedByOwner)
}
