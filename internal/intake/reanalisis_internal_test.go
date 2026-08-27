package intake

// reanalisis_internal_test.go — la SEGUNDA PUERTA por la que nace un job (Plan 044 ·
// Ola 4 · T4.6), probada sin Postgres.
//
// 🔴 POR QUÉ ESTE FICHERO EXISTE. Todo lo que decide esta pieza vive en dos sitios: en
// unas guardas de Go y en el texto de dos sentencias. Lo primero se prueba llamando;
// lo segundo, mirando la sentencia — y en los dos casos el único test alternativo
// sería un `TestIntegration_*`, que se SALTA sin `WAPP_TEST_DB_DSN` (89 así en este repo).
// Un job que naciera `aggregating` en vez de `pending` rompería el re-análisis entero
// y ningún test lo vería.

import (
	"strings"
	"testing"
)

// TestAbrirReanalisisSQL_ElJobNaceEnPending es la decisión de diseño de todo el
// fichero, escrita como test.
//
// Las DOS razones, para que quien lea el rojo sepa qué rompió:
//
//  1. `PutSourceText` solo escribe sobre una ventana `pending` —y sobre la ÚLTIMA
//     tocada de esa tupla—. Un job nacido `aggregating` no sería elegible, así que
//     `ComposeAtFlush` escribiría el sobre en cualquier OTRA fila o en ninguna, y este
//     job llegaría al worker sin literal.
//  2. `aggregating` es el único estado dentro del índice único parcial
//     `intake_jobs_ventana_viva_uidx`. Un re-análisis pedido mientras el cliente
//     escribe chocaría con un 23505 en vez del `422 reanalysis_in_progress` del
//     contrato.
func TestAbrirReanalisisSQL_ElJobNaceEnPending(t *testing.T) {
	t.Parallel()
	if !strings.Contains(abrirReanalisisSQL, "'pending'") {
		t.Fatal("el job del re-análisis ya no nace 'pending': el sobre no le llegaría y chocaría con la ventana viva")
	}
	if strings.Contains(abrirReanalisisSQL, "'aggregating'") {
		t.Fatal("el job del re-análisis NO puede nacer 'aggregating': entraría en el índice único parcial")
	}
}

// TestAbrirReanalisisSQL_HeredaElMessageTSDelPrimerJob.
//
// 🔴 `message_ts` ES LA BASE DE FECHAS DE P4 (D-044.9): «el miércoles que viene» se
// resuelve contra ella. Poner el reloj de HOY haría que un re-análisis pedido tres
// días después de la conversación resolviera «mañana» a otro día que la revisión 1 —
// el MISMO texto daría dos fechas distintas, y la culpa no se vería en ninguna parte.
//
// El `COALESCE` a `now()` cubre la solicitud que no nació de un job (la del carrito
// numérico del Plan 016/041, que no tiene fila en esta tabla).
func TestAbrirReanalisisSQL_HeredaElMessageTSDelPrimerJob(t *testing.T) {
	t.Parallel()
	for _, trozo := range []string{"SELECT j0.message_ts", "ORDER BY j0.created_at", "COALESCE(", "now()"} {
		if !strings.Contains(abrirReanalisisSQL, trozo) {
			t.Fatalf("abrirReanalisisSQL ya no hereda el message_ts original (falta %q): P4 resolvería las fechas contra HOY", trozo)
		}
	}
}

