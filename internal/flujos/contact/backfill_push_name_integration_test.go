package contact_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Integración del sobre de `contacts.push_name` (Plan 046 · T4.2): el backfill de
// arranque que cifra las filas anteriores a la migración 0069 y VACÍA la columna en
// claro, y las dos reglas de escritura que gobiernan el tráfico vivo (MD-046.5).
// Mismo gate WAPP_TEST_DB_DSN que el resto del paquete.
//
// 🔴 POR QUÉ ESTO NO PODÍA QUEDARSE SIN TESTS. Los tres modos de fallo de T4.2 son
// SILENCIOSOS y, dos de ellos, IRREVERSIBLES: nadie lee el push_name hoy, así que
// ninguno se manifiesta como un error de nadie.
//   - Un centinela mal elegido re-cifra la tabla entera en cada arranque con un nonce
//     fresco. Escritura muda: no rompe nada visible y no deja ni una línea de log.
//   - Cifrar la cadena vacía deja `push_name_enc` NO NULO con el sobre de un no-valor,
//     y a partir de ahí el nombre REAL que llegue después ya no puede entrar nunca.
//   - Escribir el sobre sin vaciar el claro deja la fila fuera del centinela del
//     backfill con su PII intacta al lado: el criterio (a) no llega a cero JAMÁS.
//
// ── POR QUÉ LA IDA Y VUELTA SE HACE POR SQL CRUDO Y NO CON UN MÉTODO ──────────────
// Porque NO EXISTE, ni va a existir, un descifrador de push_name en producción, y eso
// es una DECISIÓN, no una limitación de este test. Se comprobó antes de escribir T4.2:
// `push_name` no aparece en ningún SELECT del repositorio y `contact.Contact.PushName`
// no se puebla en ningún sitio. Añadir un `Decrypt` solo para que el test pudiera
// llamarlo sería caer en el hallazgo que este repo ya tiene registrado —un paquete
// verde puede probar código que nadie ejecuta—: el método quedaría cubierto al 100 %
// sin que ninguna ruta real lo atravesara.
//
// Así que el test lee las tres piezas por SQL crudo y las abre con un
// crypto.FieldCipher construido AQUÍ sobre el MISMO KeyProvider que usa el resolver
// (newTestKeyProvider es determinista). Lo que se prueba con eso es exactamente la
// invariante que hará seguro al primer lector de verdad el día que aparezca: el sobre
// se escribe con el mismo cipher y el mismo keyring que ya abren `value_enc`, y el
// kek_id viaja EN LA FILA. El día que exista un lector, este test se reescribe para
// llamarlo — y no antes.
//
// ── HIGIENE DEL TABLERO ───────────────────────────────────────────────────────────
// El barrido de BackfillPushName es GLOBAL (no filtra por tenant: en producción tiene
// que alcanzar toda la base), así que sus contadores solo son deterministas con el
// tablero limpio. Misma solución que el molde de T4.1: se NULIFICA la columna en claro
// de las filas ajenas en vez de borrar filas, porque lo que contamina el conteo es el
// valor en claro, no la fila.
//
// 🔴 Y SE BARRE TAMBIÉN AL SALIR, NO SOLO AL ENTRAR. Estos tests son los ÚNICOS del
// árbol que escriben un push_name en claro, y lo hacen por SQL crudo. Un t.Fatalf entre
// la siembra y el final deja esa fila en claro hasta la siguiente corrida, y contra una
// base de desarrollo compartida el residuo pone rojo el criterio (a) —cero filas con
// push_name no nulo en TODA la tabla— de OTRO test que no tiene ningún bug. Por eso la
// limpieza se registra con t.Cleanup: corre pase lo que pase, incluido el camino de
// fallo. Ver tableroSinPushNamesEnClaro.

// nombresDePrueba son literales del TEST, no datos de nadie: por eso pueden aparecer
// en un mensaje de fallo. Un valor LEÍDO DE LA BASE no puede, nunca, ni siquiera en el
// camino de error: en los t.Fatalf de abajo se imprimen longitudes, booleanos e
// identificadores opacos, jamás el nombre que salió de una fila.
var nombresDePrueba = []string{"Ana", "Beto", "Carla", "Diego", "Elena"}

// sobrePushName es la fila de contacts vista COMO ESTADO DEL SOBRE: la columna en
// claro más las tres piezas cifradas. Es lo que hace falta para afirmar las dos
// mitades del criterio (a) —ya no hay claro Y sí hay sobre—, que juntas descartan la
// pérdida de datos silenciosa (vaciar sin cifrar también daría cero en la primera).
type sobrePushName struct {
	enClaro  sql.NullString
	enc, dek []byte
	kekID    sql.NullString
}

