package intakes_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// literal_integration_test.go — EL BARRIDO POR SUBSTRING CONTRA POSTGRES DE VERDAD
// (Plan 044 · Ola 3 · T3.5).
//
// Es el criterio de T3.5 en su forma literal: «verificación por SQL directo en BD
// local: ningún fragmento del texto de Ambar aparece en claro en
// intake_revisions.payload ni en intake_jobs.source_text (barrido por substring); la
// API de detalle sí devuelve el texto descifrado al dueño autorizado; test de poda
// con reloj fake».
//
// Se salta solo sin WAPP_TEST_DB_DSN, como el resto de los *_integration_test.go de
// este repo. Con `INTEGRATION_PG_PORT=55441 make test-integration` corre.
//
// ════════════════════════════════════════════════════════════════════════════
// 🔴 LAS TRES TRAMPAS DE UN BARRIDO POR SUBSTRING, Y CÓMO LAS EVITA ÉSTE
// ════════════════════════════════════════════════════════════════════════════
//
//  1. BUSCAR UN TEXTO QUE NUNCA SE ESCRIBIÓ. Sale verde midiendo cero. Aquí el mismo
//     barrido corre DOS VECES sobre el mismo fragmento: contra el payload (tiene que
//     dar 0) y contra el literal DESCIFRADO del sobre (tiene que dar >0). El segundo
//     es el control positivo: si el texto no se hubiera persistido, ese lado fallaría
//     y el test no podría pasar por la puerta de atrás.
//
//  2. BUSCAR CON SALTOS DE LÍNEA DENTRO. `payload::text` devuelve el JSON con los
//     saltos ESCAPADOS (`\n` de dos caracteres), así que buscar el texto de Ambar
//     entero —que los lleva reales— NO CASARÍA NUNCA aunque estuviera en claro. Sería
//     un barrido que no puede fallar. Por eso los fragmentos son frases de UNA línea.
//
//  3. BUSCAR PALABRAS QUE TAMBIÉN SON DATO DE NEGOCIO. «decoración infantil» está en
//     `customization` y «torta de vainilla…» en el `label` de una línea sin match: los
//     dos son NIVEL 1 del ADR-0034 y van en claro A PROPÓSITO. Un barrido que los
//     incluyera saldría rojo pidiendo que se cifre lo que el ADR prohíbe cifrar. Lo
//     que se barre es lo que D-044.13 manda cifrar: el literal y las evidencias.

// textoAmbarCopia es una COPIA del fixture de `internal/intake/stages`
// (`ambar_fixture_test.go`), que no se puede importar porque vive en el paquete de
// tests de otro módulo interno.
//
// ⚠️ HEREDA SU CALIDAD C: LO REDACTÓ CLAUDE, NO ES EL TEXTO REAL DE AMBAR. Como
// detector de regresión sirve igual —lo que se prueba es que un literal largo del
// cliente no acaba en claro, y para eso cualquier literal largo vale—, pero no
// acredita nada sobre el caso real. El día que aparezca el texto de verdad se cambian
// las dos copias.
const textoAmbarCopia = "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###\n" +
	"cliente: Hola, buenas! Te quería pedir un presupuesto para el miércoles de la semana que viene\n" +
	"cliente: Serían 2 tortas. Una torta sería con decoración infantil, de bizcocho húmedo de chocolate " +
	"con crema de chocolate, de 10 o 12 porciones\n" +
	"cliente: Y la otra de bizcocho de vainilla que tenga lluvia de colores, con dulce de leche y " +
	"merengue, de 25 o 30 porciones\n" +
	"cliente: También quería un paquete de tequeños congelados de 30\n" +
	"cliente: Me pasas precio porfa?\n" +
	"### FIN DE LOS MENSAJES ###"

// fragmentosDelLiteral son las frases que TIENEN que estar cifradas: trozos del
// literal y las evidencias del design §7.1. Ninguna lleva salto de línea (trampa 2) y
// ninguna es un label ni una customization (trampa 3).
var fragmentosDelLiteral = []string{
	"Hola, buenas! Te quería pedir un presupuesto",
	"Me pasas precio porfa?",
	"Serían 2 tortas",
	"con dulce de leche y merengue",
	"una torta sería con decoración infantil, de bizcocho húmedo de chocolate",
	"otra de bizcocho de vainilla que tenga lluvia de colores",
	"un paquete de tequeños congelados de 30",
	"para el miércoles de la semana que viene",
}

