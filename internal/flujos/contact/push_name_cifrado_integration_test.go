package contact_test

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Integración del sobre de `contacts.push_name` (Plan 046 · T4.2): las DOS reglas de
// escritura que gobiernan el tráfico vivo (MD-046.5). Mismo gate WAPP_TEST_DB_DSN que
// el resto del paquete.
//
// 🔧 ESTE FICHERO ERA backfill_push_name_integration_test.go Y PERDIÓ TRES TESTS EN
// T5.4, que es lo primero que hay que saber al leerlo. Se fueron con la migración
// 0070, que RETIRÓ la columna en claro `push_name` y con ella el backfill de arranque
// que la vaciaba: los dos tests del backfill y el de la fila legacy con tráfico
// probaban el paso de claro a cifrado, y ese paso ya no existe en ninguna base. No se
// borraron por estorbar: se borraron porque su sujeto desapareció. Lo que queda es lo
// único que sigue corriendo en producción — el sellado del nombre en el tráfico vivo.
//
// 🔴 POR QUÉ LOS DOS QUE QUEDAN NO PODÍAN QUEDARSE SIN TEST. Sus modos de fallo son
// SILENCIOSOS: nadie lee el push_name hoy, así que ninguno se manifiesta como un error
// de nadie.
//   - Cifrar la cadena vacía deja `push_name_enc` NO NULO con el sobre de un no-valor,
//     y a partir de ahí el nombre REAL que llegue después ya no puede entrar nunca.
//   - Escribir el sobre solo en el INSERT deja sin nombre, para siempre, a todo
//     contacto creado antes de que WhatsApp reportara el suyo.
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
// Ya no hace falta ninguna: estos tests no escriben en claro (no hay dónde) y no
// dependen de un conteo global. El barrido con t.Cleanup que este fichero tenía se
// fue con los tests del backfill, que eran los únicos que sembraban por SQL directo.

// sobrePushName es la fila de contacts vista COMO ESTADO DEL SOBRE: las tres piezas
// cifradas. Tuvo una cuarta lectura —la columna en claro— hasta que la 0070 la retiró
// (T5.4): mientras existió, «ya no hay claro Y sí hay sobre» eran las dos mitades del
// criterio (a) de T4.2, y hacían falta juntas porque vaciar sin cifrar también habría
// dado cero en la primera. Hoy la primera mitad la garantiza el esquema.
type sobrePushName struct {
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
		SELECT push_name_enc, push_name_dek, push_name_kek_id
		FROM public.contacts
		WHERE tenant_id = $1 AND contact_id = $2
	`, tenantID, contactID).Scan(&s.enc, &s.dek, &s.kekID)
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
