// Porta internal/reanalisis/reanalisis.go @ 56097aa (E-13: los escalones de lectura
// 1–8, `reanalisis.go:455-655` del viejo).

package reanalisis

import (
	"context"
	"fmt"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/inferencia/tenantllm"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/solicitudes/intakes"
)

// reanalisis_checks.go — LOS ESCALONES 1–8 DE Reanalyze: lo que se comprueba, en
// orden, antes de mirar el material. Ninguno escribe. Sin exportados: su contrato es
// el de Reanalyze.

// validateShape (antes `validarForma`) es el ESCALÓN 1-2: lo que se puede rechazar sin
// tocar la base.
//
// Va antes que cualquier gate porque el §8.1 lo dice con todas las letras para el
// `invalid_via` («Validación de forma, antes de cualquier gate») y porque el saneo
// del texto tiene que ocurrir ANTES de todo lo demás: el dedupe de T4.6 compara el
// hash del texto YA SANEADO, y sanearlo dos veces sería tener la regla en dos sitios.
func validateShape(req Request) (string, error) {
	if req.Via != "" && !tenantllm.ValidVia(req.Via) {
		return "", InvalidViaError{Via: req.Via}
	}
	return sanitize(req.Text)
}

// authorize (antes `autorizar`) es el ESCALÓN 3-6: el nivel, la vía y lo que cada vía
// exige. Devuelve la vía EFECTIVA con la que se va a abrir el job.
func (s *Service) authorize(ctx context.Context, req Request) (string, error) {
	if err := s.require(ctx, req.TenantID, entitlements.FeatureLLMIntake); err != nil {
		return "", err
	}
	via, cfg, err := s.resolveVia(ctx, req)
	if err != nil {
		return "", err
	}
	if via != tenantllm.ViaAPI {
		return via, nil
	}
	// 🔴 `api_llm` SOLO APARECE DESPUÉS DE ESTE `return`, y es un INVARIANTE del
	// ADR-0044 / D-044.28: esa clave gatea LA VÍA, no la capacidad. Un tenant con
	// `llm_intake` y sin `api_llm` es un tenant VÁLIDO en vía local y su re-análisis
	// tiene que funcionar entero. Preguntar por ella «por si acaso» más arriba es el
	// defecto que vigila TestReanalyze_LocalVia_NeverAsksForAPILLM.
	if err := s.require(ctx, req.TenantID, entitlements.FeatureAPILLM); err != nil {
		return "", err
	}
	if !credentialsComplete(cfg) {
		return "", CredentialsMissingError{Via: via}
	}
	return via, nil
}

// targetOf (antes `objetivoDe`) es el ESCALÓN 7-8: la solicitud existe y es de este
// tenant, cuelga de un evento, y no hay ya un job vivo sobre él.
//
// 🔴 LA GUARDA DEL JOB VIVO ES TAMBIÉN LA DE LA CARRERA. `aggregating` es un estado NO
// terminal, así que un re-análisis pedido en mitad de una ráfaga del cliente encuentra
// aquí la ventana abierta y sale por el 422 — que es la respuesta correcta: el material
// todavía se está escribiendo.
func (s *Service) targetOf(ctx context.Context, req Request) (intakes.ReanalysisTarget, error) {
	target, err := s.intakes.ReanalysisTargetOf(ctx, req.TenantID, req.IntakeID)
	if err != nil {
		return intakes.ReanalysisTarget{}, err
	}
	if target.EventID == "" {
		// Solicitud LEGADA pre-0054: no cuelga de ningún evento, así que no hay hilo
		// del que re-analizar. Es literalmente «no hay original guardado», o sea la
		// razón `never_stored` — y NO un 404: la solicitud existe y el dueño la está
		// mirando en la bandeja.
		return intakes.ReanalysisTarget{}, SourceUnavailableError{Reason: ReasonNeverStored}
	}
	jobID, live, err := s.jobs.LiveJobOfEvent(ctx, req.TenantID, target.EventID)
	if err != nil {
		return intakes.ReanalysisTarget{}, err
	}
	if live {
		return intakes.ReanalysisTarget{}, InProgressError{JobID: jobID}
	}
	return target, nil
}

// require (antes `exigir`) es el gate en-código de una feature, con la MISMA política
// que el middleware HTTP de entitlements: FAIL-CLOSED en los tres modos de
// no-resolución. Un resolver caído responde «no la tienes», no 500 — el llamante no
// debe poder distinguir «no lo tienes» de «no pude averiguarlo», porque un 5xx
// invita a reintentar hasta colarse.
func (s *Service) require(ctx context.Context, tenantID, feature string) error {
	has, err := s.features.Has(ctx, tenantID, feature)
	if err != nil || !has {
		return FeatureMissingError{Feature: feature}
	}
	return nil
}