// payloadInterpretadoDeAmbar arma el payload §7.4 con el literal dentro, tal como lo
// entrega la etapa draft. Lleva a propósito los dos campos de NIVEL 1 que el barrido
// no puede tocar: la customization y el label de la línea sin match.
func payloadInterpretadoDeAmbar(t *testing.T) json.RawMessage {
	t.Helper()
	cuerpo := map[string]any{
		"version":     intakes.RevisionPayloadVersion,
		"source_text": textoAmbarCopia,
		"message_ts":  "2026-07-13T09:55:00-03:00",
		"analysis": map[string]any{
			"provider": "api", "model": "claude-x", "source": "event_thread", "reanalyzed_from": nil,
		},
		"lines": []any{
			map[string]any{
				"kind": "matched", "sku": "TORTA-CHOC", "label": "Torta chocolate húmedo + crema choc.",
				"qty": 1, "unit_price": nil, "customization": "decoración infantil",
				"evidence": "una torta sería con decoración infantil, de bizcocho húmedo de chocolate",
			},
			map[string]any{
				"kind": "unmatched", "label": "torta de vainilla con lluvia de colores",
				"qty": 1, "unit_price": nil,
				"evidence": "otra de bizcocho de vainilla que tenga lluvia de colores",
			},
			map[string]any{
				"kind": "matched", "sku": "TEQ-30", "label": "Tequeños congelados",
				"qty": 1, "unit_price": 490,
				"evidence": "un paquete de tequeños congelados de 30",
			},
			map[string]any{
				"kind": "shipping", "sku": "_shipping", "label": "Envío por confirmar",
				"qty": 1, "unit_price": nil,
			},
		},
		"suggested_questions": []string{},
	}
	raw, err := json.Marshal(cuerpo)
	if err != nil {
		t.Fatalf("armando el payload de Ambar: %v", err)
	}
	return raw
}

// contieneEnBD es EL BARRIDO: pregunta a Postgres si un texto aparece como subcadena
// en una expresión. `strpos` y no `LIKE` a propósito — `LIKE` interpretaría `%` y `_`
// del texto buscado como comodines, y entonces el barrido buscaría otra cosa.
func contieneEnBD(ctx context.Context, t *testing.T, db *sql.DB, expr, tabla, filtro, valorFiltro, aguja string) bool {
	t.Helper()
	var n int
	q := `SELECT count(*) FROM ` + tabla + ` WHERE ` + filtro + ` = $1 AND strpos(` + expr + `, $2) > 0`
	if err := db.QueryRowContext(ctx, q, valorFiltro, aguja).Scan(&n); err != nil {
		t.Fatalf("barrido por substring (%s): %v", aguja, err)
	}
	return n > 0
}

// TestLiteralPG_NingunFragmentoDeAmbarEnClaro es el criterio de T3.5, con SQL de
// verdad contra la tabla de verdad.
func TestLiteralPG_NingunFragmentoDeAmbarEnClaro(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	cipher, _ := cipherDePrueba(t)

	tenant, intakeID := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{{intakeID, intakes.StatusPendingApproval, "sess-ambar", 1}})
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intake_revisions WHERE intake_id = $1`, intakeID); err != nil {
			t.Logf("limpiando revisiones: %v", err)
		}
	})

	store := intakes.NewPostgres(db, intakes.ConCifraDeLiteral(cipher))
	escrita, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID:  intakeID,
		Kind:      intakes.RevisionKindInterpreted,
		Payload:   payloadInterpretadoDeAmbar(t),
		CreatedBy: intakes.RevisionBySystem,
	})
	if err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}
	if escrita.RevisionNo != 1 {
		t.Fatalf("revision_no = %d, se esperaba 1", escrita.RevisionNo)
	}

	// ── CONTROL POSITIVO: EL SOBRE EXISTE Y TRAE EL TEXTO ────────────────────────
	exigirLiteralRetenido(ctx, t, db, cipher, intakeID)
	// ── EL BARRIDO: NINGÚN FRAGMENTO EN CLARO ───────────────────────────────────
	exigirNadaEnClaro(ctx, t, db, intakeID)
	// ── LA INTERPRETACIÓN ESTRUCTURADA SIGUE EN CLARO Y CONSULTABLE POR SQL ──────
	exigirInterpretacionEnClaro(ctx, t, db, intakeID)
}

// exigirLiteralRetenido lee el trío por SQL directo, lo descifra con la misma llave y
// comprueba que el texto ESTÁ. Es la mitad que impide que el barrido de al lado sea
// una tautología: sin ella, un fixture que no persistiera nada dejaría el barrido
// midiendo cero y saliendo verde. Es lo más cerca que se puede estar de mirar la fila
// con psql.
func exigirLiteralRetenido(ctx context.Context, t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, intakeID string) {
	t.Helper()
	var enc, dek []byte
	var kekID sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT literal_enc, literal_dek, literal_kek_id
		FROM public.intake_revisions WHERE intake_id = $1 AND revision_no = 1
	`, intakeID).Scan(&enc, &dek, &kekID); err != nil {
		t.Fatalf("leyendo el sobre por SQL directo: %v", err)
	}
	if len(enc) == 0 || len(dek) == 0 || !kekID.Valid {
		t.Fatalf("el literal NO se persistió (enc=%d dek=%d kek=%v): el barrido mediría cero",
			len(enc), len(dek), kekID.Valid)
	}
	claro, err := cipher.Decrypt(enc, dek, kekID.String)
	if err != nil {
		t.Fatalf("descifrando el sobre leído por SQL: %v", err)
	}
	for _, f := range fragmentosDelLiteral {
		if !strings.Contains(claro, f) {
			t.Fatalf("el fragmento %q no está ni siquiera en el sobre descifrado: el fixture no lo persistió", f)
		}
	}
}

