// Porta internal/filtercfg/filtercfg.go @ 809345b

// Package filtercfg construye el kind:"filters" que la nube empuja al Edge por
// ConfigUpdate (ADR-0021, ADR-0027; Plan 046 · Ola 2 · T2.1).
//
// Es el gemelo del proveedor del kind "intents", con UNA diferencia de fondo:
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
//     provider. Por eso la constante Kind vive aquí y NO en el paquete del gateway
//     gRPC.
//
// # Lo que este fichero NO prueba (R-C5)
//
// Que un push fallido no cambie la respuesta HTTP del POST /sessions/{id}/profile es
// una promesa del HANDLER que llama a Pusher, no de este paquete: se prueba extremo a
// extremo en el proceso de F9, no aquí.
package filtercfg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/fleet"
)

// Kind es el espacio de nombres de la config de filtros en el ConfigUpdate
// (ADR-0021). Promete UNA cosa: vale exactamente "filters".
//
// Literal EXACTO acordado con el Edge: un duplicado del string en el otro proceso
// falla en SILENCIO (el Edge ignora los kinds no registrados con un log tolerante),
// así que se referencia esta constante y no se re-escribe la palabra.
const Kind = "filters"

// SessionFilter es lo que el mapa guarda por sesión. Hoy solo el perfil; es un OBJETO
// y no un string suelto para que mañana se le pueda añadir un campo sin romper al
// Edge (un lector de objeto ignora las claves que no conoce; un lector de string se
// rompe entero).
//
// Promete: se serializa como el objeto JSON {"profile": "<valor>"}, con esa etiqueta
// literal.
type SessionFilter struct {
	// Profile es "active" o "passive": los valores literales de la columna
	// fleet_sessions.profile. Nada más entra aquí.
	Profile string `json:"profile"`
}

// Payload es el cuerpo JSON del kind:"filters".
//
// Promete: se serializa con las etiquetas literales "version" (un número JSON, no un
// texto) y "sessions" (un objeto session_id → SessionFilter).
//
// 🔴 Sessions incluye SIEMPRE todas las sesiones del tenant, activas incluidas,
// porque el contrato dice que una sesión AUSENTE del mapa el Edge la asume `active`
// (fail-open: un Edge jamás pierde tráfico por una config incompleta). Omitir las
// activas «porque se asumen» funcionaría hoy y mentiría mañana: la sesión que pasa de
// pasiva a activa se quedaría con el `passive` viejo.
//
// Valor cero: Payload{} trae Sessions nil y se serializaría como "sessions":null. Ese
// valor NO lo produce este paquete: Build siempre arma el mapa. Quien construya un
// Payload a mano responde de no dejarlo nil.
type Payload struct {
	// Version es el mismo entero que viaja como texto en ConfigPayload.Version.
	Version int64 `json:"version"`
	// Sessions mapea session_id → filtro. Nunca nil: un tenant sin sesiones manda
	// `{}` (mapa vacío), que significa «ninguna restricción», y no `null`.
	Sessions map[string]SessionFilter `json:"sessions"`
}

// Source es el puerto de LECTURA de este paquete: la foto del eje `profile` del
// tenant entero. Lo satisfacen el adaptador Postgres de fleet y, para los tests sin
// BD, el doble fleethelpertest.Memoria.
//
// Es un puerto propio y NO fleet.Repository entero por interfaz-segregación: aquí
// solo se lee, y depender del contrato de escritura de la flota obligaría a todo
// doble de prueba a implementar diez métodos que no se usan. Y es el ÚNICO puerto de
// lectura: no hay otro por el que este paquete pudiera preguntar por una feature
// (regla 1).
type Source interface {
	// ProfilesByTenant devuelve la foto de perfiles del tenant: todas sus sesiones y
	// la versión de la foto. Un tenant sin filas no es un error.
	ProfilesByTenant(ctx context.Context, tenantID string) (fleet.TenantProfiles, error)
}

// ConfigPusher es el puerto de SALIDA: el fan-out de un ConfigUpdate a las sesiones
// vivas del tenant. Lo satisface el Server del gateway gRPC (PushConfig). Se declara
// aquí —y no se importa el tipo del gateway— para que este paquete no dependa de gRPC
// ni del contrato CloudLink: lo único que necesita es «alguien que sepa empujar».
type ConfigPusher interface {
	// PushConfig empuja el payload del kind dado, con su versión, a las sesiones
	// vivas del tenant.
	PushConfig(ctx context.Context, tenantID, kind, version string, payload []byte) error
}

