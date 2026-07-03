package publicapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/httpapi"
)

// PresignUploader es el puerto MÍNIMO que la API pública consume para presignar una
// subida a R2 (subconjunto de objectstore.PresignClient, Plan 017). Lo satisface el
// *s3PresignClient real (y un doble en tests). Zero-knowledge (ADR-0007/0009): la
// plataforma solo entrega una URL PUT firmada de corta vida; NUNCA expone las
// credenciales de R2 al cliente, y el Edge/tercero descarga/sube sin llaves.
type PresignUploader interface {
	GenerateUploadURL(ctx context.Context, key string) (url string, expiresAt time.Time, err error)
}

// mediaKeyPrefix es el namespace de los objetos de wApp en el bucket compartido
// (edugo-materials); espeja el prefijo de Plan 017 ("wapp/media/…"). La API añade el
// tenant y un uuid para AISLAR el objeto por tenant (INV-8) y evitar colisiones.
const mediaKeyPrefix = "wapp/media"

// uploadURLRequest es el cuerpo de POST /api/v1/media/upload-url (design.md §8):
// filename (nombre visible del archivo) y mime (tipo de contenido). El tenant NO
// viaja aquí (INV-8): sale del token.
type uploadURLRequest struct {
	Filename string `json:"filename"`
	Mime     string `json:"mime"`
}

// uploadURLResponse es la respuesta (design.md §8): la URL prefirmada PUT, la key
// del objeto con la que luego se referencia el archivo en un flujo (misma forma que
// model.MediaRef.Key / content.key del nodo media) y el instante de expiración.
type uploadURLResponse struct {
	URL       string `json:"url"`
	Key       string `json:"key"`
	ExpiresAt string `json:"expires_at"`
}

// uploadURLHandler devuelve el handler de POST /api/v1/media/upload-url: mina una
// key R2 namespaceada por el tenant del token (INV-8) y presigna un PUT de corta
// vida contra ella (reusa el PresignClient del Plan 017). La `key` devuelta es
// EXACTAMENTE la que el autor coloca en content.key de un nodo media (o en un blob
// de tenant_content) y que el runtime presigna VERBATIM para descargar. Respuestas:
//
//   - 200 con {url, key, expires_at} al firmar.
//   - 400 si el JSON es inválido o falta filename/mime.
//   - 401 sin identidad; 502 si el presign de R2 falla; 500 si no hay almacén.
func uploadURLHandler(uploader PresignUploader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httpapi.IdentityFromContext(r.Context())
		if !ok || id.TenantID == "" {
			writeError(w, http.StatusUnauthorized, "autenticación requerida")
			return
		}
		if uploader == nil {
			writeError(w, http.StatusInternalServerError, "almacén de objetos no configurado")
			return
		}

		var req uploadURLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "cuerpo JSON inválido")
			return
		}
		req.Filename = strings.TrimSpace(req.Filename)
		req.Mime = strings.TrimSpace(req.Mime)
		if req.Filename == "" || req.Mime == "" {
			writeError(w, http.StatusBadRequest, "filename y mime son requeridos")
			return
		}

		key := mediaObjectKey(id.TenantID, req.Filename)
		url, expiresAt, err := uploader.GenerateUploadURL(r.Context(), key)
		if err != nil {
			writeError(w, http.StatusBadGateway, "no se pudo presignar la subida")
			return
		}
		writeJSON(w, http.StatusOK, uploadURLResponse{
			URL:       url,
			Key:       key,
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		})
	})
}

// mediaObjectKey construye la key R2 del objeto: wapp/media/<tenant_id>/<uuid>-<safe>.
// El segmento tenant_id namespacea el objeto por tenant (INV-8) y el uuid lo hace no
// adivinable. La key devuelta es la que el runtime presigna sin transformar (Plan
// 017: sendMedia pasa ref.Key verbatim a GenerateDownloadURL), así que su forma
// coincide con lo que el Motor/Edge espera para descargar.
func mediaObjectKey(tenantID, filename string) string {
	return mediaKeyPrefix + "/" + tenantID + "/" + uuid.NewString() + "-" + sanitizeFilename(filename)
}

// sanitizeFilename reduce el nombre a su base y sustituye lo no seguro por '_'
// (evita separadores/traversal en la key R2). Vacío/degenerado → "file".
func sanitizeFilename(name string) string {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." {
		return "file"
	}
	var b strings.Builder
	for _, ch := range base {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '.', ch == '-', ch == '_':
			b.WriteRune(ch)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "file"
	}
	return out
}
