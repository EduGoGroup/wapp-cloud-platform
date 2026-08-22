// source_composer_integration_test.go — Plan 044 · Ola 1 · T1.4, criterio «test de
// INTEGRACIÓN» (tasks.md:304), contra Postgres REAL y con el cifrado REAL.
//
// ⏳ NINGUNO DE ESTOS TESTS SE HA EJECUTADO. Se escribieron sin Go, sin Docker y sin
// Postgres delante, así que ninguno está declarado como pasado. Cada aserción lleva
// escrita LA SALIDA QUE SE ESPERA.
//
// # QUÉ AÑADE ESTO A source_composer_test.go, QUE YA EXISTE
//
// Aquel prueba `ComposeSourceText`, que es una función PURA sobre `[]ThreadEntry`
// construidos a mano: el reparto contexto/literal, los rótulos, el fail-closed. Es lo
// correcto para la REGLA. Lo que no puede probar es LA TUBERÍA, que es donde estaban
// las piezas que sólo existen en la base:
//
//	AppendMessage (cifra) → conversation_event_messages → ListThread (descifra en el
//	borde) → ComposeSourceText → FieldCipher.Encrypt → PutSourceText → intake_jobs
//
// Cinco costuras, y cuatro de ellas son SQL o cripto. En concreto:
//
//   - que `listThreadSQL` devuelva el hilo EN ORDEN (`seq`) y con los CUATRO
//     `entry_kind`, no solo los `message` (filtrar allí sería tomarle al llamante la
//     decisión que REQ-10b le encarga);
//   - que el descifrado en el borde recupere EXACTAMENTE lo que se escribió, con la
//     KEK de CADA fila (`body_kek_id`) y no con la current;
//   - que el sobre de `intake_jobs` se escriba entero y que su `source_text_enc` NO
//     contenga el literal (un «cifrado» que no cifra pasa cualquier test de ida y
//     vuelta que no mire los bytes);
//   - que el `summary` (nivel 1, payload en claro) y el `message_out_of_turn` (nivel
//     2, cuerpo cifrado) —que son GRADOS DISTINTOS en la tabla— produzcan el MISMO
//     hilo literal, que es lo que T1.4 promete al decir que las dos clases de
//     contexto son un solo mecanismo.
//
// Se corre igual que el resto de la casa: sin build tag, por el nombre del fichero,
// con `WAPP_TEST_DB_DSN`. Reusa `openTestDB` y `seedTenant` de
// tenant_resolver_integration_test.go (mismo paquete `runtime_test`).
package runtime_test

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/events"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// ---------------------------------------------------------------------------
// LOS RÓTULOS, COPIADOS A MANO Y A PROPÓSITO
// ---------------------------------------------------------------------------
//
// Las constantes de source_composer.go no se exportan, así que aquí se repiten. NO es
// una duplicación descuidada: esos rótulos son EL CONTRATO CON EL PROMPT DE P2 (el
// bloque que el LLM tiene prohibido usar para extraer ítems), así que tenerlos escritos
// en un test es una segunda copia DELIBERADA — si alguien los cambia sin querer, esto
// se pone rojo y le obliga a mirar el prompt.
const (
	rotuloContextoAbre   = "### CONTEXTO PREVIO — NO es lo que el cliente está pidiendo ###"
	rotuloContextoCierra = "### FIN DEL CONTEXTO PREVIO ###"
	rotuloLiteralAbre    = "### MENSAJES DE LA CONVERSACIÓN (literal, en orden) ###"
	rotuloLiteralCierra  = "### FIN DE LOS MENSAJES ###"
)

// Los tres literales de la ráfaga. Llevan marca reconocible (ZZQ1/ZZQ2/ZZQ3) para
// poder buscarlos en los bytes crudos de la base.
const (
	comp1 = "quiero presupuesto de 20 hamburguesas ZZQ1"
	comp2 = "perfecto, ¿para cuándo las necesitas? ZZQ2"
	comp3 = "para el jueves a las 8 ZZQ3"
)