// tresPiezasNulas reporta si el sobre está ENTERO sin escribir. Es la invariante «las
// tres o ninguna» de la 0069 leída por su lado vacío.
func (s sobrePushName) tresPiezasNulas() bool {
	return len(s.enc) == 0 && len(s.dek) == 0 && !s.kekID.Valid
}

// tresPiezasPobladas reporta si el sobre está ENTERO escrito. La invariante no tiene
// CHECK que la vigile (0069:240-243), así que la vigilan estos tests.
func (s sobrePushName) tresPiezasPobladas() bool {
	return len(s.enc) > 0 && len(s.dek) > 0 && s.kekID.Valid
}

// resolverYCipherDePrueba devuelve el resolver Y el cipher construidos sobre EL MISMO
// KeyProvider. El cipher es lo que permite la ida y vuelta sin método de repositorio
// (ver la cabecera): es el mismo objeto que el resolver usa para cerrar el sobre, así
// que si abre lo que el resolver escribió, la invariante del día del primer lector se
// cumple. Equivale a repoDePrueba en internal/gateway/fleet/integration_test.go.
func resolverYCipherDePrueba(t *testing.T, db *sql.DB) (*contact.PostgresResolver, *crypto.FieldCipher) {
	t.Helper()
	kp := newTestKeyProvider(t)
	cipher := crypto.NewFieldCipher(kp)
	return contact.NewPostgresResolver(db, cipher, kp), cipher
}

