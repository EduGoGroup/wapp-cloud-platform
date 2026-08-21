package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Tests de integración de PostgresSelfNumbers (Plan 020 · T2, consciente del perfil;
// Plan 046 · T4.1, por índice ciego): la exclusión de sesiones passive, el filtro de
// vida y la agregación POR NÚMERO viven todos en la QUERY, así que se verifican
// contra fleet_sessions real. Reutilizan openTestDB/seedTenant (mismo gate
// WAPP_TEST_DB_DSN que el resto de la integración: sin DSN se saltan en local,
// corren en CI/e2e).
//
// 🔒 Qué cambió en la T4.1 y qué NO. El componente pasó de devolver la LISTA de
// números en claro a responder un booleano por número, resuelto en SQL contra
// self_pn_bidx. Los casos de prueba y lo que afirman son los MISMOS —ninguna
// aserción se ha aflojado—; lo que cambia es cómo se formulan: donde antes se
// preguntaba "¿está en la lista?" ahora se pregunta directamente por el número.
// El único caso que no sobrevive LITERAL es el de deduplicación: con un booleano no
// hay repetición que contar. Se reformuló hacia la propiedad que lo motivaba —la
// decisión agrega sobre TODAS las filas del número, cruzando edges— y se REFORZÓ con
// el caso mixto, ver su comentario.

// selfNumbersKP construye el KeyProvider de prueba: keyring fijo e indexKey fija.
// La indexKey tiene que ser LA MISMA con la que se sembró el bidx (en producción lo
// garantiza que sea estable de por vida, §10.C); aquí basta con reutilizar este
// helper en el sembrado y en la consulta.
func selfNumbersKP(t *testing.T) crypto.KeyProvider {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: "test-kek-1:ERERERERERERERERERERERERERERERERERERERERERE=",
		CurrentID:  "test-kek-1",
		IndexB64:   "RERERERERERERERERERERERERERERERERERERERERES=",
	})
	if err != nil {
		t.Fatalf("KeyProvider de prueba: %v", err)
	}
	return kp
}

// bidxDe calcula el índice ciego de un número EMULANDO al escritor: normaliza con
// contact.Normalize y pasa el resultado a BlindIndex. Ese orden es el contrato de la
// T4.1 y por eso el helper lo encapsula: si un test sembrara el bidx sobre el número
// crudo, estaría probando otra cosa.
//
// 🔴 EMULA AL ESCRITOR, NO LO EJECUTA, y esa distinción es la que hace que esta suite
// NO pueda cerrar el criterio (c) de T4.1 por sí sola. Aquí el bidx lo fabrica el test
// y lo interroga el test: entre los dos extremos no pasa ni una línea de
// fleet.selfPnEnvelope. Se comprobó: quitar normalizeSelfPn del escritor real deja
// esta suite ENTERA en verde. La simetría escritor↔lector la custodia
// gateway/fleet/self_pn_cifrado_integration_test.go, que escribe con SetSelfPn de
// verdad y pregunta con IsSelfNumber de verdad. Lo que SÍ protege este fichero es la
// semántica de la QUERY (perfil, estado, agregación por número, aislamiento por
// tenant), que es para lo que nació.
func bidxDe(t *testing.T, kp crypto.KeyProvider, tenantID, numero string) string {
	t.Helper()
	norm, err := contact.Normalize(contact.KindPhoneE164, numero)
	if err != nil {
		// Sin el número crudo en el mensaje (higiene PII, design.md §8/§10.I).
		t.Fatalf("normalizar el número a sembrar: %v", err)
	}
	return kp.BlindIndex(tenantID, norm)
}

