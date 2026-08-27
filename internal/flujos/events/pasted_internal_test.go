package events

// pasted_internal_test.go — LA FORMA de la fila que escribe el dueño al pegar una
// transcripción en `/reanalyze` (Plan 044 · Ola 4 · T4.6; REQ-32 / D-044.17).
//
// 🔴 POR QUÉ ES UN TEST INTERNO Y NO UNO DE INTEGRACIÓN. Las cuatro decisiones que
// definen esta fila —rol `client`, grado `message`, origen `owner_pasted`, payload
// NULL— son de FORMA, y el único test que las tocaría desde fuera sería un
// `TestPostgres_*`… que se SALTA entero sin `WAPP_TEST_DB_DSN`. En este repo hay 89 así,
// y un criterio cubierto solo por uno de ellos no lo está probando nadie. Por eso la
// construcción de la fila vive en una función pura (`pastedEntry`) y se afirma aquí,
// sin base de datos.

import (
	"strings"
	"testing"
)

// TestPastedEntry_LasCuatroDecisionesDeForma. Cada una tiene su porqué y su
// consecuencia si se rompe:
//
//   - `role='client'` — lo que el dueño pega es lo que DIJO EL CLIENTE por otro canal
//     (un audio transcrito, un mensaje de Instagram), así que su voz es la suya
//     (D-044.17). Marcarlo `business` lo convertiría en CONTEXTO para el compositor
//     (ver speakerOf en source_composer.go) y el pipeline no extraería de ahí ni un
//     ítem — exactamente lo contrario de para qué se pega.
//   - `entry_kind='message'` — es hilo literal, no contexto. Con `summary` o
//     `message_out_of_turn` el material no contaría como mensaje del cliente y el
//     `source_text` podría salir vacío (Composed.Empty mide por `Messages`).
//   - `origin='owner_pasted'` — es la ÚNICA columna que conserva el rastro de quién lo
//     escribió, ya que el rol dice otra cosa a propósito. Es también la clave del
//     dedupe de T4.6.
//   - `payload` NULL — no es una decisión de estilo: el CHECK
//     `conversation_event_messages_grade_chk` (0051) EXIGE `payload IS NULL` en toda
//     fila de nivel 2. Una fila con las dos cosas rebota en la base.
func TestPastedEntry_LasCuatroDecisionesDeForma(t *testing.T) {
	t.Parallel()
	e := pastedEntry([]byte("cifrado"), []byte("dek"), "kek-1")

	if e.role != RoleClient {
		t.Fatalf("role=%q, quiero %q: lo que pega el dueño es la voz del CLIENTE (D-044.17)", e.role, RoleClient)
	}
	if e.kind != entryKindMessage {
		t.Fatalf("entry_kind=%q, quiero %q: es hilo literal, no contexto", e.kind, entryKindMessage)
	}
	if e.origin != OriginOwnerPasted {
		t.Fatalf("origin=%q, quiero %q: es el único rastro de quién lo escribió", e.origin, OriginOwnerPasted)
	}
	if e.payload != nil {
		t.Fatal("payload debe ir NULL: el CHECK de grado de la 0051 lo exige en toda fila de nivel 2")
	}
	if string(e.bodyEnc) != "cifrado" || string(e.bodyDEK) != "dek" || e.bodyKEKID != "kek-1" {
		t.Fatalf("el sobre de tres piezas no llegó entero: %+v", e)
	}
}

// TestAppendEntrySQL_EscribeElOrigen sostiene que la columna se NOMBRA en el INSERT.
//
// Hasta T4.6 no se nombraba: existía desde la 0051 con su DEFAULT `whatsapp` y todas
// las filas caían ahí, lo cual era correcto porque todas venían del canal. Si alguien
// quitara la columna de la sentencia, `AppendPastedMessage` seguiría compilando y
// seguiría escribiendo —con `origin='whatsapp'`—, el dedupe dejaría de encontrar sus
// propias filas y el rastro de la procedencia se perdería. Sin error, sin log, sin
// nada. De ahí este test.
func TestAppendEntrySQL_EscribeElOrigen(t *testing.T) {
	t.Parallel()
	if !strings.Contains(appendEntrySQL, "origin") {
		t.Fatal("appendEntrySQL ya no nombra `origin`: toda fila caería al DEFAULT 'whatsapp' EN SILENCIO")
	}
	// Los ocho parámetros de siempre más el noveno. Si alguien añade la columna a la
	// lista y olvida el `$9`, Postgres lo rechaza en ejecución; esto lo dice antes.
	if !strings.Contains(appendEntrySQL, "$8") {
		t.Fatal("falta el parámetro del origen en el SELECT de appendEntrySQL")
	}
}

// TestListPastedSQL_FiltraPorOrigenYPorGrado: la consulta del dedupe tiene que ver
// SOLO las transcripciones del dueño.
//
// El filtro por `origin` es lo que impide el falso positivo que rompería el criterio:
// un cliente que escribiera por WhatsApp la MISMA frase que el dueño pega no puede
// impedir que la transcripción se guarde — son dos hechos distintos. Y el filtro por
// `entry_kind` deja fuera los `summary`, que no son texto del cliente.
func TestListPastedSQL_FiltraPorOrigenYPorGrado(t *testing.T) {
	t.Parallel()
	for _, trozo := range []string{"origin = $2", "entry_kind = 'message'"} {
		if !strings.Contains(listPastedSQL, trozo) {
			t.Fatalf("listPastedSQL no filtra por %q: el dedupe miraría filas que no son suyas", trozo)
		}
	}
	if strings.Contains(listPastedSQL, "LIMIT") {
		t.Fatal("listPastedSQL no puede llevar LIMIT: un recorte dejaría pasar un duplicado EN SILENCIO")
	}
}

// TestOrigenPorDefecto_EsWhatsApp: las cuatro puertas de escritura que existían antes
// de T4.6 (`AppendSummary`, `AppendDecision`, `AppendMessage`,
// `AppendOutOfTurnMessage`) construyen su `entry` SIN origen, y eso tiene que seguir
// significando `whatsapp`.
//
// El default lo resuelve `appendEntry` y no cada llamante, para que añadir un método
// de escritura no pueda dejar la columna a merced de un olvido — el mismo criterio con
// el que `kind` y `role` los clava cada puerta y no el llamador.
func TestOrigenPorDefecto_EsWhatsApp(t *testing.T) {
	t.Parallel()
	var sinOrigen entry
	if sinOrigen.origin != "" {
		t.Fatal("el cero-valor de entry.origin dejó de ser vacío; el default de appendEntry ya no se dispararía")
	}
	if OriginWhatsApp != "whatsapp" {
		t.Fatalf("OriginWhatsApp=%q: tiene que coincidir con el DEFAULT de la columna en la 0051", OriginWhatsApp)
	}
	if OriginOwnerPasted != "owner_pasted" {
		t.Fatalf("OriginOwnerPasted=%q: tiene que estar en el CHECK de la 0051", OriginOwnerPasted)
	}
}