// TestAbrirReanalisisSQL_ElJobNaceSinSobre sostiene el criterio de T4.6 «el prompt de
// P2 se armó con el literal del EVENTO y no con `intake_jobs.source_text`».
//
// La forma de afirmarlo aquí es que este INSERT no escribe NINGUNA de las tres
// columnas del sobre: el job nace con `source_text_enc/_dek/_kek_id` a NULL y lo
// rellena `ComposeAtFlush`, que lee el hilo cifrado del evento.
//
// 🔴 Y COPIAR EL SOBRE DEL JOB ANTERIOR NO ES SOLO EL ATAJO PROHIBIDO: ES IMPOSIBLE.
// INV-13 vacía las tres columnas en la MISMA sentencia de los DOS terminales —`Finish`
// y `Fail`, ver machine.go—, así que el job viejo de ese evento tiene el sobre a NULL
// SIEMPRE (medido en UAT: 34 jobs terminales, cero con `source_text_enc`). Un
// re-análisis que quisiera reutilizar «la foto que ya está compuesta» heredaría la
// nada. Volver al hilo no es una preferencia del plan: es lo único que funciona.
func TestAbrirReanalisisSQL_ElJobNaceSinSobre(t *testing.T) {
	t.Parallel()
	for _, columna := range []string{"source_text_enc", "source_text_dek", "source_text_kek_id"} {
		if strings.Contains(abrirReanalisisSQL, columna) {
			t.Fatalf("abrirReanalisisSQL escribe %q: el job heredaría el literal viejo en vez de "+
				"reconstruirlo desde el hilo del evento (criterio de T4.6)", columna)
		}
	}
}

// TestJobNoTerminalSQL_PreguntaPorEventoYNoPorSolicitud.
//
// 🔴 EL FILTRO ES `event_id` Y NO PUEDE SER `intake_id`. El job del pipeline NORMAL
// —el que abre el agregador mientras el cliente escribe— todavía no tiene `intake_id`:
// esa columna la escribe `Finish`, al final. Filtrando por ella ese job sería
// INVISIBLE y el re-análisis abriría un segundo job sobre el mismo evento; los dos
// correrían el pipeline y los dos escribirían una revisión.
func TestJobNoTerminalSQL_PreguntaPorEventoYNoPorSolicitud(t *testing.T) {
	t.Parallel()
	if !strings.Contains(jobNoTerminalSQL, "event_id = $2") {
		t.Fatal("la guarda de concurrencia dejó de filtrar por evento")
	}
	if strings.Contains(jobNoTerminalSQL, "intake_id") {
		t.Fatal("filtrar por intake_id haría INVISIBLE el job del pipeline normal, que aún no lo tiene escrito")
	}
	if !strings.Contains(jobNoTerminalSQL, "tenant_id = $1") {
		t.Fatal("la pregunta tiene que ir acotada al tenant (INV-8)")
	}
}

// TestEstadosNoTerminales_SonExactamenteElComplementoDeIsTerminal.
//
// La lista de «lo que todavía puede producir una revisión» y la función `IsTerminal`
// dicen lo mismo desde dos sitios. Si divergieran —por ejemplo al añadir un sexto
// estado— la guarda del `422 reanalysis_in_progress` dejaría de ver jobs vivos y
// nacerían dos revisiones para el mismo evento. Este test las ata.
func TestEstadosNoTerminales_SonExactamenteElComplementoDeIsTerminal(t *testing.T) {
	t.Parallel()
	todos := []string{StatusAggregating, StatusPending, StatusProcessing, StatusDone, StatusFailed}

	for _, s := range estadosNoTerminales {
		if IsTerminal(s) {
			t.Fatalf("%q está en estadosNoTerminales y IsTerminal dice que SÍ lo es", s)
		}
	}
	for _, s := range todos {
		enLista := false
		for _, n := range estadosNoTerminales {
			if n == s {
				enLista = true
			}
		}
		if enLista == IsTerminal(s) {
			t.Fatalf("%q: enLista=%t, IsTerminal=%t — las dos listas divergieron", s, enLista, IsTerminal(s))
		}
	}
}

