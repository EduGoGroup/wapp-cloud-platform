package intakes

import (
	"context"
)

// Service es la capa de dominio de las solicitudes: aplica las reglas (paginación
// acotada, normalización de estados, máquina de estados) sobre un Store. No sabe
// de HTTP y no toma decisiones de transporte; el handler traduce sus errores a
// códigos.
type Service struct {
	store Store
}

// NewService construye el servicio sobre el store dado.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// List devuelve la página de solicitudes del tenant que casan con el filtro, con
// el total de coincidencias sin paginar. Sanea la paginación (Filter.Normalized)
// antes de consultar: el llamante no puede pedir 100k filas de un golpe.
func (s *Service) List(ctx context.Context, tenantID string, f Filter) (Page, error) {
	f = f.Normalized()
	items, total, err := s.store.List(ctx, tenantID, f)
	if err != nil {
		return Page{}, err
	}
	if items == nil {
		items = []Intake{} // la UI itera sin ramificar por el nulo
	}
	return Page{Intakes: items, Page: f.Page, PageSize: f.PageSize, Total: total}, nil
}

// Get devuelve la solicitud con sus líneas. ErrNotFound si no es del tenant (404
// opaco, INV-8).
func (s *Service) Get(ctx context.Context, tenantID, intakeID string) (Detail, error) {
	return s.store.Get(ctx, tenantID, intakeID)
}

// SetStatus aplica una transición del ciclo de vida y devuelve la solicitud ya
// transicionada. El orden importa:
//
//  1. lee el estado actual (ErrNotFound si la solicitud no es del tenant): el
//     recurso se resuelve ANTES que el cuerpo, para no revelar por el código de
//     error si una solicitud ajena existe;
//  2. valida la transición contra la máquina de estados (*TransitionError con el
//     estado actual y los destinos permitidos, que el handler publica en el 422).
//     Un destino DESCONOCIDO cae por aquí sin caso aparte: no está en el mapa, así
//     que no es alcanzable desde ningún origen, y el llamante recibe la misma
//     respuesta útil — dónde está y adónde puede ir;
//  3. escribe con compare-and-swap sobre el estado leído (ErrConflict si otro
//     operador se adelantó entre 1 y 3).
//
// Los EFECTOS COLATERALES de la transición —plantilla de seña, notificación al
// cliente, fila de intake_revisions— NO se disparan aquí: llegan en la Ola 4. Esta
// operación solo persiste la transición válida.
func (s *Service) SetStatus(ctx context.Context, tenantID, intakeID, to string) (Intake, error) {
	to = NormalizeStatus(to)

	current, err := s.store.Get(ctx, tenantID, intakeID)
	if err != nil {
		return Intake{}, err
	}
	from := NormalizeStatus(current.Status)
	if !CanTransition(from, to) {
		return Intake{}, &TransitionError{From: from, To: to, Allowed: AllowedTransitions(from)}
	}

	return s.store.UpdateStatus(ctx, tenantID, intakeID, to, StoredVariants(from))
}
