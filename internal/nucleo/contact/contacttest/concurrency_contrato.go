// Caso de concurrencia: get-or-create atómico con llamadas simultáneas (R-32). Corre con -race.

package contacttest

import (
	"fmt"
	"sync"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/google/uuid"
)

// gorutinasConcurrentes es cuántas llamadas simultáneas lanza Concurrente_MismaRef_UnSoloID.
const gorutinasConcurrentes = 16

// casoConcurrenteMismaRefUnSoloID (R-32): get-or-create atómico y seguro para uso concurrente.
// Dieciséis llamadas simultáneas con la misma ref nueva terminan todas sin error y con UN solo
// contact_id, que además es el que guarda la ref. Corre bien con -race.
func casoConcurrenteMismaRefUnSoloID(t *testing.T, m Montaje) {
	ref := refTel(t, numeroAna)
	ids := make([]string, gorutinasConcurrentes)
	errs := make([]error, gorutinasConcurrentes)
	salida := make(chan struct{})
	var wg sync.WaitGroup
	for i := range gorutinasConcurrentes {
		wg.Go(func() {
			<-salida
			ids[i], errs[i] = m.Resolver.Resolve(t.Context(), m.TenantA, []contact.Ref{ref}, "")
		})
	}
	close(salida)
	wg.Wait()

	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("Resolve concurrente %d: %v", i, errs[i])
		}
	}
	for i, id := range ids {
		mismoID(t, fmt.Sprintf("la llamada concurrente %d frente a la 0", i), id, ids[0])
	}
	if _, err := uuid.Parse(ids[0]); err != nil {
		t.Fatalf("el contact_id concurrente %q no es un UUID: %v", ids[0], err)
	}
	mismoID(t, "la ref después de la ráfaga", resolverOK(t, m, m.TenantA, ref), ids[0])
	exigirDestino(t, m, m.TenantA, ids[0], ref)
}
