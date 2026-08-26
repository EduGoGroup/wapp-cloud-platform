package intake_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
)

// machine_integration_test.go — Plan 044 · Ola 2 · T2.1, el criterio ENTERO contra
// Postgres REAL: doble-claim, reanudación, artefacto inválido e INV-13.
//
// # POR QUÉ TODO ESTO ES DE INTEGRACIÓN Y NO HAY DOBLE EN MEMORIA
//
// Lo que T2.1 construye son CINCO SENTENCIAS SQL con guard. Lo que hay que probar
// es que el guard MUERDE, y el guard no existe en Go: existe en el `WHERE
// status = …`, en el `FOR UPDATE SKIP LOCKED`, en el `array_position` y en el
// `SET source_text_enc = NULL, …` de la misma sentencia. Un doble en memoria
// reescribiría esas cuatro cosas a mano y la suite pasaría a probar el doble — que
// es exactamente el aviso que MemoryStore lleva escrito en su propia cabecera.
//
// Se corre como el resto de la casa: por el NOMBRE del fichero, con
// `WAPP_TEST_DB_DSN` (sin ella se salta solo). `make test-integration` lo levanta.
//
// Los helpers `openTestDB` y `claveDeVentana` viven en postgres_integration_test.go,
// mismo paquete de test.
//
// # POR QUÉ HAY TANTOS HELPERS `exige…` Y NO TRES TESTS LARGOS
//
// Los dos tests de abajo pasaban de 15 de complejidad ciclomática (gocyclo), y
// envolver en `t.Run` NO baja ese número: gocyclo imputa los closures anidados a la
// función madre. Así que las fases se extrajeron DE VERDAD, a funciones con nombre.
// La regla al hacerlo fue: **ninguna aserción se pierde ni se degrada a `t.Logf`**
// (Go DESCARTA `t.Logf`: el test seguiría verde con el invariante roto). Cada
// `t.Fatalf` que había sigue siendo un `t.Fatalf`, con su texto; los dos únicos
// mensajes que se fusionaron —el del sobre escrito y el del sobre vaciado— llevan
// dentro el texto de LOS DOS sitios de los que vienen, no el de uno.

// ---------------------------------------------------------------------------
// HELPERS: leer la fila DE VERDAD y fabricar el estado anterior
// ---------------------------------------------------------------------------

// filaMaquina es la fila vista con SQL DIRECTO, con las columnas que la máquina de
// estados toca. Es un lector propio y NO una ampliación del `filaJob` de T1.1: aquel
// custodia el camino del entrante y se lee por tupla; este se lee por id y mira
// `stage`, `artifacts`, `error` e `intake_id`, que allí no existen.
//
// 🔴 Las tres columnas del sobre se leen como `IS NULL` y no como bytes: lo que se
// afirma es LA NULIDAD DE LAS TRES, y traerse el contenido para luego mirar si está
// vacío confunde «NULL» con «BYTEA de longitud 0», que en Postgres son cosas
// distintas y solo una satisface el barrido de INV-13.
type filaMaquina struct {
	id         string
	status     string
	stage      sql.NullString
	artifacts  []byte
	sourceRefs []byte
	errorTexto sql.NullString
	intakeID   sql.NullString
	encEsNull  bool
	dekEsNull  bool
	kekEsNull  bool
}

// leerFila trae la fila por id. Falla el test si no existe: en esta máquina nada
// borra filas.
//
// El `ctx` va PRIMERO y antes del `*testing.T` porque es lo que exigen a la vez
// `contextcheck` (la consulta tiene que colgar del contexto del llamante, no de un
// `context.Background()` propio) y `revive/context-as-argument`.
func leerFila(ctx context.Context, t *testing.T, db *sql.DB, id string) filaMaquina {
	t.Helper()
	var f filaMaquina
	err := db.QueryRowContext(ctx, `
		SELECT id::text, status, stage, artifacts, source_refs, error, intake_id::text,
		       source_text_enc IS NULL, source_text_dek IS NULL, source_text_kek_id IS NULL
		  FROM public.intake_jobs
		 WHERE id = $1::uuid
	`, id).Scan(&f.id, &f.status, &f.stage, &f.artifacts, &f.sourceRefs, &f.errorTexto,
		&f.intakeID, &f.encEsNull, &f.dekEsNull, &f.kekEsNull)
	if err != nil {
		t.Fatalf("leer la fila %s: %v — la máquina de estados NO BORRA FILAS; que no exista es un fallo, "+
			"no un estado", id, err)
	}
	return f
}

