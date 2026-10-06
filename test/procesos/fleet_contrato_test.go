//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet/fleethelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero corre la suite de contrato de fleet.Repository (fleethelpertest.ContratoRepository)
// contra fleet.PostgresRepository sobre una base clonada de la plantilla migrada (T3.30 = T9.24,
// sesión F3-05, hallazgo 30 de F3). Como contact_contrato_test.go, no es un proceso de caja negra:
// es la excepción de R9.4.d que decidió D-F1-8, y por eso solo importa la suite, el paquete del
// adaptador (su constructor) y crypto (sus argumentos).
//
// 🔴 DEL PAQUETE fleet SOLO SE NOMBRA NewPostgresRepository (candado ProcessImports, regla 3b), y
// el Montaje y los casos propios necesitan tres de sus tipos. Ninguno se nombra:
//   - fleet.Repository: el campo Montaje.Repository ya lo es; el fleetContractRig guarda el Montaje;
//   - fleet.TenantProfiles (lo que devuelve Montaje.Profiles) y fleet.HealthSnapshot (lo que recibe
//     SaveHealth): los infiere el compilador en fleetContractProfilesReader y fleetContractZeroHealth.
//
// Los casos propios del hallazgo 30 —lo que solo existe contra Postgres— están aquí
// (degraded_since) y en fleet_contrato_selfpn_test.go (el sobre del self_pn, su guarda y la
// rotación de KEK). Ese fichero no importa nada de internal/: todo le llega por el fleetContractRig.
//
// 🔴 HOMÓNIMO: la «DEK» y la KEK de este fichero son las del envelope de PII de negocio
// (internal/platform/crypto), NO la DEK del ADR-0007 que custodia el cliente. Todas se generan en
// el test y mueren con él.

const (
	// fleetContractTimeout acota cada sentencia de siembra y de observación: la base es local al
	// contenedor de la corrida.
	fleetContractTimeout = 15 * time.Second

	// fleetContractKeyID es el key_id de la única KEK del keyring de un Montaje de la suite.
	fleetContractKeyID = "1"
)

// fleetContractCases cuenta las bases pedidas en esta corrida: ContratoRepository llama a nuevo una
// vez por caso (42, en serie) y cada uno necesita una base con nombre propio mientras la anterior
// siga viva. Los casos propios toman la suya del mismo contador.
var fleetContractCases atomic.Int64

// fleetContractFrozenAt es el instante centinela que los casos propios escriben por SQL en
// updated_at o en degraded_since: lejos de cualquier now(), para que «la fila no se reescribió» y
// «la marca es la de la entrada» se lean como una igualdad exacta y no como una carrera de relojes.
var fleetContractFrozenAt = time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)

// TestFleetContrato_Postgres corre las 42 promesas del puerto fleet.Repository, las mismas que
// pasan contra fleethelpertest.Memoria, contra el adaptador Postgres. Cada caso recibe su base, un
// repositorio nuevo con su KEK y su clave de índice ciego, y siembra sus tenants (filas reales de
// public.tenants: public.fleet_sessions los referencia por clave foránea).
func TestFleetContrato_Postgres(t *testing.T) {
	fleethelpertest.ContratoRepository(t, func(t *testing.T) fleethelpertest.Montaje {
		t.Helper()
		keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
		return newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t)).m
	})
}

// fleetContractRig es lo que un caso necesita para ejercer el adaptador y mirar lo que dejó: la
// base, las dos piezas de cifrado con las que se construyó el repositorio (para recalcular el
// índice ciego y abrir el sobre leído por SQL) y el Montaje, cuyo campo Repository es el puerto.
type fleetContractRig struct {
	db     *sql.DB
	kp     crypto.KeyProvider
	cipher *crypto.FieldCipher
	m      fleethelpertest.Montaje
}

// newFleetContractDB clona una base con nuevaBase (que la borra en el Cleanup del test que la
// pide) y la abre con el arnés: es la única conexión que usa un caso (D-F9-6).
func newFleetContractDB(t *testing.T) *sql.DB {
	t.Helper()
	proceso := fmt.Sprintf("fleet_contrato_%02d", fleetContractCases.Add(1))
	return nuevaBase(t, proceso).Abrir(t)
}