// leerSobrePushName lee el estado del sobre de un contacto POR SQL CRUDO. Es la única
// vía: no hay lector de push_name en el repositorio y no se añade uno para el test.
// Se usa QueryRow porque todos los contactos de esta suite tienen UNA sola ref (un
// contacto con dos refs tiene dos filas y la consulta sería ambigua).
func leerSobrePushName(ctx context.Context, t *testing.T, db *sql.DB, tenantID, contactID string) sobrePushName {
	t.Helper()
	var s sobrePushName
	err := db.QueryRowContext(ctx, `
		SELECT push_name, push_name_enc, push_name_dek, push_name_kek_id
		FROM public.contacts
		WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, contactID).Scan(&s.enClaro, &s.enc, &s.dek, &s.kekID)
	if err != nil {
		t.Fatalf("leer el sobre del contacto %s: %v", contactID, err)
	}
	return s
}

// abrirPushName hace la IDA Y VUELTA: descifra las tres piezas leídas de la fila con
// el kek_id que viaja EN ESA FILA (no con el current), que es lo que mantiene legible
// una base a medio rotar (design.md §6, §10.F).
//
// El error NO lleva el valor: solo el hecho de que no se pudo abrir y las longitudes.
func abrirPushName(t *testing.T, cipher *crypto.FieldCipher, s sobrePushName, contactID string) string {
	t.Helper()
	if !s.tresPiezasPobladas() {
		t.Fatalf("contacto %s: el sobre no está entero (enc=%dB dek=%dB kek=%v): no hay nada que abrir",
			contactID, len(s.enc), len(s.dek), s.kekID.Valid)
	}
	claro, err := cipher.Decrypt(s.enc, s.dek, s.kekID.String)
	if err != nil {
		t.Fatalf("contacto %s: el sobre NO abre con el mismo cipher y el mismo keyring que lo cerró "+
			"(enc=%dB dek=%dB): %v — si esto falla, el nombre está perdido, porque no hay otra copia",
			contactID, len(s.enc), len(s.dek), err)
	}
	return claro
}

// sembrarPushNameEnClaro deja la fila TAL Y COMO la dejó el mundo anterior a la 0069:
// el nombre en la columna `push_name` y las tres piezas del sobre a NULL. No se puede
// sembrar desde Go —los dos INSERT y el UPDATE ya cifran, que es justo lo que se está
// probando—, así que va por SQL directo: es el único sitio del árbol de tests que
// escribe un push_name en claro, y existe para poder demostrar que el backfill lo
// retira.
//
// Las tres piezas se resetean a NULL además del claro: sin eso, una fila que ya
// tuviera sobre no casaría el centinela y el caso probaría cero.
func sembrarPushNameEnClaro(ctx context.Context, t *testing.T, db *sql.DB, tenantID, contactID, enClaro string) {
	t.Helper()
	res, err := db.ExecContext(ctx, `
		UPDATE public.contacts
		   SET push_name        = $3,
		       push_name_enc    = NULL,
		       push_name_dek    = NULL,
		       push_name_kek_id = NULL
		 WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, contactID, enClaro)
	if err != nil {
		t.Fatalf("sembrar fila legacy del contacto %s: %v", contactID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("filas afectadas al sembrar el contacto %s: %v", contactID, err)
	}
	if n == 0 {
		t.Fatalf("sembrar el contacto %s no tocó ninguna fila: el caso probaría cero", contactID)
	}
}

// limpiarPushNamesEnClaro deja el tablero SIN un solo push_name en claro. El barrido de
// BackfillPushName es global, así que sin esto los contadores del Report no son
// deterministas: una fila en claro que dejó otro caso (o una corrida anterior contra la
// misma base) sumaría a Encrypted o a Emptied. Se NULIFICA la columna, no se borra la
// fila: la fila es estado de contactos de otro test y borrarla rompería su fusión.
//
// 🔴 NO LLEVA `AND push_name_enc IS NULL`, y esa ausencia es deliberada. Con el filtro
// solo alcanzaba las filas «pendientes» —claro sí, sobre no— y dejaba intacto el estado
// «claro Y sobre a la vez». Ese estado es imposible con el código de HOY (el UPDATE de
// resolveExisting vacía el claro en el mismo SET, repository_postgres.go:322), pero es
// EXACTAMENTE el residuo que dejaría un binario anterior a esa enmienda sobre la base
// de desarrollo. Una limpieza que no lo alcanza deja el criterio (a) rojo por una razón
// que no es un bug de hoy, y el diagnóstico de eso cuesta una tarde. Por eso el helper
// ya no se llama «pendientes»: barre el claro venga como venga.
func limpiarPushNamesEnClaro(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		UPDATE public.contacts SET push_name = NULL WHERE push_name IS NOT NULL
	`); err != nil {
		t.Fatalf("limpiar push_names en claro: %v", err)
	}
}

// tableroSinPushNamesEnClaro barre el claro AHORA y registra el mismo barrido para el
// final del test. Lo usan todos los casos que siembran filas en claro por SQL crudo.
//
// El t.Cleanup construye su PROPIO contexto a propósito: los tests de este fichero
// llevan un ctx con deadline y un `defer cancel()`, y los cleanups corren DESPUÉS de
// los defer — con el ctx del test la limpieza de salida moriría cancelada justo en el
// camino que más la necesita, el del fallo.
func tableroSinPushNamesEnClaro(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	limpiarPushNamesEnClaro(ctx, t, db)
	//nolint:contextcheck // context.Background() a propósito: el cleanup corre DESPUÉS
	// de los `defer cancel()` del test, así que con el ctx del test la limpieza de
	// salida moriría cancelada justo en el camino que más la necesita, el del fallo.
	t.Cleanup(func() {
		limpiarPushNamesEnClaro(context.Background(), t, db)
	})
}

// contarPushNameEnClaro es el criterio (a) de T4.2 LITERAL, tal cual lo escribió la
// verificación (V3) de la migración 0069: sin filtrar por tenant y sin excluir la
// cadena vacía. Que no lleve filtro es deliberado —el criterio habla de la TABLA— y
// por eso la suite de integración se corre serializada (`go test -race -p 1`, ver la
// cabecera de rekey_integration_test.go).
func contarPushNameEnClaro(ctx context.Context, t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.contacts WHERE push_name IS NOT NULL`).Scan(&n); err != nil {
		t.Fatalf("contar push_name en claro: %v", err)
	}
	return n
}

// sembrarContactoConNombreLegacy crea un contacto por la vía normal SIN nombre (las
// tres piezas nacen NULL) y después le escribe el nombre en claro por SQL: el estado
// exacto de una fila anterior a la 0069. Devuelve su contact_id.
func sembrarContactoConNombreLegacy(
	ctx context.Context, t *testing.T, db *sql.DB,
	r *contact.PostgresResolver, tenantID, telefono, nombre string,
) string {
	t.Helper()
	cid, err := r.Resolve(ctx, tenantID, []contact.Ref{mustRef(t, contact.KindPhoneE164, telefono)}, "")
	if err != nil {
		t.Fatalf("sembrar contacto (%s): %v", telefono, err)
	}
	sembrarPushNameEnClaro(ctx, t, db, tenantID, cid, nombre)
	return cid
}

