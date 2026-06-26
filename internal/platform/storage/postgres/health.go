package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/EduGoGroup/wapp-shared/health"
)

// healthCheckTimeout acota el PingContext del health check de BD.
const healthCheckTimeout = 5 * time.Second

// HealthCheck implementa health.HealthCheck (wapp-shared) verificando la
// conectividad con PostgreSQL mediante PingContext.
type HealthCheck struct {
	db *sql.DB
}

// NewHealthCheck construye el health check de BD sobre el pool dado.
func NewHealthCheck(db *sql.DB) *HealthCheck {
	return &HealthCheck{db: db}
}

// Name devuelve el identificador del componente chequeado.
func (h *HealthCheck) Name() string { return "postgres" }

// Check hace ping a la BD con timeout y traduce el resultado a CheckResult:
// StatusHealthy si responde, StatusUnhealthy con el error en caso contrario.
func (h *HealthCheck) Check(ctx context.Context) health.CheckResult {
	pingCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()

	res := health.CheckResult{
		Component: h.Name(),
		Timestamp: time.Now().UTC(),
	}

	if err := h.db.PingContext(pingCtx); err != nil {
		res.Status = health.StatusUnhealthy
		res.Message = err.Error()
		return res
	}

	res.Status = health.StatusHealthy
	res.Message = "ping ok"
	return res
}