// newFleetContractRig construye sobre db un PostgresRepository con el keyring dado
// ("id:base64,id:base64"), su KEK current y la clave EXPLÍCITA del índice ciego (hallazgo 30: sin
// IndexB64 el proveedor la derivaría de la KEK current con un aviso, y rotar la KEK movería el
// índice). Dos rigs sobre la misma base con keyrings distintos son dos despliegues del servidor
// antes y después de una rotación. Falla el test si el keyring no es válido.
func newFleetContractRig(t *testing.T, db *sql.DB, keyringB64, currentID, indexB64 string) fleetContractRig {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: keyringB64,
		CurrentID:  currentID,
		IndexB64:   indexB64,
	})
	if err != nil {
		t.Fatalf("KeyProvider del contrato (current %q): %v", currentID, err)
	}
	cipher := crypto.NewFieldCipher(kp)
	repo := fleet.NewPostgresRepository(db, cipher, kp)
	return fleetContractRig{
		db:     db,
		kp:     kp,
		cipher: cipher,
		m: fleethelpertest.Montaje{
			Repository: repo,
			SeedTenant: func(t *testing.T) string {
				t.Helper()
				return seedFleetContractTenant(t, db)
			},
			// ProfilesByTenant no está en el puerto a propósito: es del adaptador.
			Profiles: fleetContractProfilesReader(repo.ProfilesByTenant),
		},
	}
}

// fleetContractProfilesReader convierte la lectura de la foto de perfiles del adaptador en el
// Montaje.Profiles que pide la suite: la llama con un plazo y falla el test si da error. P es
// fleet.TenantProfiles; lo infiere el compilador del método que recibe, y así este fichero no
// nombra el tipo.
func fleetContractProfilesReader[P any](read func(context.Context, string) (P, error)) func(*testing.T, string) P {
	return func(t *testing.T, tenantID string) P {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), fleetContractTimeout)
		defer cancel()
		photo, err := read(ctx, tenantID)
		if err != nil {
			t.Fatalf("ProfilesByTenant(%q): %v", tenantID, err)
		}
		return photo
	}
}

// fleetContractZeroHealth devuelve el valor cero del parte de salud que recibe SaveHealth (H es
// fleet.HealthSnapshot, inferido del método). El caso rellena sus campos sobre ese valor.
func fleetContractZeroHealth[H any](func(context.Context, string, string, string, H) error) (zero H) {
	return zero
}

// seedFleetContractTenant inserta un tenant en public.tenants y devuelve su id, distinto en cada
// llamada (el slug lleva un UUID nuevo: la suite pide hasta dos por caso). Solo rellena las
// columnas NOT NULL sin valor por defecto (0001_tenants.sql). Falla el test si no puede.
func seedFleetContractTenant(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fleetContractTimeout)
	defer cancel()
	slug := "flota-" + uuid.NewString()
	var id string
	err := db.QueryRowContext(ctx,
		`INSERT INTO public.tenants (slug, display_name) VALUES ($1, $2) RETURNING id::text`,
		slug, "Tenant "+slug,
	).Scan(&id)
	if err != nil {
		t.Fatalf("sembrar el tenant %q: %v", slug, err)
	}
	return id
}

// fleetContractRow es lo que los casos propios leen POR SQL de una fila de public.fleet_sessions:
// las cuatro columnas del sobre del self_pn, las dos marcas de tiempo que gobierna el adaptador y
// la fila entera en JSON (para buscar en ella un número en claro, esté en la columna que esté).
type fleetContractRow struct {
	enc, dek      []byte
	kekID, bidx   sql.NullString
	updatedAt     time.Time
	degradedSince sql.NullTime
	json          string
}

// readFleetContractRow lee la fila de la sesión. Falla el test si no existe o si la lectura falla.
func readFleetContractRow(t *testing.T, db *sql.DB, tenantID, edgeID, sessionID string) fleetContractRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fleetContractTimeout)
	defer cancel()
	var r fleetContractRow
	err := db.QueryRowContext(ctx, `
		SELECT self_pn_enc, self_pn_dek, self_pn_kek_id, self_pn_bidx, updated_at, degraded_since, to_jsonb(f)::text
		FROM public.fleet_sessions f
		WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`,
		tenantID, edgeID, sessionID,
	).Scan(&r.enc, &r.dek, &r.kekID, &r.bidx, &r.updatedAt, &r.degradedSince, &r.json)
	if err != nil {
		t.Fatalf("leer la fila de la sesión (%s, %s, %s): %v", tenantID, edgeID, sessionID, err)
	}
	return r
}

