package content_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/content"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/conversacion/model"
)

// Las tres implementaciones del paquete cumplen el puerto (aserción de compilación).
var (
	_ content.Source = content.Static{}
	_ content.Source = (*content.JSON)(nil)
	_ content.Source = content.Router{}
)

// memStore es un content.Store en memoria: (tenant|ref) → blob. Con err, falla
// siempre. Anota cuántas veces se le consultó y con qué clave.
type memStore struct {
	blobs map[string][]byte
	err   error
	calls []string
}

func (s *memStore) GetTenantContent(_ context.Context, tenantID, ref string) ([]byte, error) {
	s.calls = append(s.calls, tenantID+"|"+ref)
	if s.err != nil {
		return nil, s.err
	}
	blob, ok := s.blobs[tenantID+"|"+ref]
	if !ok {
		return nil, errors.New("ref inexistente")
	}
	return blob, nil
}

func jsonRef(ref string) *model.ContentRef { return &model.ContentRef{Source: "json", Ref: ref} }

// invalidPrefix es el texto de model.ErrInvalidFlow más la envoltura de fmt.
const invalidPrefix = "definición de flujo inválida: "

// Toda Source del paquete devuelve sus fallos como error controlado envuelto en
// model.ErrInvalidFlow, sin pánico.
func TestSource_FailuresWrapErrInvalidFlow(t *testing.T) {
	failing := map[string]struct {
		source content.Source
		node   model.Node
	}{
		"json without content": {content.NewJSON(&memStore{}), model.Node{Type: model.NodeTypeMenu}},
		"router unknown source": {
			content.NewRouter(content.NewStatic(), content.NewJSON(&memStore{})),
			model.Node{Content: &model.ContentRef{Source: "http"}},
		},
	}
	for name, tc := range failing {
		t.Run(name, func(t *testing.T) {
			_, err := tc.source.Resolve(context.Background(), "t1", tc.node)
			if !errors.Is(err, model.ErrInvalidFlow) {
				t.Fatalf("error = %v, quiero uno envuelto en ErrInvalidFlow", err)
			}
		})
	}
}
