//go:build integracion && pendiente

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/trigger/triggerhelpertest"
)

// Este fichero corre la suite de contrato de trigger.Store (triggerhelpertest.Contrato) contra
// trigger.PostgresStore sobre una base clonada de la plantilla migrada (T8.4, P4). Como
// tenantvars_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que
// decidió D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa el
// constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad contra public.flow_triggers (migraciones 0023, 0027 y 0052) lo que el
// unitario del adaptador, con su driver de mentira, solo puede mirar como texto: que el INSERT
// llena las diez columnas y el RETURNING trae el trigger_id que puso la base; que los cinco
// nullable hacen el viaje "" → NULL → ""; que `session_id = $3 OR session_id IS NULL` da las de la
// sesión más las globales y, con la sesión vacía, solo las globales; que ORDER BY trigger_id
// ordena como el doble en memoria; y que el DELETE va acotado al tenant y dice «no existía» por
// las filas afectadas.

// triggerTimeout acota cada sentencia de la siembra y del observador: la base es local al
// contenedor de la corrida.
const triggerTimeout = 15 * time.Second

// triggerContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una vez
// por caso (en serie) y cada uno necesita una base con nombre propio.
var triggerContractCases atomic.Int64

// TestTriggerContrato_Postgres corre las promesas del puerto trigger.Store, las mismas que pasan
// contra trigger.MemoryStore, contra el adaptador Postgres. Cada caso recibe su base, sin reglas,
// sus dos tenants y un PostgresStore nuevo.
func TestTriggerContrato_Postgres(t *testing.T) {
	triggerhelpertest.Contrato(t, triggerContractNewMontaje)
}

// triggerContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase
// (que la borra en el Cleanup del subtest), la abre con el arnés, siembra los dos tenants por SQL
// (D-F1-8: no con el repositorio de tenants, para no importar nada más de internal/) y construye
// el adaptador sobre ese *sql.DB.
//
// flow_triggers.tenant_id es UUID y NO tiene clave foránea a public.tenants: bastaría con dos
// UUID cualesquiera. Se siembran tenants de verdad porque es lo que hay en UAT y lo que hacía el
// test de integración viejo (seedTenant).
func triggerContractNewMontaje(t *testing.T) triggerhelpertest.Montaje {
	t.Helper()
	n := triggerContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("trigger_contrato_%02d", n)).Abrir(t)

	return triggerhelpertest.Montaje{
		Store:   trigger.NewPostgresStore(db),
		TenantA: triggerContractSeedTenant(t, db, "trigger-contrato-a"),
		TenantB: triggerContractSeedTenant(t, db, "trigger-contrato-b"),
		Hidden:  func(t *testing.T, tenantID string) map[string]string { return triggerContractHidden(t, db, tenantID) },
	}
}

// triggerContractSeedTenant inserta un tenant con el slug dado en public.tenants y devuelve su id.
// Solo rellena las columnas NOT NULL sin valor por defecto (0001_tenants.sql).
func triggerContractSeedTenant(t *testing.T, db *sql.DB, slug string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), triggerTimeout)
	defer cancel()
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

// triggerContractHidden es el Montaje.Hidden de Postgres: las dos columnas de flow_triggers que
// trigger.Rule no proyecta —created_at y updated_at—, por trigger_id, como texto. Con ellas la
// marca de estado de la suite vigila las trece columnas de la tabla: una sentencia que
// reescribiera una fila testigo (un UPDATE de más, un borrar-y-reinsertar) cambiaría alguna.
func triggerContractHidden(t *testing.T, db *sql.DB, tenantID string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), triggerTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
		SELECT trigger_id::text, created_at::text || ' | ' || updated_at::text
		FROM public.flow_triggers
		WHERE tenant_id = $1::uuid`,
		tenantID)
	if err != nil {
		t.Fatalf("leer lo oculto de flow_triggers (tenant %q): %v", tenantID, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("leer lo oculto de flow_triggers (tenant %q): cerrar rows: %v", tenantID, err)
		}
	}()
	out := make(map[string]string)
	for rows.Next() {
		var id, stamps string
		if err := rows.Scan(&id, &stamps); err != nil {
			t.Fatalf("leer lo oculto de flow_triggers (tenant %q): escanear: %v", tenantID, err)
		}
		out[id] = stamps
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("leer lo oculto de flow_triggers (tenant %q): iterar: %v", tenantID, err)
	}
	return out
}
