//go:build integracion

package procesos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm/tenantllmhelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero corre la suite de contrato de tenantllm.Store (tenantllmhelpertest.Contrato) contra
// tenantllm.Postgres sobre una base clonada de la plantilla migrada (T4.31 = T9.25). Como
// fleet_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d que decidió
// D-F1-8, y por eso solo importa la suite, el paquete del adaptador (su constructor) y crypto (sus
// argumentos).
//
// 🔴 DEL PAQUETE tenantllm SOLO SE NOMBRA NewPostgres (candado ProcessImports, regla 3b): el puerto
// no se nombra, porque el campo Montaje.Store ya lo es, y la forma de la fila se devuelve con el
// tipo de la suite, tenantllmhelpertest.Row.
//
// Es la que prueba de verdad contra public.tenant_llm (migraciones 0071 y 0073) lo que el doble
// solo imita: que el ON CONFLICT (tenant_id) reemplaza la fila entera sin duplicarla, también con
// escritores a la vez; que pasar de la vía api a la local deja a NULL las seis columnas del eje api
// sin tropezar con los CHECK de la tabla; y que la clave que entra por Upsert vuelve por APIKey
// después de cifrarse y descifrarse con el sobre real.
//
// 🔴 HOMÓNIMO: la «DEK» y la KEK de este fichero son las del envelope de PII de negocio
// (internal/platform/crypto), NO la DEK del ADR-0007 que custodia el cliente. Se generan en el test
// y mueren con él; la credencial que cifran es una cadena inventada de la suite (cero gasto).

const (
	// tenantLLMContractTimeout acota la sentencia de observación: la base es local al contenedor
	// de la corrida.
	tenantLLMContractTimeout = 15 * time.Second

	// tenantLLMContractKeyID es el key_id de la única KEK del keyring de un Montaje de la suite.
	tenantLLMContractKeyID = "1"
)

// tenantLLMContractCases cuenta los Montajes pedidos en esta corrida: Contrato llama a nuevo una
// vez por caso (en serie) y cada uno necesita una base con nombre propio.
var tenantLLMContractCases atomic.Int64

// TestTenantLLMContrato_Postgres corre las promesas del puerto tenantllm.Store, las mismas que
// pasan contra tenantllmhelpertest.Memoria, contra el adaptador Postgres. Cada caso recibe su
// base, sin filas de configuración LLM, y un store nuevo con su propia KEK.
func TestTenantLLMContrato_Postgres(t *testing.T) {
	tenantllmhelpertest.Contrato(t, tenantLLMContractNewMontaje)
}

// tenantLLMContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase
// (que la borra en el Cleanup del subtest), la abre con el arnés —la única conexión que usa el
// caso (D-F9-6)— y construye sobre ese *sql.DB el store con un cifrador de campo de keyring propio.
// La clave del índice ciego va EXPLÍCITA aunque este store no la use: sin IndexB64 el proveedor la
// derivaría de la KEK current con un aviso (hallazgo 30 de F3).
func tenantLLMContractNewMontaje(t *testing.T) tenantllmhelpertest.Montaje {
	t.Helper()
	n := tenantLLMContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("tenantllm_contrato_%02d", n)).Abrir(t)

	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		KeyringB64: tenantLLMContractKeyID + ":" + clavesSecretoB64(t),
		CurrentID:  tenantLLMContractKeyID,
		IndexB64:   clavesSecretoB64(t),
	})
	if err != nil {
		t.Fatalf("KeyProvider del contrato (current %q): %v", tenantLLMContractKeyID, err)
	}

	return tenantllmhelpertest.Montaje{
		Store: tenantllm.NewPostgres(db, crypto.NewFieldCipher(kp)),
		// public.tenant_llm guarda el tenant como TEXT sin clave foránea: no hay nada que
		// insertar, basta un UUID nuevo en cada llamada (la suite pide hasta dos por caso).
		SeedTenant: uuidAleatorio,
		Row: func(t *testing.T, tenantID string) (tenantllmhelpertest.Row, bool) {
			t.Helper()
			return readTenantLLMContractRow(t, db, tenantID)
		},
	}
}

// readTenantLLMContractRow es el observador de estado de la suite: lee POR SQL la vía de la fila
// del tenant y cuáles de las seis columnas del eje api están rellenas. No trae ningún valor: ni el
// blob cifrado ni su sobre salen de la base. found es false si el tenant no tiene fila; falla el
// test si la lectura falla por cualquier otra causa.
func readTenantLLMContractRow(t *testing.T, db *sql.DB, tenantID string) (row tenantllmhelpertest.Row, found bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), tenantLLMContractTimeout)
	defer cancel()
	err := db.QueryRowContext(ctx, `
		SELECT via,
		       provider       IS NOT NULL,
		       model          IS NOT NULL,
		       api_key_enc    IS NOT NULL,
		       api_key_dek    IS NOT NULL,
		       api_key_kek_id IS NOT NULL,
		       consented_at   IS NOT NULL
		FROM public.tenant_llm
		WHERE tenant_id = $1`,
		tenantID,
	).Scan(&row.Via, &row.HasProvider, &row.HasModel, &row.HasKeyEnc, &row.HasKeyDEK, &row.HasKEKID, &row.HasConsent)
	if errors.Is(err, sql.ErrNoRows) {
		return tenantllmhelpertest.Row{}, false
	}
	if err != nil {
		t.Fatalf("leer la fila de public.tenant_llm del tenant %s: %v", tenantID, err)
	}
	return row, true
}