// exigirNadaEnClaro es EL BARRIDO del criterio, en tres pasadas: sobre el payload,
// sobre las claves del contrato §7.4 y sobre la fila ENTERA.
func exigirNadaEnClaro(ctx context.Context, t *testing.T, db *sql.DB, intakeID string) {
	t.Helper()
	for _, f := range fragmentosDelLiteral {
		if contieneEnBD(ctx, t, db, "payload::text", "public.intake_revisions", "intake_id", intakeID, f) {
			t.Fatalf("FRAGMENTO EN CLARO en intake_revisions.payload: %q", f)
		}
		// La fila ENTERA: el sobre sale como hex, no como texto, así que nada del
		// literal puede aparecer en ninguna columna.
		if contieneEnBD(ctx, t, db, "intake_revisions::text", "public.intake_revisions", "intake_id", intakeID, f) {
			t.Fatalf("FRAGMENTO EN CLARO en alguna columna de intake_revisions: %q", f)
		}
	}
	// Y tampoco las CLAVES: si estuvieran, es que el partido no corrió.
	for _, clave := range []string{`"source_text"`, `"evidence"`} {
		if contieneEnBD(ctx, t, db, "payload::text", "public.intake_revisions", "intake_id", intakeID, clave) {
			t.Fatalf("la clave %s sigue en el payload persistido", clave)
		}
	}
}

// exigirInterpretacionEnClaro es la otra mitad del ADR-0034: si esto no estuviera, se
// habría cifrado de más y el negocio habría perdido su agregado.
func exigirInterpretacionEnClaro(ctx context.Context, t *testing.T, db *sql.DB, intakeID string) {
	t.Helper()
	for _, nivel1 := range []string{"TORTA-CHOC", "TEQ-30", "decoración infantil", "torta de vainilla con lluvia de colores"} {
		if !contieneEnBD(ctx, t, db, "payload::text", "public.intake_revisions", "intake_id", intakeID, nivel1) {
			t.Fatalf("la interpretación estructurada perdió %q: se cifró de más", nivel1)
		}
	}
}

// TestLiteralPG_IntakeJobsNoTieneSourceTextEnClaro cierra la segunda mitad del
// barrido del criterio.
//
// 🔴 Y LO QUE ENCUENTRA ES MÁS FUERTE QUE «LA COLUMNA ESTÁ VACÍA»: la columna
// `intake_jobs.source_text` NO EXISTE. La 0072 nunca la crea —el búfer del literal
// son las TRES columnas del sobre desde D-044.26— y su cabecera solo dice que, si
// alguna base la tuviera de una forma anterior de ese fichero, no se dropearía.
// Ninguna la tiene.
//
// Este test afirma esa ausencia en vez de barrer una columna inexistente, porque un
// barrido sobre lo que no existe es la tautología perfecta: no puede fallar. Y si
// alguien la crea algún día, esto se pone rojo y le obliga a decidir qué hacer con
// ella, que es exactamente lo que hay que forzar.
func TestLiteralPG_IntakeJobsNoTieneSourceTextEnClaro(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	var existe bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'intake_jobs' AND column_name = 'source_text')
	`).Scan(&existe); err != nil {
		t.Fatalf("consultando information_schema: %v", err)
	}
	if existe {
		t.Fatal("public.intake_jobs.source_text EXISTE: hay una columna en claro para el literal del cliente que esta tarea daba por inexistente")
	}

	// Y el trío del sobre sí está, que es lo que hace verdadera la ausencia anterior:
	// el literal del job se guarda, solo que cifrado.
	var trio int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'intake_jobs'
		  AND column_name IN ('source_text_enc','source_text_dek','source_text_kek_id')
	`).Scan(&trio); err != nil {
		t.Fatalf("consultando el trío de intake_jobs: %v", err)
	}
	if trio != 3 {
		t.Fatalf("el sobre de intake_jobs tiene %d de 3 columnas", trio)
	}
}