// TestIntegration_BackfillPushName_CifraVaciaElClaroYEsIdempotente cubre el camino
// feliz completo del criterio (a) —filas EN CLARO ⇒ cifradas, con `push_name` a NULL,
// en VARIOS lotes— más la ida y vuelta y el segundo arranque como no-op PERFECTO.
//
// ⏱️ El ctx lleva DEADLINE a propósito. Si el barrido perdiera el cursor, el bucle de
// BackfillPushName no volvería nunca: sin deadline el test no falla, se CUELGA, y una
// suite colgada se diagnostica mucho peor que una roja. Con él, el bucle infinito se
// manifiesta como un error de contexto en la primera aserción.
//
// 💥 MUTACIONES QUE LO PONEN ROJO, una por aserción:
//   - quitar `push_name = NULL` del SET de backfillPushNameUpdate
//     (backfill_push_name.go:140) ⇒ las filas quedan cifradas Y en claro a la vez: la
//     fase 1 falla y el conteo global del criterio (a) da 5 en vez de 0. Sin esa
//     aserción el fallo sería PERMANENTE y mudo: la fila deja de casar el centinela y
//     no la vuelve a mirar nadie.
//   - cambiar el centinela `push_name_enc IS NULL` del SELECT
//     (backfill_push_name.go:112) por cualquier cosa que vuelva a casar una fila ya
//     cifrada ⇒ la 2ª pasada re-cifra con nonce fresco y la fase 2 caza que
//     `push_name_enc` cambió byte a byte. Esa comparación es la ÚNICA huella que deja
//     una escritura muda: el dato leído seguiría siendo el mismo.
//   - no avanzar el cursor entre lotes (devolver siempre el cursor de entrada en
//     backfillPushNameBatch, backfill_push_name.go:372) ⇒ con cinco filas y batch=2 o
//     bien el conteo no llega a 5, o bien el barrido no termina y salta el deadline.
//   - romper la pareja cipher/KeyProvider de pushNameEnvelope (repository_postgres.go:224)
//     ⇒ la ida y vuelta de la fase 1 no abre el sobre.
func TestIntegration_BackfillPushName_CifraVaciaElClaroYEsIdempotente(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tenantID := seedTenant(t, db)
	repo, cipher := resolverYCipherDePrueba(t, db)
	tableroSinPushNamesEnClaro(ctx, t, db)

	ids := make([]string, 0, len(nombresDePrueba))
	for i, nombre := range nombresDePrueba {
		ids = append(ids, sembrarContactoConNombreLegacy(
			ctx, t, db, repo, tenantID, fmt.Sprintf("57300462%04d", i), nombre))
	}

	// batch=2 con CINCO filas ⇒ TRES lotes (2, 2, 1) y una cuarta vuelta vacía. Un
	// backfill que solo procesara el primer lote pasaría con el batch por defecto
	// (500) y fallaría aquí.
	rep, err := repo.BackfillPushName(ctx, 2)
	if err != nil {
		t.Fatalf("BackfillPushName (1ª pasada): %v "+
			"(si es un deadline de contexto, el barrido NO termina: el cursor no avanza y el arranque "+
			"del servidor se quedaría colgado en bucle)", err)
	}
	if rep.Encrypted != len(ids) {
		t.Fatalf("1ª pasada: Encrypted = %d, want %d (cinco filas en claro, batch=2 ⇒ tres lotes; "+
			"si sale 2, el cursor no avanza entre lotes)", rep.Encrypted, len(ids))
	}
	if rep.Emptied != 0 {
		t.Fatalf("1ª pasada: Emptied = %d, want 0 (ninguna de las cinco tiene la cadena vacía; "+
			"si sale > 0, el desenlace de vaciado se está tragando filas CON nombre y ese dato ya no existe)",
			rep.Emptied)
	}

	fasePushNameCifradoSinClaroYAbre(ctx, t, db, cipher, tenantID, ids)
	fasePushNameSegundaPasadaEsNoOp(ctx, t, db, repo, tenantID, ids[0])

	if n := contarPushNameEnClaro(ctx, t, db); n != 0 {
		t.Fatalf("criterio (a) de T4.2 INCUMPLIDO: quedan %d filas con push_name no nulo en toda la tabla, want 0 "+
			"(esta es la consulta literal de la verificación V3 de la migración 0069)", n)
	}
}

