package publicapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/tenantvars"
)

// Límites de FORMA del transporte (defensa DoS), NO interpretación del contenido
// (D-041.1). Acotan cuántas variables y cuán larga es una clave; el VALOR solo lo
// acota el tamaño del cuerpo, porque un valor legítimo puede ser largo (una
// plantilla, un JSON serializado como cadena).
const (
	maxTenantVariablesBytes = 1 << 18 // 256 KiB de cuerpo
	maxTenantVariables      = 500     // claves por tenant
	maxTenantVariableKeyLen = 200     // bytes de una clave
)

// TenantVariableStore es el puerto MÍNIMO de las variables de empresa que la API
// pública consume. Lo satisfacen *tenantvars.Postgres y *tenantvars.MemoryStore.
// TODAS las operaciones van acotadas al tenant (INV-8): el tenant sale del token,
// nunca del cuerpo ni de un parámetro.
type TenantVariableStore interface {
	List(ctx context.Context, tenantID string) ([]tenantvars.Variable, error)
	Replace(ctx context.Context, tenantID string, vars map[string]string) error
}

// tenantVariablesDTO es el contrato de GET y de la respuesta del PUT: el mapa
// clave→valor tal cual está guardado, más la marca del cambio MÁS RECIENTE (se
// omite si el tenant no tiene variables). El envoltorio `variables` —en vez del
// mapa desnudo— deja sitio a campos futuros sin romper a los clientes.
type tenantVariablesDTO struct {
	Variables map[string]string `json:"variables"`
	UpdatedAt string            `json:"updated_at,omitempty"`
}

// tenantVariablesRequest es el cuerpo del PUT. El puntero distingue "no mandaste
// el campo" (error: el cliente probablemente se equivocó de forma) de "mandaste el
// conjunto vacío" (intención explícita de dejar al tenant sin variables).
type tenantVariablesRequest struct {
	Variables *map[string]string `json:"variables"`
}

// getTenantVariablesHandler devuelve GET /api/v1/tenant-variables: las variables
// de empresa del tenant del token (INV-8), VERBATIM. Un tenant sin variables
// responde 200 con el mapa vacío —no 404—: "no tengo ninguna" es una respuesta,
// no un fallo. 401 sin identidad; 504 si la lectura agota su plazo de BD; 500 en
// fallo del store.
//
// dbTimeout acota la lectura (Plan 050 · Ola 3 · T3.3, ver dbCtx en publicapi.go).
// <=0 cae al suelo de dbCtx. El PUT hermano queda FUERA: reemplaza el conjunto
// entero en una transacción, y 1,5s está calibrado para una lectura.
func getTenantVariablesHandler(vs TenantVariableStore, dbTimeout time.Duration, log sharedlogger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if vs == nil {
			writeError(w, http.StatusInternalServerError, "store de variables no configurado")
			return
		}
		ctx, cancel := dbCtx(r.Context(), dbTimeout)
		defer cancel()
		vars, err := vs.List(ctx, id.TenantID)
		if err != nil {
			if dbTimedOut504(w, log, err, "la lectura de las variables no respondió a tiempo, reintenta",
				"op", "tenant_variables.list", "tenant_id", id.TenantID) {
				return
			}
			writeError(w, http.StatusInternalServerError, "no se pudieron leer las variables")
			return
		}
		writeJSON(w, http.StatusOK, toTenantVariablesDTO(vars))
	})
}