// sobreDePrueba devuelve un sobre COMPLETO y reconocible. No es criptografía de
// verdad y no hace falta que lo sea: lo que se prueba es que las tres columnas se
// escriben y se vacían juntas, no que el contenido descifre.
func sobreDePrueba() intake.SourceText {
	return intake.SourceText{
		Enc:   []byte("sobre-cifrado-del-literal-del-cliente"),
		DEK:   bytes.Repeat([]byte{0x7f}, 32),
		KEKID: "kek-de-test-1",
	}
}

// exigeSobreEscrito es LA MITAD DEL CRITERIO DE INV-13 que un test descuidado se
// salta: comprobar que EL ESTADO ANTERIOR EXISTE.
//
// 🔴 LAS TRES COLUMNAS DEL SOBRE NACEN NULLABLES (0072). Si el test comprobara el
// NULL final sobre un job que nunca tuvo sobre, estaría midiendo el DEFAULT y no el
// borrado de la transición: saldría verde con `Finish` sin una sola línea de vaciado.
// Por eso el sobre se ESCRIBE y se COMPRUEBA que quedó escrito antes de terminar el
// job — y se vuelve a comprobar con el job ya en `processing`, justo antes del
// terminal.
//
// `momento` dice DÓNDE se estaba mirando, porque esta comprobación se hace en varios
// puntos del camino y el mensaje tiene que decir en cuál falló.
func exigeSobreEscrito(t *testing.T, f filaMaquina, momento string) {
	t.Helper()
	if f.encEsNull || f.dekEsNull || f.kekEsNull {
		t.Fatalf("%s: el sobre NO está escrito (enc NULL=%t, dek NULL=%t, kek_id NULL=%t). SIN ESTE ESTADO "+
			"PREVIO fabricado y verificado, comprobar el NULL final NO PRUEBA NADA: las tres columnas nacen "+
			"NULLables y el verde sería el DEFAULT de la 0072, no el vaciado de la transición",
			momento, f.encEsNull, f.dekEsNull, f.kekEsNull)
	}
}

// exigeSobreVaciado es INV-13 dicho como aserción: en un terminal, LAS TRES columnas
// del sobre quedan NULL.
//
// Lleva dentro las razones de los DOS sitios de los que viene —`done` y `failed`—
// porque son la misma regla: lo que dispara el vaciado es TERMINAR, no terminar bien.
func exigeSobreVaciado(t *testing.T, f filaMaquina, terminal string) {
	t.Helper()
	if !f.encEsNull || !f.dekEsNull || !f.kekEsNull {
		t.Fatalf("🔴 INV-13 ROTA en `%s`: enc NULL=%t, dek NULL=%t, kek_id NULL=%t. LAS TRES se vacían en la "+
			"MISMA sentencia del guard, y lo que dispara el vaciado es TERMINAR, no terminar bien; media fila "+
			"borrada ni descifra ni está limpia, y un `source_text_kek_id` superviviente mantiene la fila en "+
			"el censo del Rekey re-envolviendo una DEK que ya no protege nada",
			terminal, f.encEsNull, f.dekEsNull, f.kekEsNull)
	}
}

// jobPendienteConSobre FABRICA EL ESTADO ANTERIOR (ver `exigeSobreEscrito`).
//
// Devuelve el id del job, ya en `pending` y con las tres columnas escritas.
func jobPendienteConSobre(ctx context.Context, t *testing.T, db *sql.DB, jobs *intake.Postgres,
	k intake.WindowKey, refs []string) string {
	t.Helper()

	if err := jobs.OpenOrAppend(ctx, intake.Append{
		Key: k, MessageTS: time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC), Refs: refs,
	}); err != nil {
		t.Fatalf("abrir la ventana: %v", err)
	}
	cerrada, err := jobs.CloseWindow(ctx, k)
	if err != nil || !cerrada {
		t.Fatalf("CloseWindow = (%t, %v); ESPERADO (true, nil)", cerrada, err)
	}
	escrito, err := jobs.PutSourceText(ctx, k, sobreDePrueba())
	if err != nil || !escrito {
		t.Fatalf("PutSourceText = (%t, %v); ESPERADO (true, nil)", escrito, err)
	}

	var id string
	if qerr := db.QueryRowContext(ctx, `
		SELECT id::text FROM public.intake_jobs
		 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND event_id = $4::uuid
		   AND status = 'pending'
		 ORDER BY updated_at DESC LIMIT 1
	`, k.TenantID, k.SessionID, k.ContactID, k.EventID).Scan(&id); qerr != nil {
		t.Fatalf("localizar el job recién cerrado: %v", qerr)
	}

	exigeSobreEscrito(t, leerFila(ctx, t, db, id), "el job recién cerrado, antes de reclamarlo")
	return id
}

