package runtime_test

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intakes"
)

// sinEnvío es un ShippingEnsurer que no hace nada. Estos tests miran las filas que
// proyecta el carrito (intakes/intake_items del repositorio de flujos), y la línea
// de envío se decide en el dominio de solicitudes —otro almacén— con sus propios
// tests (Plan 041 · T4.3). Un nil aquí haría estallar el cierre.
type sinEnvío struct{}

func (sinEnvío) EnsureShippingLine(context.Context, string, string, intakes.ShippingPolicy) error {
	return nil
}

// persistSinkWith construye el PersistSink con los proyectores por-módulo (cart +
// survey), como hace el arranque real (main.go), para que los tests que verifican la
// proyección (intakes/intake_items/survey_results) sigan escribiendo las MISMAS filas
// tras extraer la proyección a los módulos (Plan 027 · Ola 3 · T8). Solo cambia el
// CABLEADO del sink; las expectativas de cada test son idénticas.
//
// El escritor de revisiones va en memoria: estos tests miran la proyección a
// intakes/intake_items, no la revisión, pero el carrito ya no cierra sin dejarla y
// pasarle un nil lo haría estallar (ADR-0031 §3, Plan 041 · T4.1).
func persistSinkWith(repo store.Repository) *runtime.PersistSink {
	return runtime.NewPersistSink(repo,
		cart.NewProjector(repo, intakes.NewMemoryStore(), sinEnvío{}),
		survey.NewProjector(repo))
}

// cartResumeOpt registra la ResumePolicy del carrito (auto-reinicio tras nivel
// terminal + siembra de page_size), como el arranque real, para los tests del
// runtime que ejercen la reanudación del carrito. El TTL perezoso que también
// gobernaba se derogó en T4.7 (D-041.16).
func cartResumeOpt(repo store.Repository) runtime.Option {
	return runtime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(repo))
}