// resolveVia (antes `resolverVia`) decide la vía EFECTIVA de este re-análisis y
// devuelve, de paso, la configuración del tenant (que la rama `api` necesita para la
// credencial).
//
// # 🔴 `via` AFIRMA, NO CONMUTA (REQ-33, design §8.1; RATIFICADO POR JHOAN, D-044.51)
//
// El contrato lo dice con estas palabras: «El campo se conserva en el cuerpo para
// poder AFIRMAR la vía —y que el servidor rechace si no coincide—, NO para
// conmutarla».
//
// LA REGLA ES UNA SOLA: **`via` tiene que coincidir con la vía EFECTIVA, siempre**.
// De ella salen los tres casos, y ninguno es una excepción:
//
//  1. `via` OMITIDA ⇒ la efectiva, sin más. Y SIN FILA en `tenant_llm` la efectiva
//     es `local` (D-044.48 §4): no es un default cómodo, es la vía que corre y está
//     probada, la que no necesita credencial, y el estado de todo tenant nuevo.
//  2. `via` PRESENTE y fuera del vocabulario ⇒ 400, ya rechazado antes de llegar
//     aquí (es forma, no configuración).
//  3. `via` PRESENTE y distinta de la efectiva ⇒ 400 `invalid_via`. **También —y
//     sobre todo— cuando el tenant NO tiene fila.**
//
// 🔧 EL PUNTO 3 LLEVABA UN `hayFila &&` Y ERA UN DEFECTO, corregido el 2026-08-27.
// Desactivaba la regla EXACTAMENTE en el caso universal: sin fila, `{"via":"api"}` no
// daba 400 — se convertía en la vía efectiva y caía al 422 de credencial. La causa de
// raíz es que «sin fila» se estaba tratando como AUSENCIA de vía cuando D-044.48 §4 la
// define como un valor REAL (`local`), y contra un valor real `"api"` es una
// contradicción como cualquier otra.
//
// 🔴 Y EL DEFAULT NO PUEDE SER `api`: mandaría el texto de un cliente a un tercero de
// pago POR OMISIÓN. El default es la vía que no llama a nadie.
func (s *Service) resolveVia(ctx context.Context, req Request) (string, tenantllm.Config, error) {
	cfg, found, err := s.config.Get(ctx, req.TenantID)
	if err != nil {
		return "", tenantllm.Config{}, fmt.Errorf("reanalisis: leer la configuración LLM del tenant: %w", err)
	}

	// La vía EFECTIVA. `found` se consume AQUÍ y no vuelve a salir de esta función:
	// a partir de esta línea «no hay fila» ya no es un estado aparte, es `local`. Esa
	// es la lección del defecto de arriba — mientras el booleano siga circulando,
	// alguien lo vuelve a meter en una guarda.
	effective := tenantllm.ViaLocal
	if found && cfg.Via != "" {
		effective = cfg.Via
	}
	if req.Via != "" && req.Via != effective {
		return "", tenantllm.Config{}, InvalidViaError{Via: req.Via, Configured: effective}
	}
	return effective, cfg, nil
}

// credentialsComplete (antes `credencialCompleta`) dice si el tenant puede llamar al
// proveedor externo. Es PURA y vive aparte para poder probar los dos «no» sin montar
// nada: con fila pero sin clave, y con clave pero sin consentimiento.
//
// 🔴 EL CONSENTIMIENTO CUENTA IGUAL QUE LA CLAVE (ADR-0030 D-01/§4). Una fila con
// credencial y sin `consented_at` es un tenant que dejó la clave preparada y NO
// autorizó que el texto de sus clientes salga hacia un tercero. Mandarlo igual sería
// consentir en su nombre.
//
// 🔧 TENÍA UN TERCER «no» —`hayFila`— Y SE RETIRÓ CON EL ARREGLO DE resolveVia. Era
// una guarda sobre un camino MUERTO: solo se llega aquí con la vía efectiva en `api`,
// y la efectiva solo vale `api` si hay fila.
func credentialsComplete(cfg tenantllm.Config) bool {
	return cfg.HasAPIKey && !cfg.ConsentedAt.IsZero()
}

// sanitize (antes `sanear`) pasa el texto pegado por LA MISMA puerta que el texto
// libre del cliente (`intakes.SanitizeNote`, antes `cart.SanitizeNote`; 041 REQ-33e /
// D-041.19) y traduce su rechazo a un 400.
//
// 🔴 ES LA MISMA FUNCIÓN Y NO UNA COPIA, y su propia cabecera lo exige: el pipeline LLM
// del Plan 044 escribe esas MISMAS columnas por otra puerta y debe llamar a ESA
// función, no copiar la regla. Con dos saneos, la columna tendría dos contratos y
// ninguno sería verdad.
//
// ⚠️ HEREDA EL TOPE DE 280 RUNAS, Y ESO APRIETA AQUÍ. `MaxNoteRunes` se calibró para
// una INDICACIÓN de cocina («sin cebolla»), y lo que entra por `/reanalyze` es una
// TRANSCRIPCIÓN. Se aplica igual y se rechaza en vez de truncar, por la misma razón
// que allí: recortar «…y sin maní» pierde el final, y el final es donde va el
// alérgeno. Quien vaya a subir el tope: es una decisión de producto y el sitio es
// `solicitudes/intakes/note.go`, no este fichero.
//
// Un texto que sanea a VACÍO no es un error: equivale a no haber mandado `text`, y
// se trata igual (no se persiste nada y el origen sigue siendo el hilo).
func sanitize(text string) (string, error) {
	if text == "" {
		return "", nil
	}
	clean, err := intakes.SanitizeNote(text)
	if err != nil {
		// Se envuelve para que el transporte pueda leer el NoteTooLongError con
		// errors.As y decirle al dueño cuántas runas sobran. Ese error NO cita el
		// texto, solo su longitud.
		return "", fmt.Errorf("reanalisis: el texto pegado no pasa el saneo: %w", err)
	}
	return clean, nil
}
