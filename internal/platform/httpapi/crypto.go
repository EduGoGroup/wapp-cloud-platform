package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// RekeyFunc ejecuta UNA pasada de rotación (re-wrap incremental) por batch y
// devuelve el Report. Es la abstracción mínima que el handler necesita: en
// producción la satisface un cierre de cmd/server que cablea db+cipher+kp a
// crypto.Rekey; en tests, un doble que devuelve un Report fijo. batch <= 0 usa el
// default de crypto.Rekey.
type RekeyFunc func(ctx context.Context, batch int) (crypto.Report, error)

// rekeyResponse es la respuesta JSON de /admin/crypto/rekey: contadores y key_ids
// del keyring, SIN contenido sensible (número/value/JID), §10.H.
type rekeyResponse struct {
	Processed      int            `json:"processed"`
	CurrentKeyID   string         `json:"current_key_id"`
	PendingByKeyID map[string]int `json:"pending_by_key_id"`
}

// CryptoRekeyHandler devuelve el handler del endpoint admin de rotación de KEK
// (re-wrap incremental, Plan 012 §7). Acepta POST y un tamaño de batch opcional
// por query (?batch=N; default el de crypto.Rekey). Ejecuta una pasada reanudable
// e idempotente y responde 200 con {processed, current_key_id, pending_by_key_id}.
// Ejecutarlo repetidamente hasta pending_by_key_id vacío deja la rotación completa;
// una KEK con 0 pendientes es retirable del keyring (§10.F).
//
// SEGURIDAD — auth DIFERIDA a la fase IAM: este endpoint INTERNO NO está
// autenticado. Debe exponerse solo en la red de administración (mismo http.Server
// de /healthz, no público). No montar de cara a Internet hasta que IAM añada
// autenticación/RBAC.
func CryptoRekeyHandler(rekey RekeyFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método no permitido (usar POST)", http.StatusMethodNotAllowed)
			return
		}

		batch := 0
		if q := r.URL.Query().Get("batch"); q != "" {
			n, err := strconv.Atoi(q)
			if err != nil || n < 0 {
				http.Error(w, "batch inválido (entero >= 0)", http.StatusBadRequest)
				return
			}
			batch = n
		}

		rep, err := rekey(r.Context(), batch)
		if err != nil {
			// El error de crypto.Rekey ya viene sin contenido sensible (§10.H); aun
			// así no lo reflejamos al cliente: mensaje genérico.
			http.Error(w, "no se pudo completar la rotación de KEK", http.StatusInternalServerError)
			return
		}

		pending := rep.PendingByKeyID
		if pending == nil {
			pending = map[string]int{}
		}
		body, err := json.Marshal(rekeyResponse{
			Processed:      rep.Processed,
			CurrentKeyID:   rep.CurrentKeyID,
			PendingByKeyID: pending,
		})
		if err != nil {
			http.Error(w, "codificando respuesta de rotación", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, werr := w.Write(body); werr != nil {
			return
		}
	})
}
