package publicapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	sharedintents "github.com/EduGoGroup/wapp-shared/intents"
	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// IntentConfigStore es el puerto de persistencia del blob de intents por tenant que
// la API pública consume. Lo satisface *intentcfg.PostgresStore (y el MemoryStore en
// tests). Toda operación va acotada al tenant del token (INV-8).
type IntentConfigStore interface {
	Get(ctx context.Context, tenantID string) (intentcfg.Config, error)
	Upsert(ctx context.Context, tenantID, version string, blob []byte) error
}

// FeatureChecker responde si el tenant tiene una feature habilitada (ADR-0022). Lo
// satisface *entitlements.Postgres (y el Fake en tests). Es el gate de VERDAD del
// PUT (sin la feature ⇒ 403).
type FeatureChecker interface {
	Has(ctx context.Context, tenantID, feature string) (bool, error)
}

// ConfigPusher empuja un ConfigUpdate (ADR-0021) a las sesiones vivas del tenant
// tras un PUT. Lo satisface *gatewaygrpc.Server (PushConfig). Es best-effort: un
// fallo de push NO invalida el PUT (la config ya quedó persistida y el push al
// conectar reconcilia).
type ConfigPusher interface {
	PushConfig(ctx context.Context, tenantID, kind, version string, payload []byte) error
}

// intentConfigResponse es la respuesta de GET /api/v1/intents: la version de
// entidad + el blob de config crudo (verbatim, ya validado al persistir).
type intentConfigResponse struct {
	Version string          `json:"version"`
	Config  json.RawMessage `json:"config"`
}

// getIntentsHandler devuelve GET /api/v1/intents: el blob de intents del tenant del
// token (INV-8) con su version de entidad. 200 con {version, config}; 404 si el
// tenant no tiene config; 401 sin identidad; 500 en fallo del store.
func getIntentsHandler(store IntentConfigStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if store == nil {
			writeError(w, http.StatusInternalServerError, "store de intents no configurado")
			return
		}
		cfg, err := store.Get(r.Context(), id.TenantID)
		if err != nil {
			if errors.Is(err, intentcfg.ErrNotFound) {
				writeError(w, http.StatusNotFound, "el tenant no tiene config de intents")
				return
			}
			writeError(w, http.StatusInternalServerError, "no se pudo leer la config de intents")
			return
		}
		writeJSON(w, http.StatusOK, intentConfigResponse{Version: cfg.Version, Config: cfg.Blob})
	})
}

// putIntentsHandler devuelve PUT /api/v1/intents: valida el blob con
// wapp-shared/intents (ParseAndValidate), exige la feature llm_intent (gate de
// verdad, ADR-0022 ⇒ 403 sin ella), fija la version de entidad (hash del blob
// normalizado), persiste y empuja el ConfigUpdate a las sesiones vivas del tenant
// (ADR-0021). El tenant SIEMPRE sale del token (INV-8). Respuestas:
//
//   - 200 con {version} al persistir (+ push best-effort).
//   - 400 si el cuerpo no es un contrato de intents válido.
//   - 401 sin identidad; 403 sin la feature; 413 si excede el tamaño del contrato;
//     500 en fallo del store.
func putIntentsHandler(store IntentConfigStore, ents FeatureChecker, pusher ConfigPusher, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if store == nil || ents == nil {
			writeError(w, http.StatusInternalServerError, "API de intents no configurada")
			return
		}

		// Gate de VERDAD (ADR-0022): sin la feature, la superficie de administración
		// se rechaza (403). Un fallo del checker se trata como sin la feature (no se
		// abre la capacidad por un error transitorio).
		has, err := ents.Has(r.Context(), id.TenantID, entitlements.FeatureLLMIntent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo verificar el entitlement")
			return
		}
		if !has {
			writeError(w, http.StatusForbidden, "el plan del tenant no incluye la clasificación de intenciones")
			return
		}

		// Cortafuegos de tamaño ANTES de leer todo: el contrato acota el blob a
		// MaxConfigBytes (wapp-shared/intents). +1 detecta el exceso.
		body, err := io.ReadAll(io.LimitReader(r.Body, sharedintents.MaxConfigBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "no se pudo leer el cuerpo")
			return
		}
		if len(body) > sharedintents.MaxConfigBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "la config excede el tamaño máximo")
			return
		}

		// Validación del contrato (ParseAndValidate): nombres únicos/kebab, >=1
		// ejemplo por intent, umbral en rango, etc. Config inválida ⇒ 400.
		if _, verr := sharedintents.ParseAndValidate(body); verr != nil {
			writeError(w, http.StatusBadRequest, "config de intents inválida: "+verr.Error())
			return
		}

		version, err := entityVersion(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "el cuerpo debe ser JSON válido")
			return
		}

		if err := store.Upsert(r.Context(), id.TenantID, version, body); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudo persistir la config de intents")
			return
		}

		// Push best-effort a las sesiones vivas del tenant (ADR-0021). Un fallo NO
		// invalida el PUT: la config ya está persistida y el push al conectar
		// reconcilia (hace de reintento; no hay reintentos aquí). El error se registra
		// pero no se propaga al cliente.
		if pusher != nil {
			if perr := pusher.PushConfig(r.Context(), id.TenantID, intentcfg.Kind, version, body); perr != nil && log != nil {
				log.Warn("intents: push de config best-effort falló (persistida; reconcilia al conectar)",
					"tenant_id", id.TenantID, "version", version, "error", perr)
			}
		}

		writeJSON(w, http.StatusOK, map[string]string{"version": version})
	})
}

// entityVersion calcula la version de ENTIDAD del blob: sha256 (12 hex) del JSON
// NORMALIZADO (re-serializado desde su forma decodificada, lo que ordena las claves
// de objeto y descarta el espacio en blanco insignificante). Así dos cuerpos con el
// mismo contenido lógico producen la misma version (idempotencia del push, ADR-0021).
func entityVersion(body []byte) (string, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	norm, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(norm)
	return hex.EncodeToString(sum[:])[:12], nil
}