// TestIntegration_BackfillPushName_LaCadenaVaciaSeVaciaSinCifrarse cubre el segundo
// desenlace del backfill, que es un ÉXITO y no un fallo: una fila cuyo `push_name` es
// la cadena vacía se nulifica SIN escribir sobre alguno.
//
// Desde Go esa fila es inalcanzable hoy (nullStr manda la cadena vacía a NULL en los
// dos INSERT y el UPDATE está guardado por un nombre no vacío), así que se siembra por
// SQL crudo. El manejo es defensivo —SQL escrito a mano, un binario antiguo— y en UAT
// hay CERO filas así.
//
// 💥 MUTACIONES QUE LO PONEN ROJO:
//   - hacer que la rama `row.pushName == ""` (backfill_push_name.go:331) caiga en el
//     UPDATE con sobre en vez de en backfillPushNameEmpty ⇒ las tres piezas quedan
//     pobladas y la SEGUNDA mitad de este test falla. Esa mitad es la importante: con
//     `push_name_enc` no nulo, el centinela de MD-046.5 no vuelve a casar en esa fila
//     NUNCA, así que el nombre real que llegue después se pierde para siempre.
//   - copiar del molde de T4.1 el filtro que descarta la cadena vacía en el SELECT
//     (allí sí lo lleva) ⇒ Emptied = 0 y la fila conserva su residuo: aquí NO hay
//     latido que la sanee después, ni INSERT ni UPDATE la tocarían jamás.
func TestIntegration_BackfillPushName_LaCadenaVaciaSeVaciaSinCifrarse(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tenantID := seedTenant(t, db)
	repo, _ := resolverYCipherDePrueba(t, db)
	tableroSinPushNamesEnClaro(ctx, t, db)

	// La cadena vacía se siembra por SQL crudo: desde Go es inalcanzable.
	cid := sembrarContactoConNombreLegacy(ctx, t, db, repo, tenantID, "573004630000", "")

	rep, err := repo.BackfillPushName(ctx, 2)
	if err != nil {
		t.Fatalf("BackfillPushName con una fila de nombre vacío: %v", err)
	}
	if rep.Emptied != 1 {
		t.Fatalf("Emptied = %d, want 1: la fila con nombre vacío tiene que VACIARSE, no ignorarse "+
			"(aquí no hay latido que la sanee después: lo que el backfill no vacíe, no lo vacía nadie jamás)",
			rep.Emptied)
	}
	if rep.Encrypted != 0 {
		t.Fatalf("Encrypted = %d, want 0: el nombre vacío no se cifra", rep.Encrypted)
	}

	s := leerSobrePushName(ctx, t, db, tenantID, cid)
	if s.enClaro.Valid {
		t.Fatalf("contacto %s: la columna en claro sigue poblada (longitud %d) tras el vaciado: "+
			"el criterio (a) cuenta filas con push_name NO NULO, así que esa fila lo incumple sola",
			cid, len(s.enClaro.String))
	}
	if !s.tresPiezasNulas() {
		t.Fatalf("contacto %s: el backfill CIFRÓ el nombre vacío (enc=%dB dek=%dB kek=%v) y las tres "+
			"piezas debían seguir a NULL. Con push_name_enc no nulo, el centinela de MD-046.5 NO vuelve "+
			"a casar en esta fila: el nombre REAL que llegue después se pierde PARA SIEMPRE, que es "+
			"exactamente lo que esa decisión existe para impedir",
			cid, len(s.enc), len(s.dek), s.kekID.Valid)
	}
}

