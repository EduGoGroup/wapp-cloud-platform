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
// solicitud, su contexto y EL SOBRE DE LA PETICIÓN (una copia), sin referencias, y devuelve su id.
// La fila nace con su sobre en el mismo acto: Divergencia deliberada del viejo (D-F7-9, D-F8-13),
// T8.40. `message_ts` se copia del job MÁS ANTIGUO de ese tenant y ese evento que lo tenga, y si
// no hay ninguno es el reloj. No es idempotente.
//
// Se rechaza sin escribir nada, y en este orden: la petición incompleta, y después el sobre A
// MEDIAS (ni completo ni vacío entero), cada una con el MISMO texto que intake.Postgres. Con el
// sobre vacío entero el job nace sin sobre.
func (s *MachineMemory) OpenReanalysis(_ context.Context, req intake.ReanalysisRequest) (string, error) {
	if !req.Valid() {
		return "", fmt.Errorf("intake: solicitud de re-análisis incompleta (ventana=%t intake=%t dueño=%t)",
			req.Key.Valid(), req.IntakeID != "", req.Context.IsFromOwner())
	}
	var env intake.SourceText
	switch in := req.SourceText; {
	case in.Complete():
		env = intake.SourceText{Enc: append([]byte(nil), in.Enc...), DEK: append([]byte(nil), in.DEK...), KEKID: in.KEKID}
	case !in.Empty():
		return "", fmt.Errorf("intake: sobre del literal incompleto (enc=%d dek=%d kek_id=%t): son las tres o ninguna",
			len(in.Enc), len(in.DEK), in.KEKID != "")
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
		SourceText: env,
		Artifacts:  map[string]json.RawMessage{},
		CreatedAt:  now, UpdatedAt: now, NextAttemptAt: now,
	}
	s.rows = append(s.rows, row)
	return row.ID, nil
}