// putTenantVariablesHandler devuelve PUT /api/v1/tenant-variables.
//
// SEMÁNTICA: **reemplaza el conjunto ENTERO** del tenant (no hace merge por
// clave). El cuerpo es la foto completa: lo que no venga, se borra. Tres razones,
// en orden de peso:
//
//  1. Es la ÚNICA forma de borrar una variable. El contrato son dos rutas —GET y
//     PUT—; sin DELETE, un upsert por clave dejaría al tenant sin poder quitar
//     nunca lo que puso (y la pantalla del BFF necesita quitar).
//  2. Es lo que PUT significa sobre una colección: sustituir el recurso por lo
//     enviado. Un merge sería PATCH, y no es la ruta que el diseño fijó.
//  3. El consumidor natural es una pantalla que ya tiene el conjunto completo a la
//     vista, y el Plan 042 congela ese conjunto completo en cada `intake.push`
//     (D-18): una foto coherente vale más que una acumulación de retoques.
//
// El precio, dicho sin rodeos: dos editores simultáneos se pisan (el último gana
// entero, y una variable que el otro acababa de añadir desaparece). Es asumible en
// una pantalla de administración del propio tenant, y el `updated_at` que solo se
// mueve al cambiar de verdad deja rastro de qué cambió.
//
// wApp NO interpreta claves ni valores (D-041.1): no se valida que `moneda` sea
// una moneda, no se tipa el valor, no hay lista blanca. Lo único que se comprueba
// es la FORMA (que sea JSON, que la clave no venga vacía y los límites de tamaño).
// Escritura auditada sin PII (content.write). Respuestas:
//
//   - 200 con el conjunto resultante (misma forma que el GET).
//   - 400 si el cuerpo no es JSON, no trae `variables`, o una clave es vacía/larga.
//   - 401 sin identidad; 413 si el cuerpo excede el límite; 500 en fallo del store.
func putTenantVariablesHandler(vs TenantVariableStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if vs == nil {
			writeError(w, http.StatusInternalServerError, "store de variables no configurado")
			return
		}
		vars, code, errBody := decodeTenantVariables(r.Body)
		if errBody != nil {
			writeJSON(w, code, errBody)
			return
		}
		if err := vs.Replace(r.Context(), id.TenantID, vars); err != nil {
			writeError(w, http.StatusInternalServerError, "no se pudieron guardar las variables")
			return
		}
		saved, err := vs.List(r.Context(), id.TenantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "variables guardadas, pero no se pudieron releer")
			return
		}
		writeJSON(w, http.StatusOK, toTenantVariablesDTO(saved))
	})
}

// decodeTenantVariables lee y valida la FORMA del cuerpo del PUT. Devuelve el
// conjunto y, si algo falla, el status + el cuerpo de error ya armado (nil = todo
// bien). Se extrae del handler para que la validación se lea de un vistazo y no
// infle su complejidad ciclomática.
//
// El cuerpo es `any` —y no un string— por lo mismo que en decodeImportBody: el
// techo de bytes responde {error, max_bytes} (criterio único del 413, limits.go)
// y el resto de defectos, el {error} de siempre.
func decodeTenantVariables(body io.Reader) (map[string]string, int, any) {
	raw, err := io.ReadAll(io.LimitReader(body, maxTenantVariablesBytes+1))
	if err != nil {
		return nil, http.StatusBadRequest, errorBody("no se pudo leer el cuerpo")
	}
	if len(raw) > maxTenantVariablesBytes {
		return nil, http.StatusRequestEntityTooLarge, tooLarge("el cuerpo", maxTenantVariablesBytes)
	}
	var req tenantVariablesRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, http.StatusBadRequest, errorBody("el cuerpo debe ser un JSON {\"variables\":{clave:valor}} de cadenas")
	}
	if req.Variables == nil {
		return nil, http.StatusBadRequest, errorBody("falta el objeto variables (usa {} para dejar el tenant sin variables)")
	}
	vars := *req.Variables
	if len(vars) > maxTenantVariables {
		return nil, http.StatusBadRequest, errorBody("demasiadas variables")
	}
	for k := range vars {
		if k == "" {
			return nil, http.StatusBadRequest, errorBody("hay una clave vacía")
		}
		if len(k) > maxTenantVariableKeyLen {
			return nil, http.StatusBadRequest, errorBody("hay una clave demasiado larga")
		}
	}
	return vars, 0, nil
}

// toTenantVariablesDTO arma la respuesta: el mapa clave→valor y el updated_at MÁS
// reciente del conjunto (vacío si no hay variables). El mapa se devuelve siempre
// inicializado para que el cliente reciba `{}` y no `null`.
func toTenantVariablesDTO(vars []tenantvars.Variable) tenantVariablesDTO {
	out := tenantVariablesDTO{Variables: make(map[string]string, len(vars))}
	var latest time.Time
	for _, v := range vars {
		out.Variables[v.Key] = v.Value
		if v.UpdatedAt.After(latest) {
			latest = v.UpdatedAt
		}
	}
	if !latest.IsZero() {
		out.UpdatedAt = latest.UTC().Format(rfc3339)
	}
	return out
}
