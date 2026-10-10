//go:build integracion && pendiente

package procesos

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/runtime/runtimehelpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// Este fichero corre la suite de contrato de los números propios del runtime
// (runtimehelpertest.ContratoSelfNumbers) contra runtime.PostgresSelfNumbers sobre una base
// clonada de la plantilla migrada (F8-04, P4). Como events_contrato_test.go, no es un proceso de
// caja negra: es la excepción de R9.4.d que decidió D-F1-8, y por eso solo importa la suite, el
// paquete del adaptador —del que solo nombra el constructor— e internal/platform/crypto, de donde
// sale su segundo argumento (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad contra public.fleet_sessions lo que el unitario del adaptador, con su
// driver de mentira, solo puede mirar como forma: que el número se busca por self_pn_bidx y
// acotado al tenant, que una sesión retirada no cuenta y una offline sí, que la decisión agrega
// sobre TODAS las filas del número, que un perfil fuera de dominio bloquea y que un índice escrito
// con OTRA clave no casa jamás, sin un solo error (trampa T-3).
//
// 🔴 Lo que NO prueba: que la flota, al guardar el número propio, normalice y use la misma clave.
// Aquí el índice lo calcula este fichero con el KeyProvider del adaptador y lo siembra por SQL; no
// pasa por fleet.PostgresRepository.SetSelfPn. Esa simetría es de fleet_contrato_selfpn_test.go y
// del proceso «entrante a respuesta».
//
// Las claves de aquí son las del ENVELOPE DE PII DE NEGOCIO (internal/platform/crypto), NO la DEK
// del ADR-0007 que custodia el cliente. Se generan en el test y mueren con él.
//
// 🔴 Lleva la etiqueta `pendiente` además de `integracion` mientras runtime.PostgresSelfNumbers
// esté en rojo: un panic aborta el binario ENTERO de los procesos. El verde del adaptador (F8-04b)
// se la quita. Las siembras y el observador de public.fleet_sessions son los de
// runtime_tenant_resolver_contrato_test.go.

// runtimeContractSelfCases cuenta los Montajes pedidos en esta corrida: la suite llama a nuevo una
// vez por caso (en serie) y cada uno necesita una base con nombre propio.
var runtimeContractSelfCases atomic.Int64

// TestRuntimeSelfNumbersContrato_Postgres corre las promesas de los números propios, las mismas
// que pasan contra el gemelo en memoria, contra el adaptador Postgres. Cada caso recibe su base,
// con dos tenants recién creados y public.fleet_sessions vacía, y dos adaptadores sobre ella: con
// KeyProvider y sin él.
func TestRuntimeSelfNumbersContrato_Postgres(t *testing.T) {
	runtimehelpertest.ContratoSelfNumbers(t, runtimeContractNewSelfMontaje)
}

// runtimeContractKeyProvider devuelve un KeyProvider con una KEK y una clave de índice recién
// generadas: dos llamadas son dos despliegues que no comparten clave de índice. La clave del
// índice va EXPLÍCITA (sin ella el proveedor la derivaría de la KEK, con un aviso).
func runtimeContractKeyProvider(t *testing.T) crypto.KeyProvider {
	t.Helper()
	kp, err := crypto.NewEnvKeyProvider(crypto.KeyringConfig{
		MasterB64: clavesSecretoB64(t),
		IndexB64:  clavesSecretoB64(t),
	})
	if err != nil {
		t.Fatalf("KeyProvider del contrato: %v", err)
	}
	return kp
}

// runtimeContractNewSelfMontaje devuelve el Montaje limpio de un caso: clona una base con
// nuevaBase (que la borra en el Cleanup del subtest), la abre con el arnés, siembra dos tenants en
// public.tenants y construye sobre ese *sql.DB el adaptador con su KeyProvider y otro sin él. El
// segundo KeyProvider no lo ve ningún adaptador: solo calcula el índice «de otra clave».
func runtimeContractNewSelfMontaje(t *testing.T) runtimehelpertest.MontajeSelfNumbers {
	t.Helper()
	n := runtimeContractSelfCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("rt_selfpn_contrato_%02d", n)).Abrir(t)
	kp, other := runtimeContractKeyProvider(t), runtimeContractKeyProvider(t)

	return runtimehelpertest.MontajeSelfNumbers{
		Checker:         runtime.NewPostgresSelfNumbers(db, kp),
		NoKeyProvider:   runtime.NewPostgresSelfNumbers(db, nil),
		TenantA:         flowstoreSeedTenant(t, db, fmt.Sprintf("rt-selfpn-contrato-%02d-a", n)),
		TenantB:         flowstoreSeedTenant(t, db, fmt.Sprintf("rt-selfpn-contrato-%02d-b", n)),
		BlindIndex:      kp.BlindIndex,
		OtherBlindIndex: other.BlindIndex,
		Seed:            func(t *testing.T, s runtimehelpertest.Session) { runtimeContractSeedSession(t, db, s) },
		AllowAnyProfile: func(t *testing.T) { runtimeContractAllowAnyProfile(t, db) },
		Sessions:        func(t *testing.T) []runtimehelpertest.Session { return runtimeContractSessions(t, db) },
	}
}
