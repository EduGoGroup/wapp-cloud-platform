//go:build integracion

package procesos

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/ingest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/ingest/ingesthelpertest"
)

// Este fichero corre la suite de contrato de ingest.Deduper (ingesthelpertest.ContratoDeduper)
// contra ingest.PostgresDeduper sobre una base clonada de la plantilla migrada (T3.30 = T9.24,
// F3-05). Como contact_contrato_test.go, no es un proceso de caja negra: es la excepción de R9.4.d
// que decidió D-F1-8, y por eso solo importa la suite y el paquete del adaptador, del que solo usa
// el constructor (candado ProcessImports, regla 3).
//
// Es la que prueba de verdad contra public.ingest_dedupe (migración 0031) lo que el doble solo
// imita: que el INSERT … ON CONFLICT DO NOTHING sobre (session_id, wa_message_id) da un solo
// «primer avistamiento» por clave, también con dieciséis llamadas a la vez.
//
// El puerto es su propio observador (Seen dice lo que había): el Montaje solo pide dos session_id
// de los que el Deduper no ha visto nada (hallazgo 12 de F3).
//
// Lo que la suite NO afirma, y aquí tampoco: la poda por retención y el camino de error. El deduper
// se construye con sus valores por defecto —el candado no deja usar ingest.WithSweep ni
// ingest.WithRetention desde aquí (del paquete del puerto solo se usan sus New…)—, así que la poda
// perezosa, una cada 512 claves NUEVAS, no se dispara en ningún caso: el que más inserta, inserta
// dos.

// ingestContractCases cuenta los Montajes pedidos en esta corrida: ContratoDeduper llama a nuevo una
// vez por caso (en serie) y cada uno necesita una base con nombre propio.
var ingestContractCases atomic.Int64

// TestIngestContrato_Postgres corre las promesas de ingest.Deduper, las mismas que pasan contra
// ingesthelpertest.Memoria, contra el adaptador Postgres. Cada caso recibe su base, sin ninguna
// clave vista, y un PostgresDeduper nuevo.
func TestIngestContrato_Postgres(t *testing.T) {
	ingesthelpertest.ContratoDeduper(t, ingestContractNewMontaje)
}

// ingestContractNewMontaje devuelve el Montaje limpio de un caso: clona una base con nuevaBase (que
// la borra en el Cleanup del subtest), la abre con el arnés y construye el PostgresDeduper sobre ese
// *sql.DB. La tabla no tiene tenant ni claves foráneas, así que no hay nada que sembrar; las dos
// sesiones llevan el número del caso, aunque con una base por caso ya vendrían sin claves.
func ingestContractNewMontaje(t *testing.T) ingesthelpertest.Montaje {
	t.Helper()
	n := ingestContractCases.Add(1)
	db := nuevaBase(t, fmt.Sprintf("ingest_contrato_%02d", n)).Abrir(t)

	return ingesthelpertest.Montaje{
		Deduper:  ingest.NewPostgresDeduper(db),
		SessionA: fmt.Sprintf("ingest-contrato-%02d-a", n),
		SessionB: fmt.Sprintf("ingest-contrato-%02d-b", n),
	}
}
