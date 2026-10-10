//go:build integracion && pendiente

package procesos

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
)

// Este fichero corre la suite de contrato del resolver de tenant del runtime
// (runtimehelpertest.ContratoTenantResolver) contra runtime.PostgresTenantResolver sobre una base
// clonada de la plantilla migrada (F8-04, P4). Como events_contrato_test.go, no es un proceso de
// caja negra: es la excepción de R9.4.d que decidió D-F1-8, y por eso solo importa la suite y el
// paquete del adaptador, del que solo nombra el constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad contra public.fleet_sessions lo que el unitario del adaptador, con su
// driver de mentira, solo puede mirar como forma: que la consulta agrupa por tenant (varios edges
// de un tenant son uno; dos tenants, ambigüedad), que el perfil efectivo es pasivo en cuanto UNA
// fila no es 'active' —también con un perfil fuera de dominio, para lo que el caso retira el CHECK
// de la 0063 en SU base—, que una sesión sembrada sin perfil cae al DEFAULT passive, y que no
// filtra por estado ni escribe nada.
//
// 🔴 Lleva la etiqueta `pendiente` además de `integracion` mientras runtime.PostgresTenantResolver
// esté en rojo: un panic aborta el binario ENTERO de los procesos. El verde del adaptador (F8-04b)
// se la quita, a este fichero y a runtime_self_numbers_contrato_test.go, que usa sus siembras.

// runtimeContractTenantCases cuenta los Montajes pedidos en esta corrida: la suite llama a nuevo
// una vez por caso (en serie) y cada uno necesita una base con nombre propio.
var runtimeContractTenantCases atomic.Int64

// TestRuntimeTenantResolverContrato_Postgres corre las promesas del resolver de tenant, las mismas
// que pasan contra el gemelo en memoria, contra el adaptador Postgres. Cada caso recibe su base,
// con dos tenants recién creados y public.fleet_sessions vacía.
func TestRuntimeTenantResolverContrato_Postgres(t *testing.T) {
	runtimehelpertest.ContratoTenantResolver(t, runtimeContractNewTenantMontaje)
}

// runtimeContractNewTenantMontaje devuelve el Montaje limpio de un caso: clona una base con
// nuevaBase (que la borra en el Cleanup del subtest), la abre con el arnés, siembra dos tenants en
// public.tenants (fleet_sessions los exige por clave foránea) y construye el adaptador sobre ese
// *sql.DB.
func runtimeContractNewTenantMontaje(t *testing.T) runtimehelpertest.MontajeTenantResolver {
	t.Helper()
	n := runtimeContractTenantCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("rt_tenant_contrato_%02d", n)).Abrir(t)

	return runtimehelpertest.MontajeTenantResolver{
		Resolver:        runtime.NewPostgresTenantResolver(db),
		TenantA:         flowstoreSeedTenant(t, db, fmt.Sprintf("rt-tenant-contrato-%02d-a", n)),
		TenantB:         flowstoreSeedTenant(t, db, fmt.Sprintf("rt-tenant-contrato-%02d-b", n)),
		Seed:            func(t *testing.T, s runtimehelpertest.Session) { runtimeContractSeedSession(t, db, s) },
		AllowAnyProfile: func(t *testing.T) { runtimeContractAllowAnyProfile(t, db) },
		Sessions:        func(t *testing.T) []runtimehelpertest.Session { return runtimeContractSessions(t, db) },
	}
}

// runtimeContractSeedSession deja la sesión en public.fleet_sessions; si ya existe una con su
// clave (tenant, edge, sesión), le cambia estado, perfil e índice. Solo escribe las columnas que
// leen los dos adaptadores del runtime; el resto queda en su valor por defecto.
//
// Un Profile vacío NO escribe la columna: la fila cae al DEFAULT de la 0063 (y en la sustitución,
// vuelve a él). Un SelfPnBidx vacío es NULL: la sesión sin número conocido. El sobre cifrado del
// número (self_pn_enc/dek/kek_id) no se siembra: los adaptadores del runtime no descifran nada.
func runtimeContractSeedSession(t *testing.T, db *sql.DB, s runtimehelpertest.Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	var err error
	if s.Profile == "" {
		_, err = db.ExecContext(ctx, `
			INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, self_pn_bidx)
			VALUES ($1::uuid, $2, $3, $4, NULLIF($5, ''))
			ON CONFLICT (tenant_id, edge_id, session_id)
				DO UPDATE SET state = EXCLUDED.state, profile = DEFAULT, self_pn_bidx = EXCLUDED.self_pn_bidx`,
			s.TenantID, s.EdgeID, s.SessionID, s.State, s.SelfPnBidx)
	} else {
		_, err = db.ExecContext(ctx, `
			INSERT INTO public.fleet_sessions (tenant_id, edge_id, session_id, state, profile, self_pn_bidx)
			VALUES ($1::uuid, $2, $3, $4, $5, NULLIF($6, ''))
			ON CONFLICT (tenant_id, edge_id, session_id)
				DO UPDATE SET state = EXCLUDED.state, profile = EXCLUDED.profile, self_pn_bidx = EXCLUDED.self_pn_bidx`,
			s.TenantID, s.EdgeID, s.SessionID, s.State, s.Profile, s.SelfPnBidx)
	}
	if err != nil {
		t.Fatalf("sembrar la sesión (%s, %s, %s) con estado %q y perfil %q: %v", s.TenantID, s.EdgeID, s.SessionID, s.State, s.Profile, err)
	}
}

// runtimeContractAllowAnyProfile retira el CHECK de dominio de fleet_sessions.profile (0063), que
// es lo que hoy hace inalcanzable un perfil desconocido. No lo restaura: la base es la de ESTE
// caso y se borra con él.
func runtimeContractAllowAnyProfile(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), flowstoreTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx,
		`ALTER TABLE public.fleet_sessions DROP CONSTRAINT IF EXISTS fleet_sessions_profile_chk`); err != nil {
		t.Fatalf("retirar fleet_sessions_profile_chk: %v", err)
	}
}

// runtimeContractSessions devuelve TODAS las filas de public.fleet_sessions por lo que leen los
// adaptadores del runtime, con el índice NULL a vacío. Las ordena aquí, y no en SQL, para que el
// orden sea el de la suite (bytes) y no el de la intercalación de la base.
func runtimeContractSessions(t *testing.T, db *sql.DB) []runtimehelpertest.Session {
	t.Helper()
	rows := flowstoreRows(t, db, "las sesiones de la flota", `
		SELECT tenant_id::text, edge_id, session_id, state, profile, COALESCE(self_pn_bidx, '')
		FROM public.fleet_sessions`,
		func(rows *sql.Rows, s *runtimehelpertest.Session) error {
			return rows.Scan(&s.TenantID, &s.EdgeID, &s.SessionID, &s.State, &s.Profile, &s.SelfPnBidx)
		})
	slices.SortFunc(rows, func(a, b runtimehelpertest.Session) int {
		return cmp.Or(cmp.Compare(a.TenantID, b.TenantID), cmp.Compare(a.EdgeID, b.EdgeID), cmp.Compare(a.SessionID, b.SessionID))
	})
	return rows
}
