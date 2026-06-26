// Package httpapi expone los handlers HTTP de la Plataforma Cloud. En el corte
// T0 solo monta el endpoint de health sobre wapp-shared/health; el admin mínimo
// (revocar lease) entra en fases posteriores.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/EduGoGroup/wapp-shared/health"
)

// selfCheck es un health check trivial de liveness: si el proceso responde,
// está vivo. El check real de la base de datos entra en T1.
type selfCheck struct{}

// Name devuelve el identificador del componente chequeado.
func (selfCheck) Name() string { return "self" }

// Check devuelve siempre StatusHealthy (liveness básica).
func (selfCheck) Check(_ context.Context) health.CheckResult {
	return health.CheckResult{
		Status:    health.StatusHealthy,
		Component: "self",
		Message:   "liveness ok",
		Timestamp: time.Now().UTC(),
	}
}

// healthResponse es el cuerpo JSON del endpoint de health.
type healthResponse struct {
	Status health.Status                 `json:"status"`
	Checks map[string]health.CheckResult `json:"checks"`
}

// NewHealthChecker construye el Checker con los checks base del corte T0
// (solo liveness "self"). El check de PostgreSQL se registra en T1.
func NewHealthChecker() *health.Checker {
	c := health.NewChecker()
	c.Register(selfCheck{})
	return c
}

// HealthHandler devuelve un http.Handler que sirve el estado agregado del
// checker como JSON: 200 si está sano, 503 si algún check está unhealthy.
func HealthHandler(checker *health.Checker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		results := checker.CheckAll(ctx)

		status := health.StatusHealthy
		code := http.StatusOK
		if !checker.IsHealthy(ctx) {
			status = health.StatusUnhealthy
			code = http.StatusServiceUnavailable
		}

		body, err := json.Marshal(healthResponse{Status: status, Checks: results})
		if err != nil {
			http.Error(w, "encoding health response", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if _, werr := w.Write(body); werr != nil {
			return
		}
	})
}