// TestLiteralPG_ElDetalleDevuelveElTextoDescifrado es la segunda cláusula del
// criterio: «la API de detalle sí devuelve el texto descifrado al dueño autorizado».
// Se prueba en el store porque es donde ocurre el descifrado; que el handler acote al
// tenant (INV-8) ya lo prueban los tests de publicapi, y repetirlo aquí no añadiría
// nada.
func TestLiteralPG_ElDetalleDevuelveElTextoDescifrado(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	cipher, _ := cipherDePrueba(t)

	tenant, intakeID := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{{intakeID, intakes.StatusPendingApproval, "sess-ambar", 1}})
	store := intakes.NewPostgres(db, intakes.ConCifraDeLiteral(cipher))
	if _, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID, Kind: intakes.RevisionKindInterpreted,
		Payload: payloadInterpretadoDeAmbar(t), CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}

	detalle, err := store.Get(ctx, tenant, intakeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detalle.Revisions) != 1 {
		t.Fatalf("el detalle trae %d revisiones, se esperaba 1", len(detalle.Revisions))
	}
	var p struct {
		SourceText string `json:"source_text"`
		Lines      []struct {
			Evidence string `json:"evidence"`
			SKU      string `json:"sku"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(detalle.Revisions[0].Payload, &p); err != nil {
		t.Fatalf("payload del detalle ilegible: %v", err)
	}
	if p.SourceText != textoAmbarCopia {
		t.Fatalf("el detalle no devolvió el literal ENTERO:\n%q", p.SourceText)
	}
	if len(p.Lines) != 4 {
		t.Fatalf("el detalle trae %d líneas, se esperaban 4", len(p.Lines))
	}
	if p.Lines[2].Evidence != "un paquete de tequeños congelados de 30" {
		t.Fatalf("la evidencia no volvió A SU LÍNEA: línea 2 trae %q", p.Lines[2].Evidence)
	}
	if p.Lines[3].Evidence != "" {
		t.Fatalf("la línea de envío no tenía evidencia y volvió con una: %q", p.Lines[3].Evidence)
	}

	// Un store SIN cipher no puede leer esa revisión, y falla en vez de devolver el
	// borrador «pero sin su original»: el dueño estaría comparando su interpretación
	// contra un hueco creyendo que el cliente no escribió nada.
	if _, err := intakes.NewPostgres(db).Get(ctx, tenant, intakeID); err == nil {
		t.Fatal("un store sin FieldCipher devolvió la revisión sin protestar")
	}
}

// TestLiteralPG_PodaAlVencerElTTL es el criterio de la poda contra Postgres real. La
// revisión se ENVEJECE moviendo su created_at, y quien mide la edad es la BD con su
// propio reloj: no hay dos relojes que comparar. La versión con reloj fake de Go —que
// el criterio también pide— está en retencion_test.go, contra el MemoryStore.
func TestLiteralPG_PodaAlVencerElTTL(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	cipher, _ := cipherDePrueba(t)
	spy := &logSpy{}

	tenant, intakeID := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{{intakeID, intakes.StatusPendingApproval, "sess-ambar", 1}})
	store := intakes.NewPostgres(db, intakes.ConCifraDeLiteral(cipher), intakes.ConLogDeRetencion(spy))
	if _, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID, Kind: intakes.RevisionKindInterpreted,
		Payload: payloadInterpretadoDeAmbar(t), CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}

	// Control positivo: hoy la revisión está vigente y devuelve su texto.
	vigente, err := store.Get(ctx, tenant, intakeID)
	if err != nil {
		t.Fatalf("Get vigente: %v", err)
	}
	if !strings.Contains(string(vigente.Revisions[0].Payload), "Me pasas precio porfa?") {
		t.Fatal("la revisión vigente no devuelve su literal: sin esto la poda de abajo no probaría nada")
	}

	// Se ENVEJECE la revisión 13 meses. El TTL por defecto son 12.
	if _, err := db.ExecContext(ctx, `
		UPDATE public.intake_revisions SET created_at = now() - interval '13 months'
		WHERE intake_id = $1 AND revision_no = 1
	`, intakeID); err != nil {
		t.Fatalf("envejeciendo la revisión: %v", err)
	}

	podada, err := store.Get(ctx, tenant, intakeID)
	if err != nil {
		t.Fatalf("Get tras el vencimiento: %v", err)
	}
	if strings.Contains(string(podada.Revisions[0].Payload), "Me pasas precio porfa?") {
		t.Fatal("el literal sobrevivió al vencimiento del TTL")
	}

	exigirFilaPodada(ctx, t, db, intakeID)

	// El evento de poda, sin una palabra del texto destruido.
	log := spy.all()
	if !strings.Contains(log, "podado por TTL vencido") {
		t.Fatalf("la poda no dejó evento en el log:\n%s", log)
	}
	if strings.Contains(log, "porfa") || strings.Contains(log, "tequeños congelados de 30") {
		t.Fatalf("el evento de poda arrastró literal del cliente:\n%s", log)
	}

	// Y no se repite: la segunda lectura ya no tiene nada que podar.
	if _, err := store.Get(ctx, tenant, intakeID); err != nil {
		t.Fatalf("Get tras la poda: %v", err)
	}
	if n := strings.Count(spy.all(), "podado por TTL vencido"); n != 1 {
		t.Fatalf("el evento de poda se emitió %d veces, se esperaba 1", n)
	}
}

// TestLiteralPG_ElTTLDelTenantManda comprueba el otro lado de la configuración: un
// tenant CON fila en tenant_settings manda sobre el default de plataforma, incluido su
// 0, que significa RETENCIÓN INDEFINIDA. Sin este test, el LEFT JOIN podría estar
// devolviendo siempre el default y nadie se enteraría.
func TestLiteralPG_ElTTLDelTenantManda(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	cipher, _ := cipherDePrueba(t)

	tenant, intakeID := uuid.NewString(), uuid.NewString()
	seedPG(t, db, tenant, []fixture{{intakeID, intakes.StatusPendingApproval, "sess-ambar", 1}})
	// tenant_settings no la limpia seedPG: se borra aquí.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, intake_literal_ttl_seconds) VALUES ($1, 0)
		ON CONFLICT (tenant_id) DO UPDATE SET intake_literal_ttl_seconds = 0
	`, tenant); err != nil {
		t.Fatalf("sembrando el TTL del tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant_settings: %v", err)
		}
	})

	store := intakes.NewPostgres(db, intakes.ConCifraDeLiteral(cipher))
	if _, err := store.InsertRevision(ctx, intakes.Revision{
		IntakeID: intakeID, Kind: intakes.RevisionKindInterpreted,
		Payload: payloadInterpretadoDeAmbar(t), CreatedBy: intakes.RevisionBySystem,
	}); err != nil {
		t.Fatalf("InsertRevision: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE public.intake_revisions SET created_at = now() - interval '10 years'
		WHERE intake_id = $1 AND revision_no = 1
	`, intakeID); err != nil {
		t.Fatalf("envejeciendo la revisión: %v", err)
	}

	detalle, err := store.Get(ctx, tenant, intakeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(string(detalle.Revisions[0].Payload), "Me pasas precio porfa?") {
		t.Fatal("con intake_literal_ttl_seconds = 0 se podó una revisión de 10 años: 0 significa SIN PODA")
	}
}

// exigirFilaPodada mira la fila con SQL directo tras la poda: el trío VACÍO, la marca
// puesta y el payload INTACTO. Las tres cosas juntas son lo que distingue una poda de
// una pérdida de datos — un trío en NULL sin `literal_pruned_at` sería indistinguible
// de una revisión que nunca tuvo texto.
func exigirFilaPodada(ctx context.Context, t *testing.T, db *sql.DB, intakeID string) {
	t.Helper()
	var enc []byte
	var prunedAt sql.NullTime
	var payload string
	if err := db.QueryRowContext(ctx, `
		SELECT literal_enc, literal_pruned_at, payload::text
		FROM public.intake_revisions WHERE intake_id = $1 AND revision_no = 1
	`, intakeID).Scan(&enc, &prunedAt, &payload); err != nil {
		t.Fatalf("releyendo la fila podada: %v", err)
	}
	if len(enc) != 0 {
		t.Fatalf("literal_enc sigue con %d bytes tras la poda", len(enc))
	}
	if !prunedAt.Valid {
		t.Fatal("la fila se podó pero literal_pruned_at quedó NULL: la poda no dejó rastro")
	}
	for _, nivel1 := range []string{"TORTA-CHOC", "TEQ-30", "decoración infantil", "_shipping"} {
		if !strings.Contains(payload, nivel1) {
			t.Fatalf("la poda se llevó por delante la interpretación (%s):\n%s", nivel1, payload)
		}
	}
}