// TestIntegration_PushName_ElNombreQueLlegaTardeSeGuarda clava la verdad de campo que
// motivó MD-046.5: «a veces el nombre no llega en los primeros eventos, llega
// posterior». Un contacto se crea sin nombre (las tres piezas NULL) y un entrante
// POSTERIOR trae el nombre: tiene que sellarse entonces.
//
// 💥 MUTACIÓN QUE LO PONE ROJO: mover la escritura del sobre al INSERT solamente —o
// sea, borrar el bloque `if pushName != ""` de resolveExisting
// (repository_postgres.go:282-327)— ⇒ el sobre se queda NULL para siempre en todo
// contacto que se creara antes de que WhatsApp reportara su nombre, que según la
// verdad de campo son muchos. Nada más lo detectaría: no hay lector que se queje.
func TestIntegration_PushName_ElNombreQueLlegaTardeSeGuarda(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tenantID := seedTenant(t, db)
	repo, cipher := resolverYCipherDePrueba(t, db)
	ref := mustRef(t, contact.KindPhoneE164, "573004640000")

	// Primer entrante: WhatsApp todavía no reporta el nombre.
	cid, err := repo.Resolve(ctx, tenantID, []contact.Ref{ref}, "")
	if err != nil {
		t.Fatalf("Resolve sin nombre: %v", err)
	}
	if s := leerSobrePushName(ctx, t, db, tenantID, cid); !s.tresPiezasNulas() {
		t.Fatalf("contacto %s: sin nombre, las tres piezas tienen que nacer NULL (enc=%dB dek=%dB kek=%v). "+
			"Un sobre de la cadena vacía aquí bloquearía el nombre real para siempre",
			cid, len(s.enc), len(s.dek), s.kekID.Valid)
	}

	// Entrante POSTERIOR: ahora sí trae el nombre.
	const tardio = "Ana"
	cid2, err := repo.Resolve(ctx, tenantID, []contact.Ref{ref}, tardio)
	if err != nil {
		t.Fatalf("Resolve con nombre tardío: %v", err)
	}
	if cid2 != cid {
		t.Fatalf("el segundo entrante creó otro contacto (%s vs %s): la dedup por value_bidx está rota", cid2, cid)
	}
	s := leerSobrePushName(ctx, t, db, tenantID, cid)
	if !s.tresPiezasPobladas() {
		t.Fatalf("contacto %s: el nombre que llegó TARDE no se guardó (enc=%dB dek=%dB kek=%v). "+
			"Es la verdad de campo de este sistema: si el sobre solo se escribe en el INSERT, "+
			"los contactos creados antes de que WhatsApp reporte el nombre se quedan sin él para siempre",
			cid, len(s.enc), len(s.dek), s.kekID.Valid)
	}
	if got := abrirPushName(t, cipher, s, cid); got != tardio {
		t.Fatalf("contacto %s: la ida y vuelta no devuelve el nombre tardío: se descifró algo de %d bytes, "+
			"y el test sembró %q (%d bytes)", cid, len(got), tardio, len(tardio))
	}
}

// TestIntegration_PushName_GanaElPrimerNombreNoVacio clava el PRECIO ACEPTADO de
// MD-046.5: con el centinela `push_name_enc IS NULL`, el primer nombre no vacío se
// queda. Si el cliente se cambia el nombre en WhatsApp, la fila conserva el primero.
//
// 🔴 ESTE TEST EXISTE PARA QUE NADIE LO «ARREGLE» SIN LEER LA DECISIÓN. Parece un bug
// y no lo es: comparar por contenido es IMPOSIBLE aquí (dos cifrados del mismo texto
// nunca son iguales, con DEK y nonce frescos), así que una guarda por valor casaría
// SIEMPRE y tomaría row-locks en CADA entrante de la ráfaga de historial, reabriendo
// el deadlock 40P01 que protege deadlock_integration_test.go. El nombre es un dato de
// negocio auxiliar que hoy no lee nadie; el deadlock tumbaba el procesado de entrantes.
//
// De paso es la evidencia del criterio (b): tras el primer nombre real el UPDATE deja
// de casar, o sea CERO row-locks nuevos por entrante.
//
// 💥 MUTACIÓN QUE LO PONE ROJO: cambiar el centinela de resolveExisting
// (repository_postgres.go:323) por `push_name_enc IS DISTINCT FROM $1`, que es lo que
// había antes de T4.2 sobre la columna en claro ⇒ el sobre se reescribe en cada
// entrante, `bytes.Equal` falla, y con él vuelven los row-locks masivos.
func TestIntegration_PushName_GanaElPrimerNombreNoVacio(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tenantID := seedTenant(t, db)
	repo, cipher := resolverYCipherDePrueba(t, db)
	ref := mustRef(t, contact.KindPhoneE164, "573004650000")

	const primero, segundo = "Ana", "Beto"
	cid, err := repo.Resolve(ctx, tenantID, []contact.Ref{ref}, primero)
	if err != nil {
		t.Fatalf("Resolve con el primer nombre: %v", err)
	}
	antes := leerSobrePushName(ctx, t, db, tenantID, cid)
	if got := abrirPushName(t, cipher, antes, cid); got != primero {
		t.Fatalf("contacto %s: precondición rota, el sobre inicial descifra a algo de %d bytes y "+
			"el test sembró %q (%d bytes)", cid, len(got), primero, len(primero))
	}

	// El cliente se cambia el nombre en WhatsApp: llega un entrante con OTRO nombre.
	if _, err := repo.Resolve(ctx, tenantID, []contact.Ref{ref}, segundo); err != nil {
		t.Fatalf("Resolve con el segundo nombre: %v", err)
	}
	despues := leerSobrePushName(ctx, t, db, tenantID, cid)
	if !bytes.Equal(antes.enc, despues.enc) {
		t.Fatalf("contacto %s: el sobre CAMBIÓ con el segundo nombre (%dB → %dB). Esto no es una mejora: "+
			"el centinela push_name_enc IS NULL es lo que hace que el UPDATE deje de casar tras el primer "+
			"nombre; si vuelve a casar, cada entrante de la ráfaga de historial toma row-locks sobre TODAS "+
			"las filas del contacto y reaparece el deadlock 40P01 del Plan 026 (MD-046.5, precio aceptado)",
			cid, len(antes.enc), len(despues.enc))
	}
}

