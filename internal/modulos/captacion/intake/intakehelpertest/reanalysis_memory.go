package intakehelpertest

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/captacion/intake"
)

// reanalysis_memory.go es la mitad de MachineMemory que hace de SEGUNDO PRODUCTOR de jobs: las
// dos operaciones de intake.Postgres que no son de ningún puerto del paquete (LiveJobOfEvent y
// OpenReanalysis). Nuevo: el doble viejo no las tenía, porque el paquete viejo solo las probaba
// por el texto de su SQL. Están sobre el MISMO doble porque es la misma tabla: el job que abre
// OpenReanalysis es el que después reclama ClaimNext.

// LiveJobOfEvent implementa ReanalysisStore: el id del job de ese tenant y ese evento que no
// está en un estado terminal; si hay varios, el creado más tarde (el `ORDER BY created_at DESC
// LIMIT 1` de la sentencia). Sin tenant o sin evento es un error, no «no hay ninguno».
func (s *MachineMemory) LiveJobOfEvent(_ context.Context, tenantID, eventID string) (string, bool, error) {
	if tenantID == "" || eventID == "" {
		return "", false, fmt.Errorf("intake: hacen falta tenant y evento para preguntar por el job vivo")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var newest *Row
	for _, r := range s.rows {
		if r.Key.TenantID != tenantID || r.Key.EventID != eventID || intake.IsTerminal(r.Status) {
			continue
		}
		if newest == nil || r.CreatedAt.After(newest.CreatedAt) {
			newest = r
		}
	}
	if newest == nil {
		return "", false, nil
	}
	return newest.ID, true, nil
}

// OpenReanalysis implementa ReanalysisStore: inserta el job del re-análisis en `pending`, con su
// solicitud y su contexto, sin referencias ni sobre, y devuelve su id. `message_ts` se copia
// del job MÁS ANTIGUO de ese tenant y ese evento que lo tenga, y si no hay ninguno es el reloj.
// No es idempotente. Una petición incompleta es un error y no escribe nada.
func (s *MachineMemory) OpenReanalysis(_ context.Context, req intake.ReanalysisRequest) (string, error) {
	if !req.Valid() {
		return "", fmt.Errorf("intake: solicitud de re-análisis incompleta (ventana=%t intake=%t dueño=%t)",
			req.Key.Valid(), req.IntakeID != "", req.Context.IsFromOwner())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	messageTS := now
	var first *Row
	for _, r := range s.rows {
		if r.Key.TenantID != req.Key.TenantID || r.Key.EventID != req.Key.EventID || r.MessageTS.IsZero() {
			continue
		}
		if first == nil || r.CreatedAt.Before(first.CreatedAt) {
			first = r
		}
	}
	if first != nil {
		messageTS = first.MessageTS
	}
	// «No había revisión anterior» se guarda sin valor, como el NULL de `reanalyzed_from`.
	reanalysis := req.Context
	if reanalysis.From < 0 {
		reanalysis.From = 0
	}
	s.seq++
	row := &Row{
		ID: fmt.Sprintf("job-%03d", s.seq), Key: req.Key, Status: intake.StatusPending,
		MessageTS: messageTS, IntakeID: req.IntakeID, Reanalysis: reanalysis,
		Artifacts: map[string]json.RawMessage{},
		CreatedAt: now, UpdatedAt: now, NextAttemptAt: now,
	}
	s.rows = append(s.rows, row)
	return row.ID, nil
}
