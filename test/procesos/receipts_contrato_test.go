//go:build integracion

package procesos

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/receipts/receiptshelpertest"
)

// Este fichero corre la suite de contrato de receipts.Store (receiptshelpertest.ContratoStore)
// contra receipts.PostgresStore sobre una base clonada de la plantilla migrada (T3.30 = T9.24,
// F3-05). Como contact_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d
// que decidió D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa
// el constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad contra public.message_receipts (migración 0022) lo que el doble solo
// imita: que el ON CONFLICT sobre (session_id, message_id, status) refresca la fila sin duplicarla
// ni cambiarle el id, también con ocho escritores a la vez, y que List ordena por
// (recorded_at DESC, id DESC) y pagina con LIMIT/OFFSET.
//
// El puerto es su propia siembra (Save) y su propio observador (List): el Montaje solo pide dos
// session_id sin acuses (hallazgo 12 de F3). Lo que la suite NO afirma, y aquí tampoco (hallazgo
// 13): el ReceiptAt de un acuse guardado con ReceiptAt cero, que Postgres devuelve como la época
// Unix y el doble como el cero.

// receiptsContractCases cuenta los Montajes pedidos en esta corrida: ContratoStore llama a nuevo una
// vez por caso (en serie) y cada uno necesita una base con nombre propio.
var receiptsContractCases atomic.Int64

// TestReceiptsContrato_Postgres corre las promesas del puerto receipts.Store, las mismas que pasan
// contra receiptshelpertest.Memoria, contra el adaptador Postgres. Cada caso recibe su base, vacía
// de acuses, y un PostgresStore nuevo.
func TestReceiptsContrato_Postgres(t *testing.T) {
	receiptshelpertest.ContratoStore(t, receiptsContractNewMontaje)
}

// receiptsContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase
// (que la borra en el Cleanup del subtest), la abre con el arnés y construye el PostgresStore sobre
// ese *sql.DB. La tabla no tiene tenant ni claves foráneas, así que no hay nada que sembrar; las dos
// sesiones llevan el número del caso, aunque con una base por caso ya vendrían sin acuses (la suite
// lo comprueba con List antes de cada caso).
func receiptsContractNewMontaje(t *testing.T) receiptshelpertest.Montaje {
	t.Helper()
	n := receiptsContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("receipts_contrato_%02d", n)).Abrir(t)

	return receiptshelpertest.Montaje{
		Store:    receipts.NewPostgresStore(db),
		SessionA: fmt.Sprintf("receipts-contrato-%02d-a", n),
		SessionB: fmt.Sprintf("receipts-contrato-%02d-b", n),
	}
}