// TestIntegration_PushName_FilaLegacyConTraficoQuedaLimpia es la red de una ENMIENDA
// deliberada al SQL literal de MD-046.5: el `push_name = NULL` que resolveExisting
// añade a su SET (repository_postgres.go:322).
//
// 🔴 QUÉ PASA SIN ESA ENMIENDA, ENTERO. Una fila LEGACY (nombre en claro, sobre NULL)
// a la que el tráfico vivo le llegue ANTES de que el backfill la vea se queda con el
// sobre escrito Y el nombre en claro al lado. Desde ese instante deja de casar el
// centinela `push_name IS NOT NULL AND push_name_enc IS NULL` del backfill, así que
// NADIE la vuelve a mirar JAMÁS: su PII se queda en claro para siempre y el criterio
// (a) —cero filas con push_name no nulo— no puede llegar a cero nunca. La ventana es
// estrecha (el backfill corre antes de que ESTE proceso acepte tráfico) pero NO es
// cero: con dos réplicas o un despliegue con solape hay otra instancia sirviendo
// mientras esta arranca.
//
// Por eso aquí NO se corre el backfill: el caso es justamente el orden inverso.
//
// 💥 MUTACIÓN QUE LO PONE ROJO: quitar `push_name = NULL` del SET del UPDATE de
// resolveExisting (repository_postgres.go:322), o sea volver al SQL literal de
// MD-046.5 ⇒ la fila queda con sobre Y claro, y la segunda aserción falla.
func TestIntegration_PushName_FilaLegacyConTraficoQuedaLimpia(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tenantID := seedTenant(t, db)
	repo, cipher := resolverYCipherDePrueba(t, db)
	// Este caso NO corre el backfill, así que no le importan los contadores globales:
	// el barrido lo registra por el CLEANUP. Su siembra en claro es la que se quedaría
	// contaminando el criterio (a) de los otros casos si alguna aserción de abajo aborta
	// antes de que el tráfico vivo vacíe el claro.
	tableroSinPushNamesEnClaro(ctx, t, db)
	const telefono = "573004660000"
	cid := sembrarContactoConNombreLegacy(ctx, t, db, repo, tenantID, telefono, "Carla")

	// Precondición: estado pre-migración exacto (claro poblado, sobre entero a NULL).
	legacy := leerSobrePushName(ctx, t, db, tenantID, cid)
	if !legacy.enClaro.Valid || !legacy.tresPiezasNulas() {
		t.Fatalf("contacto %s: la siembra no dejó el estado legacy (claro=%v enc=%dB dek=%dB kek=%v)",
			cid, legacy.enClaro.Valid, len(legacy.enc), len(legacy.dek), legacy.kekID.Valid)
	}

	// Llega tráfico vivo ANTES del backfill. NO se corre BackfillPushName a propósito.
	const nuevo = "Diego"
	if _, err := repo.Resolve(ctx, tenantID,
		[]contact.Ref{mustRef(t, contact.KindPhoneE164, telefono)}, nuevo); err != nil {
		t.Fatalf("Resolve sobre la fila legacy: %v", err)
	}

	s := leerSobrePushName(ctx, t, db, tenantID, cid)
	if !s.tresPiezasPobladas() {
		t.Fatalf("contacto %s: el tráfico vivo no selló el sobre de la fila legacy (enc=%dB dek=%dB kek=%v)",
			cid, len(s.enc), len(s.dek), s.kekID.Valid)
	}
	if s.enClaro.Valid {
		t.Fatalf("contacto %s: la fila quedó con sobre Y con el nombre EN CLARO al lado (longitud %d). "+
			"A partir de aquí ya no casa el centinela del backfill (push_name_enc IS NULL), así que NADIE "+
			"la vuelve a mirar y esa PII se queda en claro PARA SIEMPRE, con el criterio (a) sin poder "+
			"llegar a cero nunca. El `push_name = NULL` del SET de resolveExisting es la enmienda que lo "+
			"impide, y este test es su única red",
			cid, len(s.enClaro.String))
	}
	if got := abrirPushName(t, cipher, s, cid); got != nuevo {
		t.Fatalf("contacto %s: el sobre de la fila legacy descifra a algo de %d bytes y el tráfico escribió "+
			"%q (%d bytes)", cid, len(got), nuevo, len(nuevo))
	}
}

