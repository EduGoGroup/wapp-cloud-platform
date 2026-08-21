// Package filtercfg construye el kind:"filters" que la nube empuja al Edge por
// ConfigUpdate (ADR-0021, ADR-0027; Plan 046 · Ola 2 · T2.1).
//
// Es el gemelo de internal/intentcfg para el otro kind, con UNA diferencia de fondo:
// intents tiene una tabla propia donde el dueño escribe un blob; filters NO tiene
// almacén propio. Su fuente de verdad es la columna `fleet_sessions.profile`, que ya
// escribe el POST /sessions/{id}/profile, y este paquete solo la PROYECTA al formato
// del cable. Por eso aquí no hay Upsert ni store: hay una lectura y un armado.
//
// # El contrato del payload (D-046.2), que NO se re-abre
//
//	{"version": <int64>, "sessions": {"<session_id>": {"profile": "active"|"passive"}}}
//
// El Edge se escribió contra este mismo contrato EN PARALELO: cualquier desviación
// —una clave renombrada, un `profile` que salga como número, un mapa recortado— no da
// error en ningún lado, simplemente deja de filtrar o filtra de más.
//
// # Las tres reglas que gobiernan este paquete
//
//  1. NO se gatea por entitlement. `passive_profiles` está declarada
//     (0039_seed_plan_taxonomy.sql) y NO gatea en v1. Si este provider consultara
//     entitlements.Has, un tenant sin el add-on subiría a la nube el tráfico de sus
//     sesiones pasivas — exactamente el fallo que el Plan 046 viene a cerrar. No hay
//     ni un import de entitlements aquí, y es a propósito.
//  2. El payload se manda SIEMPRE, aunque el tenant no tenga ni una sesión pasiva. Un
//     mapa todo-`active` ES información: es lo que hace CONVERGER al Edge cuando una
//     sesión deja de ser pasiva. Devolver nil «porque no hay nada que filtrar» dejaría
//     al Edge con el mapa anterior y una sesión reactivada seguiría muda.
//  3. El Gateway trata el payload como OPACO y no conoce los kinds: los aporta el
//     provider. Por eso la constante Kind vive aquí y NO en internal/gateway/grpc.
package filtercfg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/fleet"
)

// Kind es el espacio de nombres de la config de filtros en el ConfigUpdate
// (ADR-0021). Literal EXACTO acordado con el Edge: un duplicado del string en el
// otro proceso falla en SILENCIO (el Edge ignora los kinds no registrados con un log
// tolerante), así que se referencia esta constante y no se re-escribe la palabra.
const Kind = "filters"

// SessionFilter es lo que el mapa guarda por sesión. Hoy solo el perfil; es un OBJETO
// y no un string suelto para que mañana se le pueda añadir un campo sin romper al
// Edge (un lector de objeto ignora las claves que no conoce; un lector de string se
// rompe entero).
type SessionFilter struct {
	// Profile es "active" o "passive": los valores literales de la columna
	// fleet_sessions.profile. Nada más entra aquí.
	Profile string `json:"profile"`
}

// Payload es el cuerpo JSON del kind:"filters".
//
// 🔴 Sessions incluye SIEMPRE todas las sesiones del tenant, activas incluidas,
// porque el contrato dice que una sesión AUSENTE del mapa el Edge la asume `active`
// (fail-open: un Edge jamás pierde tráfico por una config incompleta). Omitir las
// activas «porque se asumen» funcionaría hoy y mentiría mañana: la sesión que pasa de
// pasiva a activa se quedaría con el `passive` viejo.
type Payload struct {
	// Version es el mismo entero que viaja como texto en ConfigPayload.Version.
	Version int64 `json:"version"`
	// Sessions mapea session_id → filtro. Nunca nil: un tenant sin sesiones manda
	// `{}` (mapa vacío), que significa «ninguna restricción», y no `null`.
	Sessions map[string]SessionFilter `json:"sessions"`
}

// Source es el puerto de LECTURA de este paquete: la foto del eje `profile` del
// tenant entero. Lo satisface *fleet.PostgresRepository (y *fleet.MemoryRepository
// para los tests sin BD).
//
// Es un puerto propio y NO fleet.Repository entero por interfaz-segregación: aquí
// solo se lee, y depender del contrato de escritura de la flota obligaría a todo
// doble de prueba a implementar quince métodos que no se usan.
type Source interface {
	ProfilesByTenant(ctx context.Context, tenantID string) (fleet.TenantProfiles, error)
}

// ConfigPusher es el puerto de SALIDA: el fan-out de un ConfigUpdate a las sesiones
// vivas del tenant. Lo satisface *gatewaygrpc.Server (PushConfig). Se declara aquí
// —y no se importa el tipo del gateway— para que este paquete no dependa de gRPC ni
// del contrato CloudLink: lo único que necesita es «alguien que sepa empujar».
type ConfigPusher interface {
	PushConfig(ctx context.Context, tenantID, kind, version string, payload []byte) error
}