// reclamarElMio toma un job y exige que sea el esperado. La cola es global —
// ClaimNext no filtra por tenant — así que si otro test dejó un `pending` vivo, esto
// lo dice en vez de asertar sobre la fila equivocada.
func reclamarElMio(ctx context.Context, t *testing.T, jobs *intake.Postgres, quiero string) intake.ClaimedJob {
	t.Helper()
	j, ok, err := jobs.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("ClaimNext: %v", err)
	}
	if !ok {
		t.Fatalf("ClaimNext no encontró nada; ESPERADO el job %s en `pending`", quiero)
	}
	if j.ID != quiero {
		t.Fatalf("ClaimNext se llevó %s; ESPERADO %s — la cola es GLOBAL: otro test dejó un job "+
			"`pending` sin limpiar", j.ID, quiero)
	}
	return j
}

// artefacto arma un artefacto válido y reconocible por etapa.
func artefacto(stage, marca string) intake.Artifact {
	return intake.Artifact{Stage: stage, Payload: []byte(`{"version":1,"marca":"` + marca + `"}`)}
}

// guardaEtapa exige que SaveStage APLIQUE, y dice por qué tenía que aplicar. `porQue`
// no es decoración: es lo que distingue «avanzar de etapa» de «repetir la etapa
// ACTUAL tras una reanudación», que son dos conductas distintas del mismo guard.
func guardaEtapa(ctx context.Context, t *testing.T, jobs *intake.Postgres, id string,
	art intake.Artifact, porQue string) {
	t.Helper()
	ok, err := jobs.SaveStage(ctx, id, art)
	if err != nil || !ok {
		t.Fatalf("SaveStage(%s) = (%t, %v); ESPERADO (true, nil) — %s", art.Stage, ok, err, porQue)
	}
}

// claves devuelve las claves de `artifacts` presentes en la fila.
func claves(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("artifacts = %s; no decodifica como objeto JSON: %v", raw, err)
	}
	return m
}

// ---------------------------------------------------------------------------
// (1) DOBLE-CLAIM DEL MISMO JOB PIERDE UNO
// ---------------------------------------------------------------------------

// TestIntegration_DobleClaimDelMismoJobPierdeUno es el guard del claim.
//
// SALIDAS ESPERADAS:
//   - primer  ClaimNext ... (job, true, nil), y la fila queda 'processing'
//   - segundo ClaimNext ... NO devuelve el MISMO job (la cola ya no lo tiene en 'pending')
//   - el claim trae el sobre y `stage` vacío: es un job que nunca corrió
//
// 🔴 DÓNDE ESTÁ EL GUARD: en el `WHERE status = 'pending'` de la SUBCONSULTA de
// claimNextSQL, no en el UPDATE de fuera (ver el comentario de la sentencia: fuera
// sería una guarda sobre un camino muerto, porque SKIP LOCKED ya bloqueó la fila).
//
// MUTACIÓN QUE LO PONE ROJO (ejecutada): quitar `WHERE status = 'pending'` de la
// subconsulta ⇒ el segundo claim vuelve a llevarse el mismo job.
func TestIntegration_DobleClaimDelMismoJobPierdeUno(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	id := jobPendienteConSobre(ctx, t, db, jobs, k, []string{"wamid.uno", "wamid.dos"})

	primero := reclamarElMio(ctx, t, jobs, id)
	if primero.Stage != "" {
		t.Fatalf("stage del primer claim = %q; ESPERADO \"\" — un job recién cerrado no ha corrido "+
			"ninguna etapa", primero.Stage)
	}
	if !primero.SourceText.Complete() {
		t.Fatalf("el claim NO trae el sobre (enc=%d dek=%d kek=%q); el worker no tendría con qué "+
			"trabajar y haría falta una segunda consulta", len(primero.SourceText.Enc),
			len(primero.SourceText.DEK), primero.SourceText.KEKID)
	}
	if len(primero.SourceRefs) != 2 {
		t.Fatalf("source_refs del claim = %v; ESPERADO 2 referencias", primero.SourceRefs)
	}
	if len(primero.Artifacts) != 0 {
		t.Fatalf("artifacts del primer claim = %v; ESPERADO vacío", primero.Artifacts)
	}
	if f := leerFila(ctx, t, db, id); f.status != intake.StatusProcessing {
		t.Fatalf("status tras el claim = %q; ESPERADO %q", f.status, intake.StatusProcessing)
	}

	segundo, ok, err := jobs.ClaimNext(ctx)
	if err != nil {
		t.Fatalf("segundo ClaimNext: %v", err)
	}
	if ok && segundo.ID == id {
		t.Fatalf("🔴 EL MISMO JOB (%s) SE RECLAMÓ DOS VECES. El guard `WHERE status = 'pending'` de la "+
			"subconsulta de claimNextSQL no está mordiendo: dos workers correrían el mismo pipeline y "+
			"el cliente recibiría dos presupuestos", id)
	}
	if ok {
		// Un job AJENO (residuo de otro test). No es el fallo que este test busca,
		// pero hay que devolverlo o queda tomado para siempre.
		if _, rerr := jobs.Release(ctx, segundo.ID); rerr != nil {
			t.Logf("devolviendo a la cola el job ajeno %s: %v", segundo.ID, rerr)
		}
		t.Logf("el segundo claim se llevó un job AJENO (%s): la tabla tenía residuo de otro test", segundo.ID)
	}
}