// fasePushNameCifradoSinClaroYAbre comprueba, fila a fila y por SQL directo, las DOS
// mitades del criterio (a) —ya no hay claro Y sí hay sobre— más la ida y vuelta.
// La segunda mitad no es redundante: vaciar sin cifrar también daría cero en la
// primera, y sería una pérdida de datos silenciosa (es la misma pareja de consultas
// V3 de la migración 0069). La tercera tampoco: un sobre que no abre es lo mismo que
// no tener el dato, solo que ocupa sitio.
//
// Extraída y NOMBRADA por gocyclo (umbral 15, que aplica también a los tests).
func fasePushNameCifradoSinClaroYAbre(
	ctx context.Context, t *testing.T, db *sql.DB, cipher *crypto.FieldCipher, tenantID string, ids []string,
) {
	t.Helper()
	for i, cid := range ids {
		s := leerSobrePushName(ctx, t, db, tenantID, cid)
		if s.enClaro.Valid {
			t.Fatalf("contacto %s: push_name sigue en claro tras el backfill (longitud %d): "+
				"criterio (a) incumplido, y la fila ya no casa el centinela, así que nadie la volverá a mirar",
				cid, len(s.enClaro.String))
		}
		if !s.tresPiezasPobladas() {
			t.Fatalf("contacto %s: el backfill vació el claro pero no dejó el sobre ENTERO "+
				"(enc=%dB dek=%dB kek=%v): eso es PERDER el dato, no protegerlo",
				cid, len(s.enc), len(s.dek), s.kekID.Valid)
		}
		if got := abrirPushName(t, cipher, s, cid); got != nombresDePrueba[i] {
			t.Fatalf("contacto %s: la ida y vuelta no devuelve el nombre original: se descifró algo de "+
				"%d bytes y el test sembró %q (%d bytes)",
				cid, len(got), nombresDePrueba[i], len(nombresDePrueba[i]))
		}
	}
}

// fasePushNameSegundaPasadaEsNoOp: el arranque siguiente no toca NADA. Se afirma por
// las dos vías, y la segunda es la que importa — un contador a 0 lo daría igual un
// backfill que reescribiera las filas y no supiera contarlas; el sobre INTACTO byte a
// byte no lo da nada más que no haber escrito.
func fasePushNameSegundaPasadaEsNoOp(
	ctx context.Context, t *testing.T, db *sql.DB,
	repo *contact.PostgresResolver, tenantID, contactID string,
) {
	t.Helper()
	antes := leerSobrePushName(ctx, t, db, tenantID, contactID)
	rep, err := repo.BackfillPushName(ctx, 2)
	if err != nil {
		t.Fatalf("BackfillPushName (2ª pasada): %v", err)
	}
	if rep.Encrypted != 0 || rep.Emptied != 0 {
		t.Fatalf("2ª pasada: Encrypted=%d Emptied=%d, want 0 y 0 (reejecutar tiene que ser un no-op: "+
			"esto corre en CADA arranque del servidor)", rep.Encrypted, rep.Emptied)
	}
	despues := leerSobrePushName(ctx, t, db, tenantID, contactID)
	if !bytes.Equal(antes.enc, despues.enc) {
		t.Fatalf("contacto %s: el sobre cambió en la 2ª pasada (%dB → %dB): el backfill RE-CIFRÓ una fila "+
			"ya cifrada. Como el nonce es fresco por escritura y nadie lee este campo, TODO lo demás "+
			"seguiría funcionando y la tabla se reescribiría entera en cada boot sin una sola línea que "+
			"lo delate. Esta comparación es la única huella de esa escritura muda",
			contactID, len(antes.enc), len(despues.enc))
	}
}