// Build proyecta la foto del tenant al par (version, payload) del cable.
//
// version es la representación DECIMAL del mismo entero que va dentro del JSON: no un
// hash, no un UUID. El frame lo transporta como string (ConfigPayload.Version) pero
// el Edge lo compara como NÚMERO para descartar versiones viejas, así que las dos
// mitades tienen que ser el mismo valor o la comparación no significa nada.
//
// Un perfil que no sea active|passive se proyecta como "passive". No debería ocurrir
// —la columna tiene CHECK— pero si ocurriera, emitir el valor crudo haría que el
// validador del Edge rechazara el payload ENTERO y se quedara con el last-known-good
// de todas las sesiones. Degradar solo esa sesión al valor seguro («no auto-responde»)
// es estrictamente mejor que perder el mapa completo.
func Build(tp fleet.TenantProfiles) (version string, payload []byte, err error) {
	p := Payload{Version: tp.Version, Sessions: make(map[string]SessionFilter, len(tp.Sessions))}
	for sessionID, profile := range tp.Sessions {
		if !fleet.ValidProfile(profile) {
			profile = fleet.ProfilePassive
		}
		p.Sessions[sessionID] = SessionFilter{Profile: string(profile)}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", nil, fmt.Errorf("filtercfg: serializar payload: %w", err)
	}
	return strconv.FormatInt(tp.Version, 10), raw, nil
}

// ForTenant es la lectura + armado completos de un tenant: lo que necesitan LAS DOS
// vías que existen para que la config llegue al Edge —el provider al conectar
// (bootstrap.filtersConfigProvider) y el hook en caliente (Pusher, más abajo)—.
//
// Vive aquí, y las dos vías la llaman, para que no puedan divergir: encender una sola
// de las dos, o armarlas distinto, deja una vía muda que NO da ningún rojo (los tests
// de cada vía pasan por separado). Es la trampa que este plan ya se comió una vez.
//
// Un fallo se PROPAGA (no se empuja config a medias): el llamante lo loguea y no
// empuja nada, y el Edge conserva su last-known-good, que es la degradación correcta.
// Un tenant sin sesiones NO es un fallo: devuelve su versión 0 y el mapa vacío, y se
// empuja igual (regla 2).
func ForTenant(ctx context.Context, src Source, tenantID string) (version string, payload []byte, err error) {
	tp, err := src.ProfilesByTenant(ctx, tenantID)
	if err != nil {
		return "", nil, fmt.Errorf("filtercfg: leer perfiles del tenant: %w", err)
	}
	return Build(tp)
}

// Pusher adapta el cambio de perfil recién persistido a un ConfigUpdate del
// kind:"filters" hacia las sesiones vivas del tenant. Implementa
// flowadmin.ProfilePusher, y es el hook que T1.2 dejó APAGADO (cableado a nil) y que
// T2.1 enciende.
//
// 🔴 Best-effort, como su puerto manda: un fallo del push NO invalida la escritura ni
// cambia el código de respuesta del POST. El perfil ya está persistido y el push al
// conectar reconcilia. Aquí eso se cumple SOLO: PushConfig del Gateway devuelve nil
// siempre a propósito, y de esta función solo salen errores de LECTURA o de armado —
// que el handler loguea y descarta.
type Pusher struct {
	src  Source
	push ConfigPusher
}

// NewPusher construye el hook sobre la fuente de perfiles y el fan-out del Gateway.
func NewPusher(src Source, push ConfigPusher) *Pusher {
	return &Pusher{src: src, push: push}
}

// PushProfile re-arma la foto COMPLETA del tenant y la empuja.
//
// ⚠️ Los argumentos sessionID y profile son el DISPARADOR, no el contenido: el payload
// de filters es del tenant entero (D-046.2) y se re-lee de la BD, que es la única
// fuente de verdad y ya tiene el valor recién escrito (el handler llama a este hook
// DESPUÉS de que SetProfile haya confirmado). Construir el mapa a partir del argumento
// daría un mapa de UNA sesión, y el Edge —que interpreta la ausencia como `active`—
// reactivaría en silencio todas las demás pasivas del tenant.
//
// Un pusher con push nil es un no-op silencioso (mismo criterio que el pusher nil del
// handler): sirve para montar el hook sin Gateway en un test.
func (p *Pusher) PushProfile(ctx context.Context, tenantID, sessionID string, profile fleet.Profile) error {
	if p == nil || p.push == nil {
		return nil
	}
	version, payload, err := ForTenant(ctx, p.src, tenantID)
	if err != nil {
		return fmt.Errorf("filtercfg: armar filtros del tenant (disparado por %s=%s): %w",
			sessionID, profile, err)
	}
	return p.push.PushConfig(ctx, tenantID, Kind, version, payload)
}
