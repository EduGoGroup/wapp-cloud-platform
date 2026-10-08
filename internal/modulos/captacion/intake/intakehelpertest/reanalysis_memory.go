package intakehelpertest

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// reanalysis_memory.go es la mitad de MachineMemory que hace de SEGUNDO PRODUCTOR de jobs: las
// dos operaciones de intake.Postgres que no son de ningún puerto del paquete (LiveJobOfEvent y
// OpenReanalysis). Nuevo: el doble viejo no las tenía, porque el paquete viejo solo las probaba
// por el texto de su SQL. Están sobre el MISMO doble porque es la misma tabla: el job que abre
// OpenReanalysis es el que después reclama ClaimNext.

// LiveJobOfEvent implementa ReanalysisStore: el id del job de ese tenant y ese evento que no
// está en un estado terminal; si hay varios, el creado más tarde (el `ORDER BY created_at DESC
// LIMIT 1` de la sentencia). Sin tenant o sin evento es un error, no «no hay ninguno».
func (s *MachineMemory) LiveJobOfEvent(ctx context.Context, tenantID, eventID string) (string, bool, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.LiveJobOfEvent"))
}

// OpenReanalysis implementa ReanalysisStore: inserta el job del re-análisis en `pending`, con su
// solicitud y su contexto, sin referencias ni sobre, y devuelve su id. `message_ts` se copia
// del job MÁS ANTIGUO de ese tenant y ese evento que lo tenga, y si no hay ninguno es el reloj.
// No es idempotente. Una petición incompleta es un error y no escribe nada.
func (s *MachineMemory) OpenReanalysis(ctx context.Context, req intake.ReanalysisRequest) (string, error) {
	panic(pendiente.Implementar("intakehelpertest.MachineMemory.OpenReanalysis"))
}
