// Porta internal/publicapi/publicapi.go @ 115a4ba (defaultDiagnosticsTTL, defaultDBTimeout, dbCtx,
// margenDeEscritura, SendBudgetFrom, sendCtx y dbTimedOut504, líneas 287-428).
//
// deadlines.go — LOS PLAZOS DE LA CARA: el de cada lectura a BD, el presupuesto de una petición
// de envío y la traducción del plazo vencido a 504. En la spec FX era `plazos.go` (05 E-11).
//
// En el rojo solo existe SendBudgetFrom: el resto (dbCtx, sendCtx, dbTimedOut504 y sus
// constantes) es NO exportado y nace con el verde (05 E-4, P6), con su test.

package apipublica

import (
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// SendBudgetFrom DERIVA el presupuesto de una petición de envío del WriteTimeout del servidor
// HTTP que la sirve (Plan 050 · Ola 5 · T5.4, cierra REQ-050.19): writeTimeout menos el margen
// de escritura de 1 s (10 s ⇒ 9 s). Un writeTimeout que no deja sitio al margen (≤ 1 s, cero o
// negativo) devuelve 0 = SIN presupuesto: es preferible a un plazo ya vencido al nacer, que
// abortaría TODOS los envíos en el acto. Nunca devuelve un valor negativo.
//
// Lo llama el arranque en el mismo sitio que construye el http.Server, y el resultado viaja en
// MessagesDeps.SendBudget: así el presupuesto y el WriteTimeout no pueden desincronizarse.
func SendBudgetFrom(writeTimeout time.Duration) time.Duration {
	panic(pendiente.Implementar("apipublica.SendBudgetFrom"))
}