// seedFleetSessionStatePerfilPn siembra una fila de fleet_sessions con state, perfil
// e ÍNDICE CIEGO del número explícitos. El STATE pesa tanto como el perfil: la query
// separa la sesión RETIRADA (loggedout, no vuelve sin re-QR) de la meramente
// desconectada (offline, recuperable).
//
// 🔧 Hasta la 0064 este helper sembraba TAMBIÉN la columna `role`. Al retirarse, el
// sembrado nombra el único eje que existe. El perfil se escribe SIEMPRE explícito y
// nunca se deja al DEFAULT —que es pasivo—: dejarlo convertiría en pasiva a toda
// sesión que el caso quería activa.
//
// 🔒 Y desde la T4.1 no se siembra `self_pn` en claro: esa columna queda VACÍA. Solo
// se escribe self_pn_bidx, que es lo ÚNICO que lee el predicado. El triplete
// cifrado (self_pn_enc/dek/kek_id) es cosa de la capa de persistencia y no
// interviene aquí — precisamente porque esta guarda ya no descifra nada.
func seedFleetSessionStatePerfilPn(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, state, perfil, selfPnBidx string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO public.fleet_sessions
			(tenant_id, edge_id, session_id, state, profile, self_pn_bidx, last_connected_at, last_seen_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now(), now())
		ON CONFLICT (tenant_id, edge_id, session_id)
			DO UPDATE SET state = EXCLUDED.state,
			              profile = EXCLUDED.profile, self_pn_bidx = EXCLUDED.self_pn_bidx
	`, tenantID, edgeID, sessionID, state, perfil, selfPnBidx)
	if err != nil {
		t.Fatalf("sembrar fleet_sessions (state=%s profile=%s): %v", state, perfil, err)
	}
}

// seedFleetSessionPerfilPn siembra una sesión VIVA (online) con perfil y bidx dados.
func seedFleetSessionPerfilPn(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID, perfil, selfPnBidx string) {
	t.Helper()
	seedFleetSessionStatePerfilPn(t, db, tenantID, edgeID, sessionID, "online", perfil, selfPnBidx)
}

// esPropio pregunta al checker y falla el test si la consulta revienta: los casos
// afirman sobre el VEREDICTO, no sobre el error.
func esPropio(t *testing.T, c *runtime.PostgresSelfNumbers, tenantID, numero string) bool {
	t.Helper()
	norm, err := contact.Normalize(contact.KindPhoneE164, numero)
	if err != nil {
		t.Fatalf("normalizar el número de la consulta: %v", err)
	}
	propio, err := c.IsSelfNumber(context.Background(), tenantID, norm)
	if err != nil {
		t.Fatalf("IsSelfNumber: %v", err)
	}
	return propio
}

// Un número de una sesión ACTIVA sí cuenta como propio (bloquea el self-loop); el de
// una sesión PASSIVE se EXCLUYE (una passive nunca auto-responde ⇒ sin riesgo de
// loop, y así una sesión activa puede atender al número personal del mismo tenant).
func TestIntegration_PostgresSelfNumbers_ExcluyePassive(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	kp := selfNumbersKP(t)
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	suffix := time.Now().UnixNano()
	activaPn := fmt.Sprintf("57300%010d", suffix%1e10)
	passivePn := fmt.Sprintf("57301%010d", suffix%1e10)
	seedFleetSessionPerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-activa-%d", suffix),
		"active", bidxDe(t, kp, tenantID, activaPn))
	seedFleetSessionPerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-pas-%d", suffix),
		"passive", bidxDe(t, kp, tenantID, passivePn))

	if !esPropio(t, checker, tenantID, activaPn) {
		t.Fatal("el número de una sesión ACTIVA debe contar como propio y bloquear el self-loop")
	}
	if esPropio(t, checker, tenantID, passivePn) {
		t.Fatal("el número de una sesión passive NO debe contar como número propio")
	}
}

// Un tenant cuyas ÚNICAS sesiones con número son passive no bloquea NADA: la guarda
// anti-self-loop devuelve false para cualquier remitente, incluido el número passive
// del propio tenant.
func TestIntegration_PostgresSelfNumbers_SoloPassiveNoBloqueaNada(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	kp := selfNumbersKP(t)
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	suffix := time.Now().UnixNano()
	passivePn := fmt.Sprintf("57302%010d", suffix%1e10)
	seedFleetSessionPerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-pas-%d", suffix),
		"passive", bidxDe(t, kp, tenantID, passivePn))

	if esPropio(t, checker, tenantID, passivePn) {
		t.Fatal("un tenant solo-passive no debe bloquear su propio número")
	}
	// Y un número que no existe en absoluto tampoco: es el grupo VACÍO, el caso que
	// en SQL devuelve bool_or → NULL y que el código traduce a false.
	if esPropio(t, checker, tenantID, fmt.Sprintf("57399%010d", suffix%1e10)) {
		t.Fatal("un número que no está en fleet_sessions no puede ser propio (grupo vacío ⇒ NULL ⇒ false)")
	}
}

// Una sesión RETIRADA (loggedout) no aporta su número: es un zombi que no vuelve sin
// re-emparejar, así que no puede auto-responder y no puede cerrar un bucle. Muerde el
// filtro state <> 'loggedout': sin él, la fila fantasma bloquearía.
func TestIntegration_PostgresSelfNumbers_SesionRetiradaNoBloquea(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	kp := selfNumbersKP(t)
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	suffix := time.Now().UnixNano()
	zombiPn := fmt.Sprintf("57303%010d", suffix%1e10)
	seedFleetSessionStatePerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-zombi-%d", suffix),
		"loggedout", "active", bidxDe(t, kp, tenantID, zombiPn))

	if esPropio(t, checker, tenantID, zombiPn) {
		t.Fatal("una sesión loggedout NO debe aportar su número (no auto-responde ⇒ no hay bucle que cerrar)")
	}
}

// EL BUG QUE MOTIVÓ LA AGREGACIÓN POR NÚMERO: el número está en DOS filas —la sesión
// viva marcada passive y la fila muerta (loggedout) de un emparejamiento anterior,
// que quedó activa—. Marcar passive desde la consola tiene que surtir efecto: el
// número NO debe bloquear, o el bot nunca podrá atender al teléfono personal.
//
// Con el índice ciego esto se sostiene solo si el MISMO número da el MISMO bidx en
// ambas filas — que es justo lo que hace determinista al HMAC.
func TestIntegration_PostgresSelfNumbers_PassiveVivaConZombiActivoDelMismoNumero(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	kp := selfNumbersKP(t)
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	suffix := time.Now().UnixNano()
	pn := fmt.Sprintf("57304%010d", suffix%1e10)
	bidx := bidxDe(t, kp, tenantID, pn)
	// La sesión VIVA de ese número, ya marcada passive por el operador.
	seedFleetSessionStatePerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-viva-%d", suffix),
		"online", "passive", bidx)
	// El fantasma del emparejamiento anterior: MISMO número, retirado, aún activo.
	seedFleetSessionStatePerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-muerta-%d", suffix),
		"loggedout", "active", bidx)

	if esPropio(t, checker, tenantID, pn) {
		t.Fatal("una fila loggedout activa NO debe mantener bloqueado el número de una sesión viva passive " +
			"(el cambio de perfil quedaría sin efecto)")
	}
}

// ⚠️ NO-REGRESIÓN CONTRA LA "OPTIMIZACIÓN" EQUIVOCADA: una sesión activa en OFFLINE
// sigue bloqueando. offline es el stream CloudLink caído y RECUPERABLE —el socket de
// WhatsApp sigue vivo y la sesión auto-responde al reconectar, drenando el outbox—,
// así que su número sí puede cerrar un bucle. Este test falla si alguien estrecha el
// filtro a state = 'online'.
func TestIntegration_PostgresSelfNumbers_SesionOfflineSigueBloqueando(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	kp := selfNumbersKP(t)
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	suffix := time.Now().UnixNano()
	offlinePn := fmt.Sprintf("57305%010d", suffix%1e10)
	seedFleetSessionStatePerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-off-%d", suffix),
		"offline", "active", bidxDe(t, kp, tenantID, offlinePn))

	if !esPropio(t, checker, tenantID, offlinePn) {
		t.Fatal("una sesión activa OFFLINE (stream caído pero recuperable) SÍ debe seguir bloqueando su número " +
			"—filtrar por state='online' reabriría el bucle—")
	}
}

// La decisión es por NÚMERO, no por fila, y el número puede estar repartido entre
// EDGES. Antes esto se afirmaba contando repeticiones en la lista devuelta; con un
// booleano no hay repetición que contar, así que se afirma la propiedad de fondo —el
// agregado abarca TODAS las filas del número— y se refuerza con el caso MIXTO, que
// la lista no llegaba a distinguir: passive en un edge + activa en otro ⇒ BLOQUEA.
// Es la dirección correcta del fallo: si alguna sesión viva de ese número
// auto-responde, el bucle es posible.
func TestIntegration_PostgresSelfNumbers_AgregaEntreEdges(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	kp := selfNumbersKP(t)
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	suffix := time.Now().UnixNano()

	// (a) el mismo número activo en dos edges ⇒ bloquea (no-regresión del caso viejo).
	ambosActivos := fmt.Sprintf("57306%010d", suffix%1e10)
	bidxAmbos := bidxDe(t, kp, tenantID, ambosActivos)
	seedFleetSessionPerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-a-%d", suffix), "active", bidxAmbos)
	seedFleetSessionPerfilPn(t, db, tenantID, "edge-B", fmt.Sprintf("sess-b-%d", suffix), "active", bidxAmbos)
	if !esPropio(t, checker, tenantID, ambosActivos) {
		t.Fatal("el mismo número activo en dos edges debe bloquear")
	}

	// (b) mixto: passive en un edge, activa en otro ⇒ bloquea (bool_or sobre el grupo).
	mixto := fmt.Sprintf("57307%010d", suffix%1e10)
	bidxMixto := bidxDe(t, kp, tenantID, mixto)
	seedFleetSessionPerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-mix-p-%d", suffix), "passive", bidxMixto)
	seedFleetSessionPerfilPn(t, db, tenantID, "edge-B", fmt.Sprintf("sess-mix-a-%d", suffix), "active", bidxMixto)
	if !esPropio(t, checker, tenantID, mixto) {
		t.Fatal("un número con ALGUNA sesión viva no-pasiva debe bloquear, aunque otro edge lo tenga passive")
	}
}

// EL CASO QUE CAZA QUE EL *LLAMANTE* NO NORMALICE (Plan 046 · T4.1). El mismo número
// escrito con '+', espacios y guiones tiene que dar el MISMO índice ciego y por tanto
// el MISMO veredicto. Y la segunda mitad del test explica POR QUÉ hace falta el
// caso: BlindIndex NO normaliza —es un HMAC sobre los bytes que recibe—, así que la
// forma adornada SIN pasar por contact.Normalize produce un índice DISTINTO que no
// casaría con nada. El fallo sería mudo: la guarda dejaría de bloquear sin un solo
// error en el log.
//
// ⚠️ LO QUE ESTE CASO **NO** CAZA, dicho aquí porque su nombre invitaba a creer lo
// contrario: no caza que el ESCRITOR deje de normalizar. Los dos extremos —el sembrado
// (bidxDe) y la consulta (esPropio)— normalizan por su cuenta dentro de este fichero,
// así que la mutación «quitar normalizeSelfPn de fleet.selfPnEnvelope» lo deja verde.
// Esa mitad la cierra gateway/fleet/self_pn_cifrado_integration_test.go.
func TestIntegration_PostgresSelfNumbers_MismoNumeroDistintaGrafia(t *testing.T) {
	db := openTestDB(t)
	tenantID := seedTenant(t, db)
	kp := selfNumbersKP(t)
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	suffix := time.Now().UnixNano()
	canonico := fmt.Sprintf("57308%010d", suffix%1e10)
	// La misma cifra con adornos: '+', espacios, guiones y paréntesis.
	adornado := fmt.Sprintf("+%s (%s) %s-%s", canonico[:2], canonico[2:5], canonico[5:9], canonico[9:])

	seedFleetSessionPerfilPn(t, db, tenantID, "edge-A", fmt.Sprintf("sess-graf-%d", suffix),
		"active", bidxDe(t, kp, tenantID, canonico))

	if !esPropio(t, checker, tenantID, canonico) {
		t.Fatal("la forma canónica del número propio debe bloquear")
	}
	if !esPropio(t, checker, tenantID, adornado) {
		t.Fatal("el MISMO número con '+', espacios, guiones y paréntesis debe dar el mismo veredicto: " +
			"normalizar ANTES del índice ciego es lo que lo garantiza")
	}
	// La razón de ser del caso: sin normalizar, el índice sale distinto.
	if kp.BlindIndex(tenantID, adornado) == bidxDe(t, kp, tenantID, canonico) {
		t.Fatal("BlindIndex no normaliza (es un HMAC sobre bytes); si estos dos índices coincidieran, " +
			"este test habría dejado de proteger la normalización del llamante")
	}
}

// El índice ciego va SALADO con el tenant_id (BlindIndex escribe tenantID || 0x00 ||
// value), así que el mismo teléfono da índices distintos en tenants distintos. El
// aislamiento (INV-8) queda doblemente garantizado: por el WHERE tenant_id y por el
// propio índice. Este test muerde la segunda mitad.
func TestIntegration_PostgresSelfNumbers_AisladoPorTenant(t *testing.T) {
	db := openTestDB(t)
	tenantA := seedTenant(t, db)
	tenantB := seedTenant(t, db)
	if tenantA == tenantB {
		t.Skip("seedTenant devolvió el mismo tenant dos veces; el caso necesita dos distintos")
	}
	kp := selfNumbersKP(t)
	checker := runtime.NewPostgresSelfNumbers(db, kp)

	suffix := time.Now().UnixNano()
	pn := fmt.Sprintf("57309%010d", suffix%1e10)
	seedFleetSessionPerfilPn(t, db, tenantA, "edge-A", fmt.Sprintf("sess-iso-%d", suffix),
		"active", bidxDe(t, kp, tenantA, pn))

	if !esPropio(t, checker, tenantA, pn) {
		t.Fatal("el número propio del tenant A debe bloquear en A")
	}
	if esPropio(t, checker, tenantB, pn) {
		t.Fatal("el número propio del tenant A NO debe bloquear en el tenant B (INV-8)")
	}
	if kp.BlindIndex(tenantA, pn) == kp.BlindIndex(tenantB, pn) {
		t.Fatal("el índice ciego debe ir salado con el tenant_id: dos tenants no pueden compartir índice")
	}
}

// Sin KeyProvider no hay índice que calcular y la pregunta NO tiene respuesta: se
// devuelve ERROR, no un false mudo. Quien decide qué hacer con eso es isSelfLoop
// (omite la guarda y procesa), y esa política tiene que poder distinguirse de un
// "no es número propio" legítimo. No toca la BD, así que corre siempre.
func TestPostgresSelfNumbers_SinKeyProviderDevuelveError(t *testing.T) {
	_, err := runtime.NewPostgresSelfNumbers(nil, nil).
		IsSelfNumber(context.Background(), "tenant-x", "573001110000")
	if !errors.Is(err, runtime.ErrSelfNumbersSinKeyProvider) {
		t.Fatalf("sin KeyProvider debe devolver ErrSelfNumbersSinKeyProvider, devolvió %v", err)
	}
}
