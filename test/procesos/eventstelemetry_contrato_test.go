//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/apipublica/eventstelemetryhelpertest"
)

// Este fichero corre la suite de contrato de apipublica.EventTelemetryReader
// (eventstelemetryhelpertest.Contrato) contra apipublica.PostgresEventTelemetryStore sobre una
// base clonada de la plantilla migrada (TX.16 de FX, P4). Como receipts_contrato_test.go, no es un
// proceso de caja negra: es la excepción de R9.4.d que decidió D-F1-8, y por eso solo importa la
// suite y el paquete del adaptador, del que solo usa el constructor (candado ProcessImports,
// regla 3). El adaptador vive en la cara y no en un módulo (D-FX-4): por eso su suite cuelga de
// internal/apipublica y entra por la lista cerrada de candados.ContractAdapterDirs.
//
// Es la que prueba de verdad contra public.flow_events (migraciones 0009 y 0056) lo que el
// unitario del adaptador, con su driver de mentira, solo puede mirar como texto: que
// `name LIKE 'event\_%'` trata el guion bajo como literal; que `payload->>'kind'` da el texto de
// un número o un booleano y NULL si falta; que `$2::timestamptz IS NULL OR created_at >= $2` no
// filtra con NULL e incluye el instante exacto; que `(created_at, id) > ($3, $4)` compara el par
// y no cada parte; y que ORDER BY created_at, id desempata por id.
//
// tenant_id es TEXT sin clave foránea: sembrar un tenant es dar un UUID nuevo. El puerto es de
// solo lectura, así que la siembra es SQL sobre el mismo *sql.DB que usa el adaptador.

// eventTelemetryContractTimeout acota cada inserción: la base es local al contenedor de la
// corrida.
const eventTelemetryContractTimeout = 15 * time.Second

// eventTelemetryContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo
// una vez por caso (en serie) y cada uno necesita una base con nombre propio.
var eventTelemetryContractCases atomic.Int64

// TestEventTelemetryContrato_Postgres corre las promesas del puerto
// apipublica.EventTelemetryReader, las mismas que pasan contra eventstelemetryhelpertest.Memory,
// contra el adaptador Postgres. Cada caso recibe su base, sin una sola fila en flow_events.
func TestEventTelemetryContrato_Postgres(t *testing.T) {
	eventstelemetryhelpertest.Contrato(t, newEventTelemetryContractMontaje)
}

// newEventTelemetryContractMontaje devuelve el Montaje limpio de un caso: clona una base con
// nuevaBase (que la borra en el Cleanup del subtest), la abre con el arnés y construye el
// adaptador sobre el mismo *sql.DB con el que Insert siembra.
func newEventTelemetryContractMontaje(t *testing.T) eventstelemetryhelpertest.Montaje {
	t.Helper()
	n := eventTelemetryContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("eventstelemetry_contrato_%02d", n)).Abrir(t)

	return eventstelemetryhelpertest.Montaje{
		Reader:  apipublica.NewPostgresEventTelemetryStore(db),
		TenantA: uuidAleatorio(t),
		TenantB: uuidAleatorio(t),
		Insert: func(t *testing.T, row eventstelemetryhelpertest.FlowEvent) int64 {
			t.Helper()
			return insertEventTelemetryContractRow(t, db, row)
		},
	}
}

// insertEventTelemetryContractRow es el Montaje.Insert de Postgres: escribe la fila en
// public.flow_events con el created_at que trae (en UTC), no el DEFAULT now() de la tabla, y
// devuelve su id, que sale del BIGSERIAL y por tanto crece con cada inserción. Las tres columnas
// NOT NULL que la lectura no mira (contact_id, flow_id, flow_version) llevan valores fijos y
// opacos. Falla el test si no puede insertar.
func insertEventTelemetryContractRow(t *testing.T, db *sql.DB, row eventstelemetryhelpertest.FlowEvent) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), eventTelemetryContractTimeout)
	defer cancel()
	var id int64
	err := db.QueryRowContext(ctx, `
		INSERT INTO public.flow_events (tenant_id, contact_id, flow_id, flow_version, kind, name, payload, created_at)
		VALUES ($1, 'contact-opaco', 'flow-contrato', 1, $2, $3, $4::jsonb, $5)
		RETURNING id`,
		row.TenantID, row.Kind, row.Name, row.Payload, row.CreatedAt.UTC()).Scan(&id)
	if err != nil {
		t.Fatalf("Insert(tenant %q, nombre %q): %v", row.TenantID, row.Name, err)
	}
	return id
}
