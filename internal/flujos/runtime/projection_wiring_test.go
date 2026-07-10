package runtime_test

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/cart"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/modules/survey"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/runtime"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
)

// persistSinkWith construye el PersistSink con los proyectores por-módulo (cart +
// survey), como hace el arranque real (main.go), para que los tests que verifican la
// proyección (orders/order_items/survey_results) sigan escribiendo las MISMAS filas
// tras extraer la proyección a los módulos (Plan 027 · Ola 3 · T8). Solo cambia el
// CABLEADO del sink; las expectativas de cada test son idénticas.
func persistSinkWith(repo store.Repository) *runtime.PersistSink {
	return runtime.NewPersistSink(repo, cart.NewProjector(repo), survey.NewProjector(repo))
}

// cartResumeOpt registra la ResumePolicy del carrito (TTL perezoso + auto-reinicio +
// siembra de page_size), como el arranque real, para los tests del runtime que
// ejercen la reanudación del carrito.
func cartResumeOpt(repo store.Repository) runtime.Option {
	return runtime.WithResumePolicy(cart.NodeTypeCart, cart.NewResumePolicy(repo))
}