// Build proyecta la foto del tenant al par (version, payload) del cable. Promete:
//
//   - el payload es el JSON de D-046.2 y trae TODAS las sesiones de tp.Sessions, las
//     activas también (una sesión ausente el Edge la asume `active`);
//   - cada sesión viaja como el objeto {"profile": …}, con "active" o "passive";
//   - version es la representación DECIMAL de tp.Version, y es el mismo entero que va
//     dentro del JSON como "version". No un hash, no un UUID: el frame lo transporta
//     como string (ConfigPayload.Version) pero el Edge lo compara como NÚMERO para
//     descartar versiones viejas, así que las dos mitades tienen que ser el mismo
//     valor o la comparación no significa nada;
//   - sin sesiones (tp.Sessions nil o mapa vacío) el JSON trae "sessions":{} y NUNCA
//     "sessions":null;
//   - un fallo al serializar devuelve ("", nil, err) con el texto
//     "filtercfg: serializar payload: <causa>". Con los tipos de hoy no puede ocurrir.
//
// Valor cero y fail-open (se porta tal cual): un perfil que no sea active|passive
// —incluido el vacío ""— se proyecta como "passive". No debería ocurrir —la columna
// tiene CHECK— pero si ocurriera, emitir el valor crudo haría que el validador del
// Edge rechazara el payload ENTERO y se quedara con el last-known-good de todas las
// sesiones. Degradar solo esa sesión al valor seguro («no auto-responde») es
// estrictamente mejor que perder el mapa completo. Y fleet.TenantProfiles{} (el cero)
// da la versión "0" y "sessions":{}: es la foto de un tenant sin ni una fila, y se
// empuja igual (regla 2).
func Build(tp fleet.TenantProfiles) (version string, payload []byte, err error) {
	// make y no un mapa nil: es lo que hace que un tenant sin sesiones salga como
	// "sessions":{} y no como "sessions":null.
	p := Payload{Version: tp.Version, Sessions: make(map[string]SessionFilter, len(tp.Sessions))}
	for sessionID, profile := range tp.Sessions {
		if !fleet.ValidProfile(profile) {
			// Lado seguro: degrada SOLO esta sesión, no tumba el payload entero.
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
// vías que existen para que la config llegue al Edge —el provider al conectar y el
// hook en caliente (Pusher, más abajo)—. Promete:
//
//   - hace UNA sola lectura de src, con el tenantID dado, y devuelve el Build de esa
//     foto;
//   - no consulta nada más: no hay puerto de entitlements que inyectarle (regla 1),
//     y por eso SIEMPRE devuelve config en el camino feliz. Un tenant sin sesiones NO
//     es un fallo: devuelve su versión "0" y el mapa vacío, y se empuja igual
//     (regla 2);
//   - si la lectura falla devuelve ("", nil, err), con el error envuelto como
//     "filtercfg: leer perfiles del tenant: <causa>" (errors.Is llega a la causa).
//
// Vive aquí, y las dos vías la llaman, para que no puedan divergir: encender una sola
// de las dos, o armarlas distinto, deja una vía muda que NO da ningún rojo (los tests
// de cada vía pasan por separado). Es la trampa que el Plan 046 ya se comió una vez.
//
// Un fallo se PROPAGA (no se empuja config a medias): el llamante lo loguea y no
// empuja nada, y el Edge conserva su last-known-good, que es la degradación correcta.
func ForTenant(ctx context.Context, src Source, tenantID string) (version string, payload []byte, err error) {
	tp, err := src.ProfilesByTenant(ctx, tenantID)
	if err != nil {
		return "", nil, fmt.Errorf("filtercfg: leer perfiles del tenant: %w", err)
	}
	return Build(tp)
}

// Pusher adapta el cambio de perfil recién persistido a un ConfigUpdate del
// kind:"filters" hacia las sesiones vivas del tenant. Es el hook de perfil del
// handler POST /sessions/{id}/profile (el ProfilePusher de la cara HTTP), el que
// T1.2 dejó APAGADO (cableado a nil) y T2.1 enciende.
//
// 🔴 Best-effort, como su puerto manda: un fallo del push NO invalida la escritura ni
// cambia el código de respuesta del POST (R-C5, que se prueba en F9). El perfil ya
// está persistido y el push al conectar reconcilia. Aquí eso se cumple SOLO:
// PushConfig del Gateway devuelve nil siempre a propósito, y de PushProfile solo
// salen errores de LECTURA o de armado —que el handler loguea y descarta—.
//
// Valor cero: un *Pusher nil, y uno sin ConfigPusher (incluido Pusher{}), son un
// no-op silencioso. Ver PushProfile.
type Pusher struct {
	src  Source
	push ConfigPusher
}

// NewPusher construye el hook sobre la fuente de perfiles y el fan-out del Gateway.
// No consulta ni empuja nada al construir. Un push nil es válido y da un Pusher
// no-op (ver PushProfile).
func NewPusher(src Source, push ConfigPusher) *Pusher {
	return &Pusher{src: src, push: push}
}

// PushProfile re-arma la foto COMPLETA del tenant y la empuja. Promete:
//
//   - relee la foto del tenant con ForTenant y hace exactamente UN
//     PushConfig(ctx, tenantID, Kind, version, payload) con el par que ForTenant
//     devolvió: el payload es el del tenant ENTERO, no el de la sesión disparadora;
//   - si la lectura o el armado fallan NO empuja nada y devuelve el error envuelto
//     como "filtercfg: armar filtros del tenant (disparado por <sessionID>=<profile>):
//     <causa>" (errors.Is llega a la causa original);
//   - el error de PushConfig vuelve TAL CUAL, sin envolver.
//
// ⚠️ Los argumentos sessionID y profile son el DISPARADOR, no el contenido: el payload
// de filters es del tenant entero (D-046.2) y se re-lee de la BD, que es la única
// fuente de verdad y ya tiene el valor recién escrito (el handler llama a este hook
// DESPUÉS de que SetProfile haya confirmado). Construir el mapa a partir del argumento
// daría un mapa de UNA sesión, y el Edge —que interpreta la ausencia como `active`—
// reactivaría en silencio todas las demás pasivas del tenant. Solo aparecen en el
// texto del error.
//
// Valor cero (se porta tal cual): con el receptor nil, o con un Pusher sin
// ConfigPusher (push nil), devuelve nil SIN consultar la fuente: ni lee ni empuja.
// Mismo criterio que el pusher nil del handler: sirve para montar el hook sin Gateway
// en un test.
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