// Las dos sentencias de freezeFleetContractRow: cada una escribe el centinela en una columna de la
// fila de la sesión y no toca ninguna otra.
const (
	fleetContractFreezeUpdatedAt     = `UPDATE public.fleet_sessions SET updated_at = $4 WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
	fleetContractFreezeDegradedSince = `UPDATE public.fleet_sessions SET degraded_since = $4 WHERE tenant_id = $1::uuid AND edge_id = $2 AND session_id = $3`
)

// freezeFleetContractRow escribe por SQL el centinela fleetContractFrozenAt en la fila de la sesión
// con una de las dos sentencias de arriba (fleetContractFreezeUpdatedAt o
// fleetContractFreezeDegradedSince). Falla el test si no toca exactamente una fila.
func freezeFleetContractRow(t *testing.T, db *sql.DB, statement, tenantID, edgeID, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), fleetContractTimeout)
	defer cancel()
	res, err := db.ExecContext(ctx, statement, tenantID, edgeID, sessionID, fleetContractFrozenAt)
	if err != nil {
		t.Fatalf("congelar la sesión (%s, %s, %s) con %q: %v", tenantID, edgeID, sessionID, statement, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("congelar la sesión (%s, %s, %s) con %q: tocó %d filas (err %v); quería 1", tenantID, edgeID, sessionID, statement, n, err)
	}
}

// newFleetContractSession siembra un tenant y registra bajo él una sesión online con edge_id y
// session_id propios: el punto de partida de todos los casos propios.
func newFleetContractSession(t *testing.T, rig fleetContractRig) (tenantID, edgeID, sessionID string) {
	t.Helper()
	tenantID, edgeID, sessionID = rig.m.SeedTenant(t), "edge-"+uuid.NewString(), "session-"+uuid.NewString()
	if err := rig.m.Repository.MarkOnline(t.Context(), tenantID, edgeID, sessionID); err != nil {
		t.Fatalf("MarkOnline(%s, %s, %s): error inesperado %v", tenantID, edgeID, sessionID, err)
	}
	return tenantID, edgeID, sessionID
}

// TestFleetContract_SaveHealth_DegradedSinceLivesInTheColumn es el caso propio de degraded_since
// (hallazgo 30). La suite ya afirma por Get que la marca se fija al entrar, se conserva y se
// limpia; lo que solo se ve en Postgres es CÓMO: el CASE … COALESCE(degraded_since, now()) decide
// contra el valor que la fila tiene en ese instante, no contra uno que el proceso recuerde.
//
//   - una sesión recién registrada tiene la columna a NULL (no 'epoch': hallazgo 22);
//   - con una marca de entrada sembrada por SQL, un parte degradado la CONSERVA byte a byte;
//   - un parte sano la deja en NULL;
//   - y la entrada siguiente la fija con el now() de la base, no con el centinela.
func TestFleetContract_SaveHealth_DegradedSinceLivesInTheColumn(t *testing.T) {
	keyring := fleetContractKeyID + ":" + clavesSecretoB64(t)
	rig := newFleetContractRig(t, newFleetContractDB(t), keyring, fleetContractKeyID, clavesSecretoB64(t))
	tenant, edge, session := newFleetContractSession(t, rig)
	repo := rig.m.Repository
	save := func(whatsappState, reason string) {
		t.Helper()
		h := fleetContractZeroHealth(repo.SaveHealth)
		h.WhatsappState, h.DegradedReason = whatsappState, reason
		if err := repo.SaveHealth(t.Context(), tenant, edge, session, h); err != nil {
			t.Fatalf("SaveHealth(%q, %q): error inesperado %v", whatsappState, reason, err)
		}
	}

	born := readFleetContractRow(t, rig.db, tenant, edge, session)
	if born.degradedSince.Valid {
		t.Fatalf("una sesión recién registrada tiene degraded_since = %v; quería NULL", born.degradedSince.Time)
	}

	// La sesión entró en degradado en el instante centinela, y lo sigue estando.
	freezeFleetContractRow(t, rig.db, fleetContractFreezeDegradedSince, tenant, edge, session)
	save("degraded", "dek_load_timeout")
	kept := readFleetContractRow(t, rig.db, tenant, edge, session)
	if !kept.degradedSince.Valid || !kept.degradedSince.Time.Equal(fleetContractFrozenAt) {
		t.Errorf("degraded_since tras un parte degradado = %v (válida %v); quería la marca de entrada %v",
			kept.degradedSince.Time, kept.degradedSince.Valid, fleetContractFrozenAt)
	}
	s, found, err := repo.Get(t.Context(), tenant, edge, session)
	if err != nil || !found || !s.DegradedSince.Equal(fleetContractFrozenAt) {
		t.Errorf("Get: (DegradedSince %v, found %v, err %v); quería la marca de entrada %v", s.DegradedSince, found, err, fleetContractFrozenAt)
	}

	save("connected", "")
	if healthy := readFleetContractRow(t, rig.db, tenant, edge, session); healthy.degradedSince.Valid {
		t.Errorf("degraded_since tras un parte sano = %v; quería NULL", healthy.degradedSince.Time)
	}

	save("dead", "")
	again := readFleetContractRow(t, rig.db, tenant, edge, session)
	if !again.degradedSince.Valid || again.degradedSince.Time.Before(born.updatedAt) {
		t.Errorf("degraded_since de la segunda entrada = %v (válida %v); quería el now() de la base, no anterior al alta (%v)",
			again.degradedSince.Time, again.degradedSince.Valid, born.updatedAt)
	}
}