// ---------------------------------------------------------------------------
// (2) LA REANUDACIÓN SALTA ETAPAS CON ARTEFACTO PERSISTIDO
// ---------------------------------------------------------------------------

// exigeReleaseConservaElSobre devuelve el job a la cola y comprueba las DOS cosas
// que Release tiene que hacer y la que NO puede hacer: deja `pending` y NO vacía el
// sobre. Solo los TERMINALES vacían (INV-13): un job devuelto a la cola sin literal
// está vivo y sin con qué continuar.
func exigeReleaseConservaElSobre(ctx context.Context, t *testing.T, db *sql.DB,
	jobs *intake.Postgres, id string) {
	// SIN `t.Helper()` A PROPÓSITO: esta función es una FASE del test, no un helper de
	// una línea. Marcarla colapsaría el frame y el fallo se reportaría en la llamada de
	// la función de test, escondiendo QUÉ aserción cayó.
	devuelto, err := jobs.Release(ctx, id)
	if err != nil || !devuelto {
		t.Fatalf("Release = (%t, %v); ESPERADO (true, nil)", devuelto, err)
	}
	f := leerFila(ctx, t, db, id)
	if f.status != intake.StatusPending {
		t.Fatalf("status tras Release = %q; ESPERADO %q", f.status, intake.StatusPending)
	}
	exigeSobreEscrito(t, f, "🔴 Release VACIÓ EL SOBRE")
}

// exigeReanudacionCompleta mira el claim de vuelta: tiene que decir DÓNDE se quedó,
// QUÉ ya hizo y traer el literal. Sin las tres cosas el worker no puede continuar sin
// repetir trabajo.
func exigeReanudacionCompleta(t *testing.T, segundo intake.ClaimedJob) {
	// SIN `t.Helper()` A PROPÓSITO: esta función es una FASE del test, no un helper de
	// una línea. Marcarla colapsaría el frame y el fallo se reportaría en la llamada de
	// la función de test, escondiendo QUÉ aserción cayó.
	if segundo.Stage != intake.StageP2 {
		t.Fatalf("Stage del claim de reanudación = %q; ESPERADO %q — sin esto el worker repetiría P2, "+
			"que es una llamada al LLM de 22-32 s tirada", segundo.Stage, intake.StageP2)
	}
	if _, ok := segundo.Artifacts[intake.StageP2]; !ok {
		t.Fatalf("el claim de reanudación NO trae el artefacto de p2 (artifacts=%v); el worker no "+
			"podría saltarse la etapa sin volver a leer la fila", segundo.Artifacts)
	}
	if !segundo.SourceText.Complete() {
		t.Fatalf("el claim de reanudación llega SIN sobre; el job no puede continuar")
	}
}

// exigeQueNoRetrocede intenta guardar p2 con el job ya en p3 y comprueba las TRES
// consecuencias: SaveStage devuelve false, `stage` sigue en p3 y el artefacto viejo
// de p2 NO se pisó.
func exigeQueNoRetrocede(ctx context.Context, t *testing.T, db *sql.DB, jobs *intake.Postgres, id string) {
	// SIN `t.Helper()` A PROPÓSITO: esta función es una FASE del test, no un helper de
	// una línea. Marcarla colapsaría el frame y el fallo se reportaría en la llamada de
	// la función de test, escondiendo QUÉ aserción cayó.
	retroceso, err := jobs.SaveStage(ctx, id, artefacto(intake.StageP2, "ideas-viejas"))
	if err != nil {
		t.Fatalf("SaveStage(p2) desde p3: %v", err)
	}
	if retroceso {
		t.Fatalf("🔴 LA MÁQUINA RETROCEDIÓ de p3 a p2. Un worker rezagado con un artefacto viejo dejaría " +
			"`stage` apuntando hacia atrás y la siguiente reanudación repetiría trabajo ya hecho")
	}
	f := leerFila(ctx, t, db, id)
	if !f.stage.Valid || f.stage.String != intake.StageP3 {
		t.Fatalf("stage tras el intento de retroceso = %v; ESPERADO %q (intacto)", f.stage, intake.StageP3)
	}
	if !bytes.Contains(claves(t, f.artifacts)[intake.StageP2], []byte("ideas")) ||
		bytes.Contains(claves(t, f.artifacts)[intake.StageP2], []byte("ideas-viejas")) {
		t.Fatalf("el artefacto de p2 CAMBIÓ pese al guard: %s", f.artifacts)
	}
}