// contextoMarca es el texto que entra por la puerta del CONTEXTO. Se busca DENTRO del
// bloque rotulado y se exige AUSENTE del bloque literal: es la mitad de T1.4 que
// impide que el LLM lea nuestro propio automensaje como pedido del cliente.
const contextoMarca = "Torta de chocolate ZZCTX"

// composerCipherDePrueba construye el FieldCipher con un keyring de laboratorio. Nombre
// propio para no chocar con webhookCipherDePrueba ni con kpDePrueba, que ya viven en
// este paquete de test.
func composerCipherDePrueba(t *testing.T) *crypto.FieldCipher {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: "test-kek-1:ERERERERERERERERERERERERERERERERERERERERERE=",
		CurrentID:  "test-kek-1",
		IndexB64:   "RERERERERERERERERERERERERERERERERERERERERES=",
	})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return crypto.NewFieldCipher(kp)
}

// hiloDePrueba deja en la base un evento con su HILO ya escrito por el STORE REAL —no
// sembrado a mano— y su ventana de captación ya CERRADA, que es la única forma en que
// `PutSourceText` tiene dónde escribir.
//
// `contexto` decide qué grado se usa para la entrada de contexto:
//   - "summary"     → AppendSummary (nivel 1, payload EN CLARO)
//   - "out_of_turn" → AppendOutOfTurnMessage (nivel 2, cuerpo CIFRADO)
//
// Los dos son la mitad del criterio: son grados DISTINTOS en la tabla y tienen que
// producir el mismo hilo literal.
//
// El orden de escritura es el de la conversación: cliente, contexto, negocio, cliente.
// El contexto va EN MEDIO a propósito — si el compositor se limitara a recortar por los
// extremos en vez de clasificar por `entry_kind`, se le colaría.
func hiloDePrueba(ctx context.Context, t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, contexto string) (*events.Store, intake.WindowKey) {
	t.Helper()
	//nolint:contextcheck // seedTenant es el helper COMPARTIDO del paquete (28 llamantes) y
	// no toma ctx: darle uno aquí obligaría a tocar 27 tests ajenos a esta ola.
	tenantID := seedTenant(t, db)
	sessionID := "sess-comp-" + uuid.NewString()
	contactID := uuid.NewString()

	st := events.NewStore(db, cipher, events.WithClock(func() time.Time {
		return time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	}))
	ev, err := st.CreateEvent(ctx, events.NewEvent{
		TenantID: tenantID, SessionID: sessionID, ContactID: contactID,
		Kind: "cart", FlowID: "flujo-lab", FlowVersion: 3,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	if _, err := st.AppendMessage(ctx, ev.ID, events.RoleClient, comp1); err != nil {
		t.Fatalf("AppendMessage (1): %v", err)
	}
	switch contexto {
	case "summary":
		// Un resumen REAL, construido con su Build*: el compositor lo renderiza con
		// Summary.Render(), que es el mismo texto que el cliente leyó al reanudar.
		sum := events.BuildCartSummary(events.CartState{
			Level: "summary",
			Lines: []events.SummaryLine{{SKU: "TORTA", Label: contextoMarca, Qty: 2, UnitPrice: 5}},
		})
		payload, eerr := sum.Encode()
		if eerr != nil {
			t.Fatalf("Encode del resumen: %v", eerr)
		}
		if _, err := st.AppendSummary(ctx, ev.ID, payload); err != nil {
			t.Fatalf("AppendSummary: %v", err)
		}
	case "out_of_turn":
		if _, err := st.AppendOutOfTurnMessage(ctx, ev.ID, contextoMarca); err != nil {
			t.Fatalf("AppendOutOfTurnMessage: %v", err)
		}
	default:
		t.Fatalf("montaje: clase de contexto desconocida %q", contexto)
	}
	if _, err := st.AppendMessage(ctx, ev.ID, events.RoleBusiness, comp2); err != nil {
		t.Fatalf("AppendMessage (2): %v", err)
	}
	if _, err := st.AppendMessage(ctx, ev.ID, events.RoleClient, comp3); err != nil {
		t.Fatalf("AppendMessage (3): %v", err)
	}

	// La ventana: se abre y se CIERRA, porque `putSourceTextSQL` solo escribe sobre una
	// fila en `pending` (y con el sobre vacío).
	k := intake.WindowKey{TenantID: tenantID, SessionID: sessionID, ContactID: contactID, EventID: ev.ID}
	jobs := intake.NewPostgres(db)
	if err := jobs.OpenOrAppend(ctx, intake.Append{
		Key: k, MessageTS: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC), Refs: []string{"wamid.c1"},
	}); err != nil {
		t.Fatalf("OpenOrAppend: %v", err)
	}
	if cerrada, err := jobs.CloseWindow(ctx, k); err != nil || !cerrada {
		t.Fatalf("CloseWindow = (%v, %v); ESPERADO (true, nil)", cerrada, err)
	}
	//nolint:contextcheck // context.Background() a propósito: este cleanup corre DESPUÉS
	// del test, cuando el ctx del caso ya puede estar cancelado — y entonces no borraría.
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.intake_jobs WHERE tenant_id = $1`, k.TenantID); err != nil {
			t.Logf("limpiando intake_jobs de %s: %v", k.TenantID, err)
		}
	})
	return st, k
}

// sobreDelJob lee el trío del sobre de la ventana, CRUDO.
func sobreDelJob(ctx context.Context, t *testing.T, db *sql.DB, k intake.WindowKey) (enc, dek []byte, kekID sql.NullString) {
	t.Helper()
	if err := db.QueryRowContext(ctx, `
		SELECT source_text_enc, source_text_dek, source_text_kek_id
		  FROM public.intake_jobs
		 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3 AND event_id = $4::uuid
	`, k.TenantID, k.SessionID, k.ContactID, k.EventID).Scan(&enc, &dek, &kekID); err != nil {
		t.Fatalf("leer el sobre de intake_jobs: %v", err)
	}
	return enc, dek, kekID
}

// bloque devuelve lo que hay entre dos rótulos. Falla si falta cualquiera de los dos:
// un `source_text` sin sus delimitadores es un prompt en el que el LLM no puede
// distinguir el contexto del pedido, que es todo lo que T1.4 construye.
func bloque(t *testing.T, texto, abre, cierra string) string {
	t.Helper()
	i := strings.Index(texto, abre)
	if i < 0 {
		t.Fatalf("el source_text no trae el rótulo %q. Texto completo:\n%s", abre, texto)
	}
	resto := texto[i+len(abre):]
	j := strings.Index(resto, cierra)
	if j < 0 {
		t.Fatalf("el source_text no trae el cierre %q. Texto completo:\n%s", cierra, texto)
	}
	return strings.TrimSpace(resto[:j])
}

// ---------------------------------------------------------------------------
// (8) EL HILO REAL → EL source_text, CON LAS DOS CLASES DE CONTEXTO
// ---------------------------------------------------------------------------

// TestIntegration_ComposeAtFlush_HiloRealConSummaryYConFueraDeTurno recorre la tubería
// entera DOS VECES, cambiando SOLO el grado de la entrada de contexto, y exige el
// MISMO resultado en todo lo que T1.4 promete.
//
// «El mismo resultado» se dice con precisión, porque una de las dos cosas SÍ cambia y
// tiene que cambiar:
//
//	IGUAL  → el bloque LITERAL, byte a byte: los 3 mensajes, en orden, con su speaker.
//	IGUAL  → Messages = 3 y ContextEntries = 1.
//	IGUAL  → el texto del contexto NO aparece dentro del bloque literal.
//	DISTINTO → el RÓTULO del contexto, que es lo único que distingue las dos clases:
//	           "[resumen del sistema] …" vs "[mensaje del negocio fuera de turno] …".
//
// SALIDAS ESPERADAS (las dos vueltas):
//   - ListThread devuelve 4 entradas, seq 1..4, con Kind message/…/message/message
//   - Composed.Messages ......... 3
//   - Composed.ContextEntries ... 1
//   - bloque literal ............ "cliente: …ZZQ1\nnegocio: …ZZQ2\ncliente: …ZZQ3"
//   - bloque contexto ........... contiene "ZZCTX"
//   - bloque literal ............ NO contiene "ZZCTX"
//   - source_text_kek_id ........ "test-kek-1"
//   - source_text_enc ........... NO contiene "ZZQ1" en claro
//   - Decrypt(sobre) ............ == Composed.Text
func TestIntegration_ComposeAtFlush_HiloRealConSummaryYConFueraDeTurno(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cipher := composerCipherDePrueba(t)

	var literales [2]string
	for i, clase := range []string{"summary", "out_of_turn"} {
		t.Run(clase, func(t *testing.T) {
			literales[i] = verificaClaseDeContexto(ctx, t, db, cipher, clase)
		})
	}

	// --- 4. LAS DOS CLASES DE CONTEXTO PRODUCEN EL MISMO HILO LITERAL.
	// Es la afirmación central de T1.4: son dos FILAS de una tabla (`contextKinds`), no
	// dos caminos. Si algún día alguien abre el segundo camino, los dos divergirán aquí.
	if literales[0] != literales[1] {
		t.Fatalf("el bloque literal cambia según la clase de contexto.\ncon summary:\n%s\ncon "+
			"message_out_of_turn:\n%s\nESPERADO que fueran IDÉNTICOS: el grado de la entrada de contexto "+
			"no puede alterar lo que el cliente dijo", literales[0], literales[1])
	}
}

// verificaClaseDeContexto recorre la tubería entera para UNA clase de contexto y
// devuelve el bloque LITERAL, que es lo que la vuelta exterior compara entre clases.
//
// Va extraída y no escrita dentro del `t.Run` por una razón mecánica: gocyclo imputa a
// la función madre la complejidad de los closures que contiene, así que un subtest
// inline la infla con un cuerpo que no es suyo.
func verificaClaseDeContexto(ctx context.Context, t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, clase string) string {
	t.Helper()
	st, k := hiloDePrueba(ctx, t, db, cipher, clase)
	entries := leeElHiloEntero(ctx, t, st, k)
	composed := runtime.ComposeSourceText(entries)
	literal := verificaElReparto(t, composed, clase)
	verificaElSobre(ctx, t, db, st, cipher, k, composed)
	return literal
}

// leeElHiloEntero es el paso 1: el hilo vuelve entero, en orden y DESCIFRADO.
func leeElHiloEntero(ctx context.Context, t *testing.T, st *events.Store, k intake.WindowKey) []events.ThreadEntry {
	t.Helper()
	entries, err := st.ListThread(ctx, k.EventID, 200)
	if err != nil {
		t.Fatalf("ListThread: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("entradas del hilo = %d; ESPERADO 4 (3 `message` + 1 de contexto). "+
			"Menos significa que listThreadSQL está filtrando por entry_kind, que es justo lo "+
			"que REQ-10b le prohíbe: quien clasifica es el llamante", len(entries))
	}
	for n := 1; n < len(entries); n++ {
		if entries[n].Seq <= entries[n-1].Seq {
			t.Fatalf("el hilo no viene en orden: seq %d después de %d. La subconsulta de "+
				"listThreadSQL recorta por el PRINCIPIO pero devuelve ASCENDENTE",
				entries[n].Seq, entries[n-1].Seq)
		}
	}
	if entries[0].Text != comp1 {
		t.Fatalf("entries[0].Text = %q; ESPERADO %q — si no coincide, el descifrado en el borde "+
			"no está recuperando lo que AppendMessage guardó", entries[0].Text, comp1)
	}
	return entries
}

// verificaElReparto es el paso 2: la regla pura, sobre entradas que salieron de la BD.
// Devuelve el bloque literal ya extraído.
func verificaElReparto(t *testing.T, composed runtime.Composed, clase string) string {
	t.Helper()
	if composed.Messages != 3 {
		t.Fatalf("Composed.Messages = %d; ESPERADO 3 — el CONTEXTO NO SUMA VOLUMEN "+
			"(REQ-10b (c)): un resumen no es actividad del cliente", composed.Messages)
	}
	if composed.ContextEntries != 1 {
		t.Fatalf("Composed.ContextEntries = %d; ESPERADO 1 (la entrada de clase %q)",
			composed.ContextEntries, clase)
	}

	literal := bloque(t, composed.Text, rotuloLiteralAbre, rotuloLiteralCierra)
	quiero := "cliente: " + comp1 + "\nnegocio: " + comp2 + "\ncliente: " + comp3
	if literal != quiero {
		t.Fatalf("bloque literal =\n%q\nESPERADO\n%q\n(los tres mensajes, en orden, con su "+
			"speaker: `cliente` solo para role=client, todo lo demás `negocio`)", literal, quiero)
	}

	contextoTxt := bloque(t, composed.Text, rotuloContextoAbre, rotuloContextoCierra)
	if !strings.Contains(contextoTxt, "ZZCTX") {
		t.Fatalf("bloque de contexto = %q; ESPERADO que contuviera \"ZZCTX\" — sin él, el «sí, esas "+
			"dos» del cliente se quedaría sin antecedente", contextoTxt)
	}
	if strings.Contains(literal, "ZZCTX") {
		t.Fatalf("el texto del CONTEXTO se coló en el bloque LITERAL:\n%s\n"+
			"ESPERADO que no apareciera. El automensaje de rescate LISTA PRODUCTOS: si entra como "+
			"literal, el LLM extrae como pedido del cliente lo que escribimos nosotros (D-044.24)",
			literal)
	}
	return literal
}

// verificaElSobre es el paso 3: cifrado de verdad, escrito entero, en la fila correcta.
func verificaElSobre(ctx context.Context, t *testing.T, db *sql.DB, st *events.Store,
	cipher *crypto.FieldCipher, k intake.WindowKey, composed runtime.Composed) {
	t.Helper()
	comp := runtime.NewSourceTextComposer(discardLogger(), st, intake.NewPostgres(db), cipher)
	if err := comp.ComposeAtFlush(ctx, k); err != nil {
		t.Fatalf("ComposeAtFlush: %v", err)
	}
	enc, dek, kekID := sobreDelJob(ctx, t, db, k)
	if len(enc) == 0 || len(dek) == 0 || !kekID.Valid {
		t.Fatalf("sobre = (enc=%d bytes, dek=%d bytes, kek_id=%v); ESPERADO las TRES piezas "+
			"puestas. Media escritura deja una fila indescifrable", len(enc), len(dek), kekID)
	}
	if kekID.String != "test-kek-1" {
		t.Fatalf("source_text_kek_id = %q; ESPERADO \"test-kek-1\" — sin el key_id correcto la "+
			"fila queda fuera del barrido de rotación de KEK (Plan 012)", kekID.String)
	}
	if strings.Contains(string(enc), "ZZQ1") {
		t.Fatal("el literal del cliente está EN CLARO dentro de source_text_enc: ESPERADO que NO " +
			"apareciera. Un cifrado que no cifra pasa cualquier prueba de ida y vuelta")
	}
	claro, derr := cipher.Decrypt(enc, dek, kekID.String)
	if derr != nil {
		t.Fatalf("Decrypt del sobre: %v", derr)
	}
	if claro != composed.Text {
		t.Fatalf("el sobre descifrado NO es el texto compuesto.\nguardado:\n%s\nesperado:\n%s",
			claro, composed.Text)
	}
}

// ---------------------------------------------------------------------------
// (9) EL BARRIDO: EL TEXTO DEL HILO NO ESTÁ EN CLARO EN NINGUNA PARTE
// ---------------------------------------------------------------------------

// identificadorSano acota lo que se puede interpolar en el SQL del barrido. Los
// nombres salen de information_schema de una base que nosotros creamos, pero
// interpolar identificadores sin comprobarlos es una costumbre que no se coge «solo
// esta vez».
var identificadorSano = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// TestIntegration_ElLiteralNoQuedaEnClaroEnNingunaTabla es el criterio de barrido de
// T1.4 (REQ-10c: «descifrarlo en el borde de la app y no dejarlo en claro en ningún
// otro sitio»), escrito COMO TEST y no como bloque SQL comentado.
//
// # POR QUÉ COMO TEST, Y NO COMO COMENTARIO PARA QUE LO CORRA EL CLI
//
// Se sopesaron las dos formas. Un bloque SQL en un comentario tiene una ventaja real
// —lo lee un humano y lo pega en psql— y dos defectos que aquí pesan más:
//
//  1. NADIE LO CORRE DOS VECES. Un barrido que se ejecuta el día que se escribe y
//     nunca más no es una garantía, es una anécdota. Lo que este barrido protege
//     —que un campo NUEVO no empiece a guardar el literal en claro— es exactamente un
//     riesgo FUTURO, y una comprobación que no corre en cada CI no lo cubre.
//  2. HAY QUE ESCRIBIRLO A CIEGAS. El barrido honesto no puede enumerar a mano las
//     tablas donde mirar (serían las que uno RECUERDA, y la que se olvida es
//     justamente la que tendría la fuga): tiene que salir de `information_schema`. Y
//     una consulta que se genera desde el catálogo no es un bloque que se pegue en
//     psql, es un bucle — o sea, código.
//
// El bloque SQL equivalente queda escrito abajo de todos modos, para que un operador
// pueda repetirlo a mano en UAT sobre datos reales, que es donde un test no llega.
//
// # CÓMO FUNCIONA
//
// Se escribe una AGUJA irrepetible dentro de un mensaje del cliente, se recorre la
// tubería entera, y después se busca esa aguja en TODA columna de TODA tabla base del
// esquema `public`: las de texto y JSON por `::text LIKE`, y las `bytea` por
// `position(aguja in columna)` —que es la que caza un «cifrado» que no cifró—.
//
// SALIDA ESPERADA: CERO coincidencias. Ni una.
//
// Si aparece alguna, el mensaje dice tabla y columna: eso es una fuga de contenido de
// persona a una columna en claro, y hay que taparla antes de seguir.
func TestIntegration_ElLiteralNoQuedaEnClaroEnNingunaTabla(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cipher := composerCipherDePrueba(t)

	// La AGUJA: irrepetible, sin caracteres especiales de LIKE (ni % ni _), y con
	// pinta de nada que ningún identificador del sistema pueda contener.
	aguja := "AGUJAPII" + strings.ReplaceAll(uuid.NewString(), "-", "")

	tenantID := seedTenant(t, db)
	sessionID := "sess-aguja-" + uuid.NewString()
	contactID := uuid.NewString()
	st := events.NewStore(db, cipher)
	ev, err := st.CreateEvent(ctx, events.NewEvent{
		TenantID: tenantID, SessionID: sessionID, ContactID: contactID,
		Kind: "cart", FlowID: "flujo-lab", FlowVersion: 1,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if _, err := st.AppendMessage(ctx, ev.ID, events.RoleClient, "quiero 20 hamburguesas "+aguja); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	k := intake.WindowKey{TenantID: tenantID, SessionID: sessionID, ContactID: contactID, EventID: ev.ID}
	jobs := intake.NewPostgres(db)
	if err := jobs.OpenOrAppend(ctx, intake.Append{
		Key: k, MessageTS: time.Now().UTC(), Refs: []string{"wamid.aguja"},
	}); err != nil {
		t.Fatalf("OpenOrAppend: %v", err)
	}
	if _, err := jobs.CloseWindow(ctx, k); err != nil {
		t.Fatalf("CloseWindow: %v", err)
	}
	t.Cleanup(func() {
		if _, derr := db.ExecContext(context.Background(),
			`DELETE FROM public.intake_jobs WHERE tenant_id = $1`, k.TenantID); derr != nil {
			t.Logf("limpiando intake_jobs de %s: %v", k.TenantID, derr)
		}
	})
	comp := runtime.NewSourceTextComposer(discardLogger(), st, jobs, cipher)
	if err := comp.ComposeAtFlush(ctx, k); err != nil {
		t.Fatalf("ComposeAtFlush: %v", err)
	}

	// Precondición del barrido: el sobre EXISTE. Sin ella, un barrido limpio podría
	// venir de que no se escribió nada en absoluto — verde por no haber hecho nada.
	enc, _, _ := sobreDelJob(ctx, t, db, k)
	if len(enc) == 0 {
		t.Fatal("montaje: source_text_enc está vacío, así que el barrido no probaría nada. " +
			"ESPERADO que ComposeAtFlush hubiera dejado el sobre")
	}

	censo := censoDeColumnasDeTexto(ctx, t, db)
	fugas := barreLaAgujaPorElCenso(ctx, t, db, censo, aguja)

	if len(fugas) != 0 {
		t.Fatalf("el literal del cliente aparece EN CLARO en %d columna(s): %s\n"+
			"SALIDA ESPERADA: cero. El texto del hilo vive cifrado en "+
			"conversation_event_messages.body_enc y en intake_jobs.source_text_enc, y en claro SOLO en "+
			"memoria, entre ListThread y Encrypt (REQ-10c). Ni flow_events, ni logs, ni telemetría, ni un "+
			"campo nuevo", len(fugas), strings.Join(fugas, ", "))
	}
}

// colDelCenso es una columna candidata a esconder texto: la tupla que devuelve el
// catálogo.
type colDelCenso struct{ tabla, columna, tipo string }

// censoDeColumnasDeTexto lee del CATÁLOGO las columnas donde podría esconderse texto.
// Sale de `information_schema` y no de una lista escrita a mano: la tabla que uno
// olvida es justo la que tiene la fuga.
func censoDeColumnasDeTexto(ctx context.Context, t *testing.T, db *sql.DB) []colDelCenso {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT c.table_name, c.column_name, c.data_type
		  FROM information_schema.columns c
		  JOIN information_schema.tables t
		    ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		 WHERE c.table_schema = 'public'
		   AND t.table_type   = 'BASE TABLE'
		   AND c.data_type IN ('text','character varying','character','json','jsonb','bytea')
		 ORDER BY c.table_name, c.column_name
	`)
	if err != nil {
		t.Fatalf("censar columnas de texto del esquema: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("cerrar el censo de columnas: %v", cerr)
		}
	}()

	var censo []colDelCenso
	for rows.Next() {
		var c colDelCenso
		if serr := rows.Scan(&c.tabla, &c.columna, &c.tipo); serr != nil {
			t.Fatalf("escanear el censo: %v", serr)
		}
		censo = append(censo, c)
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("iterar el censo: %v", rerr)
	}
	if len(censo) == 0 {
		t.Fatal("el censo de columnas salió VACÍO; ESPERADO decenas. Un barrido sobre cero columnas " +
			"pasa siempre y no prueba nada")
	}
	return censo
}