// TestSolicitudReanalisis_Valid: las tres condiciones para poder escribir la fila.
//
// La tercera —que el contexto sea del DUEÑO— no es defensiva por gusto: esta puerta
// existe SOLO para el re-análisis, y un `requested_by` vacío produciría un job que el
// `draft` trataría como del pipeline normal (revisión firmada `system`, sin rastro y
// sin empuje al CRM) pero que nadie habría agregado. Es un estado que no significa
// nada, así que no se escribe.
func TestSolicitudReanalisis_Valid(t *testing.T) {
	t.Parallel()
	completa := func() SolicitudReanalisis {
		return SolicitudReanalisis{
			Key:      WindowKey{TenantID: "t", SessionID: "s", ContactID: "c", EventID: "e"},
			IntakeID: "i",
			Contexto: Reanalisis{RequestedBy: RequestedByOwner},
		}
	}
	if !completa().Valid() {
		t.Fatal("la solicitud completa tiene que ser válida")
	}

	casos := map[string]func(*SolicitudReanalisis){
		"sin evento en la clave": func(s *SolicitudReanalisis) { s.Key.EventID = "" },
		"sin tenant en la clave": func(s *SolicitudReanalisis) { s.Key.TenantID = "" },
		"sin solicitud":          func(s *SolicitudReanalisis) { s.IntakeID = "" },
		"sin marca del dueño":    func(s *SolicitudReanalisis) { s.Contexto.RequestedBy = "" },
		"con otra marca":         func(s *SolicitudReanalisis) { s.Contexto.RequestedBy = "system" },
	}
	for nombre, romper := range casos {
		t.Run(nombre, func(t *testing.T) {
			t.Parallel()
			s := completa()
			romper(&s)
			if s.Valid() {
				t.Fatalf("%s: la solicitud NO debería ser válida", nombre)
			}
		})
	}
}

// TestNullableInt_CeroEsNULL: «no había revisión anterior» y «la anterior era la
// número cero» no son lo mismo, y la segunda no existe —los correlativos empiezan en
// 1—. El contrato §7.4 publica ese caso como `null`, así que la columna guarda NULL y
// no un 0 que después habría que traducir.
func TestNullableInt_CeroEsNULL(t *testing.T) {
	t.Parallel()
	if nullableInt(0) != nil {
		t.Fatal("0 tiene que ir NULL: es «no había revisión anterior»")
	}
	if nullableInt(-3) != nil {
		t.Fatal("un negativo tiene que ir NULL: no es un correlativo")
	}
	if nullableInt(2) != 2 {
		t.Fatalf("nullableInt(2)=%v, quiero 2", nullableInt(2))
	}
}

// TestReanalisis_EsDelDueño es LA pregunta que gatea las tres diferencias de conducta
// de T4.6 (`created_by='owner'`, el `payload.analysis` y el empuje al CRM). Se hace en
// un solo sitio para que ninguna de las tres pueda quedarse con un criterio distinto.
func TestReanalisis_EsDelDueño(t *testing.T) {
	t.Parallel()
	if !(Reanalisis{RequestedBy: RequestedByOwner}).EsDelDueño() {
		t.Fatal("la marca del dueño no se reconoce")
	}
	for _, otro := range []string{"", "system", "crm", "Owner", " owner"} {
		if (Reanalisis{RequestedBy: otro}).EsDelDueño() {
			t.Fatalf("%q no es la marca del dueño y se aceptó", otro)
		}
	}
}

// TestClaimSQL_DevuelveElContextoDelReanalisis: las cuatro columnas de la 0080 viajan
// en LOS DOS claims.
//
// 🔴 SON DOS SENTENCIAS Y UN SOLO ESCANEO (`escanearClaim`). Si una añadiera la
// columna y la otra no, el `Scan` fallaría en ejecución por el camino que se olvidó —
// que es el del despertar por evento (T2.7), el menos transitado y el que más tarda en
// destaparse. Este test mira las dos.
func TestClaimSQL_DevuelveElContextoDelReanalisis(t *testing.T) {
	t.Parallel()
	columnas := []string{"requested_by", "reanalysis_via", "reanalysis_source", "reanalyzed_from"}
	for nombre, sql := range map[string]string{
		"claimNextSQL":             claimNextSQL,
		"claimIgnorandoBackoffSQL": claimIgnorandoBackoffSQL,
	} {
		for _, col := range columnas {
			if !strings.Contains(sql, col) {
				t.Fatalf("%s no devuelve %q: el draft no sabría que el job es un re-análisis", nombre, col)
			}
		}
	}
}