// TestIntegration_Reanudacion_SaltaEtapasConArtefactoPersistido recorre el camino
// completo de un job que se cae a mitad y vuelve.
//
// SALIDAS ESPERADAS:
//   - tras SaveStage(p2) ....... stage='p2', artifacts={"p2":…}
//   - tras Release ............. status='pending' y EL SOBRE SIGUE (Release no vacía nada)
//   - segundo claim ............ Stage="p2" y Artifacts["p2"] presente ⇒ el worker se salta P2
//   - tras SaveStage(p3) ....... artifacts={"p2":…,"p3":…} — el `||` FUSIONA, no sustituye
//   - SaveStage(p2) desde p3 ... (false, nil): la máquina NO retrocede, y p2 NO cambia
//
// MUTACIONES QUE LO PONEN ROJO (ejecutadas):
//   - `artifacts = jsonb_build_object(...)` en vez de `j.artifacts || …` ⇒ p3 borra p2.
//   - quitar el guard `array_position(...) <= …` ⇒ el retroceso a p2 devuelve true.
//   - vaciar el sobre en releaseSQL ⇒ el segundo claim llega sin literal.
func TestIntegration_Reanudacion_SaltaEtapasConArtefactoPersistido(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	id := jobPendienteConSobre(ctx, t, db, jobs, k, []string{"wamid.uno"})
	reclamarElMio(ctx, t, jobs, id)

	guardaEtapa(ctx, t, jobs, id, artefacto(intake.StageP2, "ideas"), "es la primera etapa del job")
	if f := leerFila(ctx, t, db, id); !f.stage.Valid || f.stage.String != intake.StageP2 {
		t.Fatalf("stage = %v; ESPERADO %q", f.stage, intake.StageP2)
	}

	exigeReleaseConservaElSobre(ctx, t, db, jobs, id)

	// LA REANUDACIÓN: el segundo claim tiene que decir dónde se quedó y qué ya hizo.
	exigeReanudacionCompleta(t, reclamarElMio(ctx, t, jobs, id))

	guardaEtapa(ctx, t, jobs, id, artefacto(intake.StageP3, "specs"), "p3 va después de p2")
	f := leerFila(ctx, t, db, id)
	arts := claves(t, f.artifacts)
	if len(arts) != 2 || arts[intake.StageP2] == nil || arts[intake.StageP3] == nil {
		t.Fatalf("artifacts = %s; ESPERADO LAS DOS etapas. El `||` de jsonb sobre objetos FUSIONA: si "+
			"aquí falta p2, la sentencia está SUSTITUYENDO el objeto y cada etapa borra la anterior "+
			"— la reanudación dejaría de existir", f.artifacts)
	}

	// LA MÁQUINA NO RETROCEDE.
	exigeQueNoRetrocede(ctx, t, db, jobs, id)

	// Y sí puede REPETIR la etapa actual: una reanudación a mitad de p3 vuelve a
	// producirla y tiene que poder guardarla (`<=`, no `<`).
	guardaEtapa(ctx, t, jobs, id, artefacto(intake.StageP3, "specs-v2"),
		"repetir la etapa ACTUAL es legítimo tras una reanudación; lo que no vale es retroceder")
}

// ---------------------------------------------------------------------------
// (3) UN ARTEFACTO INVÁLIDO JAMÁS SE PERSISTE
// ---------------------------------------------------------------------------