// barreLaAgujaPorElCenso busca la aguja en TODA columna censada y devuelve las que la
// tienen. Va extraída de la función de test por gocyclo, no por gusto.
func barreLaAgujaPorElCenso(ctx context.Context, t *testing.T, db *sql.DB, censo []colDelCenso, aguja string) []string {
	t.Helper()
	var fugas []string
	for _, c := range censo {
		if !identificadorSano.MatchString(c.tabla) || !identificadorSano.MatchString(c.columna) {
			t.Fatalf("identificador inesperado en el catálogo (%q.%q): el barrido no lo interpola",
				c.tabla, c.columna)
		}
		var q string
		if c.tipo == "bytea" {
			// `::text` sobre un bytea da el hexadecimal («\x…»), que jamás contendría la
			// aguja en ASCII: haría pasar en verde justo la fuga que más importa —un
			// «cifrado» que dejó el claro dentro del blob—. Por eso los bytea van por
			// `position`, que compara bytes con bytes.
			// #nosec G201 -- lo único interpolado son IDENTIFICADORES leídos de
			// information_schema y ya validados contra identificadorSano (^[a-z_][a-z0-9_]*$);
			// la aguja viaja SIEMPRE como parámetro ($1), nunca concatenada.
			q = fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM public.%q WHERE position($1::bytea in %q) > 0)`,
				c.tabla, c.columna)
		} else {
			// #nosec G201 -- ídem: identificadores validados, aguja como parámetro.
			q = fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM public.%q WHERE %q::text LIKE '%%' || $1 || '%%')`,
				c.tabla, c.columna)
		}
		var hay bool
		if qerr := db.QueryRowContext(ctx, q, aguja).Scan(&hay); qerr != nil {
			t.Fatalf("barriendo %s.%s: %v", c.tabla, c.columna, qerr)
		}
		if hay {
			fugas = append(fugas, c.tabla+"."+c.columna+" ("+c.tipo+")")
		}
	}
	return fugas
}