// TestIntegration_ArtefactoInvalido_JamasSePersiste comprueba lo que dice el título
// MIRANDO LA FILA, no el valor de retorno: que SaveStage devuelva error no prueba que
// no haya escrito.
//
// 🔴 EL CASO QUE MUERDE es el objeto JSON válido SIN `version`: los otros los
// rechazaría también el `::jsonb` de Postgres, así que un test que solo los llevara
// pasaría con la validación borrada.
//
// MUTACIÓN QUE LO PONE ROJO (ejecutada): quitar la llamada a `a.Validate()` de
// SaveStage ⇒ `{"ideas":[]}` se persiste bajo la clave p2.
func TestIntegration_ArtefactoInvalido_JamasSePersiste(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	id := jobPendienteConSobre(ctx, t, db, jobs, k, []string{"wamid.uno"})
	reclamarElMio(ctx, t, jobs, id)

	invalidos := []struct {
		nombre string
		art    intake.Artifact
	}{
		{"objeto SIN version", intake.Artifact{Stage: intake.StageP2, Payload: []byte(`{"ideas":[]}`)}},
		{"JSON roto", intake.Artifact{Stage: intake.StageP2, Payload: []byte(`{"version":1`)}},
		{"array", intake.Artifact{Stage: intake.StageP2, Payload: []byte(`[1,2]`)}},
		{"etapa fuera del vocabulario", intake.Artifact{Stage: "p5", Payload: []byte(`{"version":1}`)}},
		{"payload vacío", intake.Artifact{Stage: intake.StageP2}},
	}
	for _, c := range invalidos {
		t.Run(c.nombre, func(t *testing.T) {
			ok, err := jobs.SaveStage(ctx, id, c.art)
			if err == nil {
				t.Fatalf("SaveStage(%s) = (%t, nil); ESPERADO error ANTES de tocar la base", c.nombre, ok)
			}
			if ok {
				t.Fatalf("SaveStage(%s) devolvió true con error", c.nombre)
			}
			f := leerFila(ctx, t, db, id)
			if string(f.artifacts) != "{}" {
				t.Fatalf("artifacts = %s; ESPERADO {} — el artefacto inválido SE PERSISTIÓ. La puerta "+
					"está en Go (Artifact.Validate) porque Postgres aceptaría cualquier objeto JSON",
					f.artifacts)
			}
			if f.stage.Valid {
				t.Fatalf("stage = %v; ESPERADO NULL — un artefacto rechazado no mueve la etapa", f.stage)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (4) INV-13: LOS DOS TERMINALES VACÍAN LAS TRES PIEZAS DEL SOBRE
// ---------------------------------------------------------------------------

// idBorrador es el `intake_id` que la Ola 3 escribirá al terminar. Aquí es un UUID
// cualquiera: lo que se prueba es que se escribe EN la transición.
const idBorrador = "11111111-2222-3333-4444-555555555555"

// causaDelFallo es la causa que viaja a `Fail`, y se comprueba dos veces: al fallar
// el job y después de rebotarle los terminales encima.
const causaDelFallo = "el proveedor devolvió una salida degenerada tres veces seguidas"

// exigeDoneVaciaElSobre lleva un job hasta `done` y comprueba QUÉ se borra y QUÉ
// sobrevive. Devuelve el id del job.
//
//	🕳️ TRAMPA 2 — EL NULL ES EL DEFAULT: antes de terminar, vuelve a exigir el sobre
//	ESCRITO con el job ya en `processing` (ver `exigeSobreEscrito`). Sin ese estado
//	anterior fabricado y verificado, el NULL de después lo pondría el DEFAULT de la
//	0072 y esto no probaría nada.
func exigeDoneVaciaElSobre(ctx context.Context, t *testing.T, db *sql.DB, jobs *intake.Postgres,
	k intake.WindowKey) string {
	// SIN `t.Helper()` A PROPÓSITO: esta función es una FASE del test, no un helper de
	// una línea. Marcarla colapsaría el frame y el fallo se reportaría en la llamada de
	// la función de test, escondiendo QUÉ aserción cayó.
	idA := jobPendienteConSobre(ctx, t, db, jobs, k, []string{"wamid.a1", "wamid.a2"})
	reclamarElMio(ctx, t, jobs, idA)
	guardaEtapa(ctx, t, jobs, idA, artefacto(intake.StageDraft, "borrador"), "el job A llega hasta el borrador")

	// El estado ANTERIOR, verificado con el job ya en `processing`: las tres puestas.
	exigeSobreEscrito(t, leerFila(ctx, t, db, idA), "job A en `processing`, justo antes de terminarlo")
	refsAntes := leerFila(ctx, t, db, idA).sourceRefs

	if ok, err := jobs.Finish(ctx, idA, idBorrador); err != nil || !ok {
		t.Fatalf("Finish = (%t, %v); ESPERADO (true, nil)", ok, err)
	}
	fa := leerFila(ctx, t, db, idA)
	if fa.status != intake.StatusDone {
		t.Fatalf("status = %q; ESPERADO %q", fa.status, intake.StatusDone)
	}
	exigeSobreVaciado(t, fa, intake.StatusDone)
	if !bytes.Equal(fa.sourceRefs, refsAntes) {
		t.Fatalf("source_refs = %s; ESPERADO %s (INTACTO) — el rastro opaco sobrevive al terminal: es "+
			"lo único que queda del hilo, y el literal ya vive en conversation_event_messages",
			fa.sourceRefs, refsAntes)
	}
	if len(claves(t, fa.artifacts)) != 1 {
		t.Fatalf("artifacts = %s; ESPERADO el borrador INTACTO — solo el literal se borra", fa.artifacts)
	}
	if !fa.intakeID.Valid || !strings.EqualFold(fa.intakeID.String, idBorrador) {
		t.Fatalf("intake_id = %v; ESPERADO %s — se escribe EN la transición porque `done` es absorbente "+
			"y un UPDATE posterior afectaría 0 filas", fa.intakeID, idBorrador)
	}
	return idA
}

// exigeFailedVaciaElSobre lleva OTRO job —misma tupla: el índice único es PARCIAL—
// hasta `failed` y comprueba que el vaciado es idéntico al del camino feliz, que la
// causa queda escrita y que `stage` se CONSERVA. Devuelve el id del job.
func exigeFailedVaciaElSobre(ctx context.Context, t *testing.T, db *sql.DB, jobs *intake.Postgres,
	k intake.WindowKey) string {
	// SIN `t.Helper()` A PROPÓSITO: esta función es una FASE del test, no un helper de
	// una línea. Marcarla colapsaría el frame y el fallo se reportaría en la llamada de
	// la función de test, escondiendo QUÉ aserción cayó.
	idB := jobPendienteConSobre(ctx, t, db, jobs, k, []string{"wamid.b1"})
	reclamarElMio(ctx, t, jobs, idB)
	guardaEtapa(ctx, t, jobs, idB, artefacto(intake.StageP3, "specs"), "el job B muere en p3")

	exigeSobreEscrito(t, leerFila(ctx, t, db, idB), "job B en `processing`, justo antes de fallarlo")

	if ok, err := jobs.Fail(ctx, idB, causaDelFallo); err != nil || !ok {
		t.Fatalf("Fail = (%t, %v); ESPERADO (true, nil)", ok, err)
	}
	fb := leerFila(ctx, t, db, idB)
	if fb.status != intake.StatusFailed {
		t.Fatalf("status = %q; ESPERADO %q", fb.status, intake.StatusFailed)
	}
	exigeSobreVaciado(t, fb, intake.StatusFailed)
	if !fb.errorTexto.Valid || fb.errorTexto.String != causaDelFallo {
		t.Fatalf("error = %v; ESPERADO %q", fb.errorTexto, causaDelFallo)
	}
	if !fb.stage.Valid || fb.stage.String != intake.StageP3 {
		t.Fatalf("stage = %v; ESPERADO %q CONSERVADO — dónde murió es rastro, no búfer", fb.stage, intake.StageP3)
	}
	return idB
}

// exigeTerminalesAbsorbentes rebota las CUATRO transiciones posibles contra los dos
// jobs terminados y comprueba que ninguna aplica, ni deja rastro.
func exigeTerminalesAbsorbentes(ctx context.Context, t *testing.T, db *sql.DB, jobs *intake.Postgres,
	idA, idB string) {
	// SIN `t.Helper()` A PROPÓSITO: esta función es una FASE del test, no un helper de
	// una línea. Marcarla colapsaría el frame y el fallo se reportaría en la llamada de
	// la función de test, escondiendo QUÉ aserción cayó.
	for _, c := range []struct {
		nombre string
		fn     func() (bool, error)
	}{
		{"Fail sobre un job `done`", func() (bool, error) { return jobs.Fail(ctx, idA, "tarde") }},
		{"Finish sobre un job `failed`", func() (bool, error) { return jobs.Finish(ctx, idB, "") }},
		{"SaveStage sobre un job `done`", func() (bool, error) {
			return jobs.SaveStage(ctx, idA, artefacto(intake.StageDraft, "tarde"))
		}},
		{"Release sobre un job `failed`", func() (bool, error) { return jobs.Release(ctx, idB) }},
	} {
		ok, err := c.fn()
		if err != nil {
			t.Fatalf("%s: %v", c.nombre, err)
		}
		if ok {
			t.Fatalf("🔴 %s devolvió true: EL TERMINAL NO ES ABSORBENTE. Un job terminado que vuelve a "+
				"moverse puede reabrir el literal, duplicar el presupuesto o perder la causa del fallo",
				c.nombre)
		}
	}
	if f := leerFila(ctx, t, db, idA); f.status != intake.StatusDone {
		t.Fatalf("el job `done` cambió de estado: %q", f.status)
	}
	if f := leerFila(ctx, t, db, idB); f.status != intake.StatusFailed || f.errorTexto.String != causaDelFallo {
		t.Fatalf("el job `failed` cambió: status=%q error=%v", f.status, f.errorTexto)
	}
}

// exigeBarridoINV13Limpio corre el barrido de INV-13 tal como lo escribe el criterio
// — y ANTES la cuenta que lo hace significar algo.
//
//	🕳️ TRAMPA 1 — CERO FILAS. El barrido `SELECT count(*) … WHERE status IN
//	('done','failed') AND (…)` da 0 sobre una tabla SIN jobs terminales. El 0 no
//	significa «se vaciaron»: significa «no había ninguno». Por eso se cuentan ANTES
//	los terminales del propio tenant y se exige que sean 2.
//
// Y las TRES columnas con `OR`, nunca solo la `_enc`: media fila borrada —el sobre
// sin su DEK, o la DEK sin su sobre— pasaría un barrido que mirase una sola columna
// y deja una fila que ni descifra ni está limpia.
func exigeBarridoINV13Limpio(ctx context.Context, t *testing.T, db *sql.DB, tenantID string) {
	// SIN `t.Helper()` A PROPÓSITO: esta función es una FASE del test, no un helper de
	// una línea. Marcarla colapsaría el frame y el fallo se reportaría en la llamada de
	// la función de test, escondiendo QUÉ aserción cayó.

	var terminales int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.intake_jobs
		 WHERE tenant_id = $1 AND status IN ('done','failed')
	`, tenantID).Scan(&terminales); err != nil {
		t.Fatalf("contar terminales del tenant: %v", err)
	}
	if terminales != 2 {
		t.Fatalf("terminales del tenant = %d; ESPERADO 2 (uno `done`, uno `failed`). SIN ESTA CUENTA el "+
			"barrido de abajo daría 0 POR CERO FILAS y saldría verde con el vaciado borrado", terminales)
	}

	var sucias int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.intake_jobs
		 WHERE status IN ('done','failed')
		   AND (source_text_enc IS NOT NULL OR source_text_dek IS NOT NULL OR source_text_kek_id IS NOT NULL)
	`).Scan(&sucias); err != nil {
		t.Fatalf("barrido de INV-13: %v", err)
	}
	if sucias != 0 {
		t.Fatalf("🔴 BARRIDO DE INV-13 = %d; ESPERADO 0. Hay jobs TERMINADOS que conservan alguna pieza "+
			"del sobre del literal del cliente. Las TRES con OR: media fila borrada pasaría un barrido "+
			"que mirase solo `source_text_enc`", sucias)
	}
}

// TestIntegration_INV13_LosTerminalesVacianLasTresPiezasDelSobre es EL criterio de
// T2.1, y lleva dentro las DOS trampas por las que un barrido de este tipo sale
// verde sin que el código haga nada. Se dicen aquí para que nadie las cuente luego
// como conducta viva:
//
//	🕳️ TRAMPA 1 — CERO FILAS. El barrido `SELECT count(*) … WHERE status IN
//	('done','failed') AND (…)` da 0 sobre una tabla SIN jobs terminales. El 0 no
//	significa «se vaciaron»: significa «no había ninguno». Por eso este test cuenta
//	ANTES los terminales de su propio tenant y exige que sean 2 (en
//	`exigeBarridoINV13Limpio`, la cuenta y el barrido juntos y en ese orden).
//
//	🕳️ TRAMPA 2 — EL NULL ES EL DEFAULT. Las tres columnas nacen NULLables (0072): si
//	el job nunca tuvo sobre, el NULL final lo puso el DEFAULT, no la transición. Por
//	eso `jobPendienteConSobre` ESCRIBE el sobre y comprueba que quedó escrito, y por
//	eso se vuelve a comprobar con el job ya en `processing`, justo antes de
//	terminarlo: el estado anterior está fabricado y verificado.
//
// Y las TRES columnas con `OR`, nunca solo la `_enc`: media fila borrada —el sobre
// sin su DEK, o la DEK sin su sobre— pasaría un barrido que mirase una sola columna
// y deja una fila que ni descifra ni está limpia.
//
// SALIDAS ESPERADAS:
//   - job A ⇒ done   : las tres NULL, source_refs intacto, artifacts intacto, intake_id escrito
//   - job B ⇒ failed : las tres NULL, `error` con la causa, `stage` CONSERVADO (dónde murió)
//   - barrido de INV-13 ......... 0
//   - terminales del tenant ..... 2 (si fuera 0, el barrido anterior no significaría nada)
//
// MUTACIONES QUE LO PONEN ROJO (ejecutadas, y REEJECUTADAS tras extraer los helpers):
//   - vaciar solo `source_text_enc` en finishSQL ⇒ rojo en `exigeSobreVaciado(done)`.
//   - vaciar solo `source_text_enc` en failSQL   ⇒ rojo en `exigeSobreVaciado(failed)`.
//   - quitar `AND j.status = 'processing'` de failSQL ⇒ rojo en `exigeTerminalesAbsorbentes`.
func TestIntegration_INV13_LosTerminalesVacianLasTresPiezasDelSobre(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	jobs := intake.NewPostgres(db)
	k := claveDeVentana(t, db)

	idA := exigeDoneVaciaElSobre(ctx, t, db, jobs, k)
	idB := exigeFailedVaciaElSobre(ctx, t, db, jobs, k)
	exigeTerminalesAbsorbentes(ctx, t, db, jobs, idA, idB)
	exigeBarridoINV13Limpio(ctx, t, db, k.TenantID)
}