// ---------------------------------------------------------------------------
// EL MISMO BARRIDO, A MANO, PARA UAT
// ---------------------------------------------------------------------------
//
// El test de arriba corre sobre una base de laboratorio con UNA aguja que nosotros
// plantamos. En UAT no hay aguja: hay literales reales de clientes reales, y el
// operador que quiera repetir el barrido tiene que elegir el suyo. Se deja escrito
// porque un test no llega a la base de producción y ésta es la comprobación que de
// verdad cierra REQ-10c en campo.
//
// PASO 1 — elegir una AGUJA de un mensaje reciente. Sale de descifrar, así que se hace
// desde la app (o desde el propio hilo que el operador acaba de escribir por WhatsApp
// en la prueba de campo). NUNCA se copia a un fichero ni a un ticket.
//
// PASO 2 — generar el barrido. Esta consulta NO barre: IMPRIME las consultas que hay
// que correr, una por columna candidata del esquema.
//
//	SELECT format(
//	         CASE WHEN c.data_type = 'bytea'
//	              THEN 'SELECT %L AS objetivo, count(*) FROM public.%I WHERE position(%L::bytea in %I) > 0;'
//	              ELSE 'SELECT %L AS objetivo, count(*) FROM public.%I WHERE %I::text LIKE ''%%'' || %L || ''%%'';'
//	         END,
//	         c.table_name || '.' || c.column_name, c.table_name,
//	         CASE WHEN c.data_type = 'bytea' THEN :aguja ELSE c.column_name END,
//	         CASE WHEN c.data_type = 'bytea' THEN c.column_name ELSE :aguja END)
//	  FROM information_schema.columns c
//	  JOIN information_schema.tables  t
//	    ON t.table_schema = c.table_schema AND t.table_name = c.table_name
//	 WHERE c.table_schema = 'public'
//	   AND t.table_type   = 'BASE TABLE'
//	   AND c.data_type IN ('text','character varying','character','json','jsonb','bytea')
//	 ORDER BY c.table_name, c.column_name;
//
// PASO 3 — ejecutar la salida del paso 2. SALIDA ESPERADA: todos los `count` a 0.
//
// PASO 4 — el contra-chequeo, que es la mitad que nadie hace y sin la cual un cero no
// significa nada (podría ser que la aguja estuviera mal copiada):
//
//	SELECT count(*) FROM public.conversation_event_messages WHERE body_enc IS NOT NULL;
//	-- esperado: > 0. Si sale 0, no hay hilo escrito y el barrido pasó por vacío.
//
// ⚠️ El paso 2 usa `:aguja` como marcador de psql (`\set aguja 'texto'`). Escribirlo
// así —y no con el literal pegado— es lo que evita que el texto del cliente quede en
// el historial del shell.
