// Parte de internal/arranque/auth.go (copia de internal/bootstrap/arranque/auth.go @ 80807ba), T2.34: la cadena de configuración que se empuja al Edge al conectar.
package arranque

import (
	"context"
	"errors"

	sharedlogger "github.com/EduGoGroup/wapp-shared/logger"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/intentcfg"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/acceso/entitlements"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/filtercfg"
	edgegrpc "github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/edge/grpc"
)

// jwksConfigProvider entrega SIEMPRE la config kind:"jwks" (la pública ES256 del
// emisor, ADR-0025 dec.2) a TODO Edge que conecta y delega el resto de kinds al
// provider siguiente (la cadena intents+filters). La llave ES256 es GLOBAL del emisor
// (un solo par por proceso, no por-tenant): el mismo JWKS vale para todos los
// tenants, así que se entrega sin mirar el tenantID.
//
// 🔴 «SIEMPRE» es literal desde la corrección del code review 2026-08-21: antes, un
// error del eslabón siguiente hacía `return nil, err` y se llevaba por delante el
// jwks, que YA ESTABA EN LA MANO y no había podido fallar (es un valor calculado al
// arrancar, sin I/O). Un hipo de Neon en el connect dejaba al Edge sin jwks, sin
// intents y sin filters de una vez. Ahora el error del siguiente se LOGUEA y el jwks
// viaja igual. Ver el porqué completo en chainConfigProvider.
type jwksConfigProvider struct {
	jwks edgegrpc.ConfigPayload
	next edgegrpc.ConfigProvider
	log  sharedlogger.Logger
}

func (p jwksConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]edgegrpc.ConfigPayload, error) {
	if p.next == nil {
		return []edgegrpc.ConfigPayload{p.jwks}, nil
	}
	rest, err := p.next.ConfigsForConnect(ctx, tenantID)
	if err != nil {
		// No se propaga: perder el jwks por un fallo de otro kind es exactamente el
		// modo de fallo que esta función existe para no tener.
		logConfigLinkError(p.log, "jwks:next", tenantID, err)
	}
	// El jwks va SIEMPRE el primero de la lista, haya fallado o no el eslabón siguiente.
	out := make([]edgegrpc.ConfigPayload, 0, 1+len(rest))
	out = append(out, p.jwks)
	return append(out, rest...), nil
}

// intentConfigStore es el puerto de LECTURA que consume intentsConfigProvider: solo
// el Get del blob de intents del tenant. Lo satisface *intentcfg.PostgresStore
// (producción) y *intentcfg.MemoryStore (tests).
//
// Se declara como interfaz —y no se guarda el tipo concreto— para que la CADENA REAL
// que arma buildConfigProvider se pueda ejercer en un test sin PostgreSQL. Ese test
// es el criterio (d) del Plan 046 · T2.1 y no existía: el que había le daba al
// Gateway una lista de configs escrita a mano, así que gatear `filters` por
// entitlement —la regla 1, la que el plan más protege— lo habría dejado en VERDE.
type intentConfigStore interface {
	Get(ctx context.Context, tenantID string) (intentcfg.Config, error)
}

// intentsConfigProvider adapta el store de config de intents + los entitlements al
// puerto edgegrpc.ConfigProvider (ADR-0021): al conectar un Edge, entrega la
// config de intents vigente del tenant SOLO si tiene la feature llm_intent (gate de
// verdad, ADR-0022) y hay config persistida. Es el ÚNICO punto que ata el kind
// "intents" al push al conectar; el Gateway permanece genérico (no conoce kinds).
type intentsConfigProvider struct {
	store intentConfigStore
	ents  entitlements.Resolver
}

// ConfigsForConnect resuelve el gate y devuelve la config de intents del tenant, o
// nil si no aplica (sin feature o sin config). Un fallo de infraestructura se devuelve
// al llamante; quien lo trata es chainConfigProvider, que lo LOGUEA con el kind y
// sigue con los demás eslabones (best-effort por eslabón): que Neon tosa al resolver
// la feature no puede costarle al Edge el jwks ni los filtros.
func (p intentsConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]edgegrpc.ConfigPayload, error) {
	has, err := p.ents.Has(ctx, tenantID, entitlements.FeatureLLMIntent)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, nil
	}
	cfg, err := p.store.Get(ctx, tenantID)
	if err != nil {
		if errors.Is(err, intentcfg.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return []edgegrpc.ConfigPayload{{Kind: intentcfg.Kind, Version: cfg.Version, Payload: cfg.Blob}}, nil
}

// filtersConfigProvider adapta la foto de perfiles del tenant (fleet_sessions.profile)
// al puerto edgegrpc.ConfigProvider: al conectar un Edge, le entrega el
// kind:"filters" vigente (Plan 046 · T2.1, ADR-0027). Es el TERCER eslabón de la
// cadena, junto a jwks e intents.
//
// 🔴 Se parece a intentsConfigProvider en la forma y se le opone en el fondo, en dos
// puntos que NO son estilo:
//
//  1. NO consulta entitlements. `passive_profiles` está declarada y NO gatea en v1.
//     Gatear aquí haría que un tenant sin el add-on SUBIERA a la nube el tráfico de
//     sus sesiones pasivas: el fallo exacto que este plan viene a cerrar. Por eso
//     este struct no tiene campo `ents` — no es que esté sin usar, es que no existe.
//  2. NUNCA devuelve (nil, nil) en el camino feliz. intents sí lo hace («sin feature o
//     sin config no hay nada que empujar»); aquí un mapa todo-`active` ES la
//     información que hace converger al Edge cuando una sesión deja de ser pasiva, y
//     callarse dejaría al Edge con el mapa anterior y una sesión reactivada muda.
//
// Comparte con el hook en caliente (filtercfg.Pusher) la MISMA función de armado,
// filtercfg.ForTenant, para que las dos vías no puedan divergir.
type filtersConfigProvider struct {
	src filtercfg.Source
}

// ConfigsForConnect devuelve SIEMPRE una config (salvo error de infraestructura, que
// se devuelve al llamante). Ese error NO tumba la entrega de los otros kinds:
// chainConfigProvider lo loguea con el kind "filters" y sigue. El Edge se queda con el
// mapa de filtros que ya tenía —su last-known-good de ESTE kind, que sigue siendo
// válido— y lo reconcilia en la próxima conexión o en el próximo cambio de perfil.
func (p filtersConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]edgegrpc.ConfigPayload, error) {
	version, payload, err := filtercfg.ForTenant(ctx, p.src, tenantID)
	if err != nil {
		return nil, err
	}
	return []edgegrpc.ConfigPayload{{Kind: filtercfg.Kind, Version: version, Payload: payload}}, nil
}

// chainLink es un eslabón de la cadena con el KIND que aporta. El kind no se usa para
// decidir nada: es la etiqueta con la que se loguea su fallo. Sin ella, el operador ve
// «un eslabón falló» y tiene que adivinar cuál, que es justo lo que no puede hacer a
// las 3 de la mañana.
type chainLink struct {
	kind     string
	provider edgegrpc.ConfigProvider
}

// chainConfigProvider concatena N proveedores en el orden dado y devuelve la unión de
// sus configs. Existe porque el encadenado por campo `next` no escala a tres eslabones
// sin una trampa: intentsConfigProvider tiene DOS `return nil, nil` tempranos (sin
// feature, sin config persistida), así que colgarle un `next` habría hecho que un
// tenant sin llm_intent se quedara TAMBIÉN sin filters —silenciosamente, y con todos
// los tests de cada provider en verde—.
//
// 🔴 BEST-EFFORT POR ESLABÓN (corrección del code review 2026-08-21). Un eslabón que
// falla LOGUEA su error CON SU KIND y la cadena SIGUE con los demás: nunca se pierde
// un kind por culpa de otro. La versión anterior propagaba el primer error y abortaba
// entera, con este razonamiento —«media config es peor que ninguna: el Edge conserva
// su last-known-good COMPLETO»— que suena bien y es falso en los dos extremos:
//
//   - No hay «last-known-good completo» que conservar. Los kinds son INDEPENDIENTES
//     en el Edge: cada uno tiene su propia versión y su propia persistencia. Que
//     `filters` no llegue no invalida el `intents` que sí llegó, ni al revés.
//   - Y el modo de fallo real era el contrario del que se temía: un hipo de Neon en
//     el connect dejaba al Edge SIN JWKS —que no había fallado, y que ni siquiera
//     hace I/O—, sin intents y sin filters de una vez, con UN solo Error en el log de
//     la nube. Con T2.1 hay una query MÁS a Neon en cada connect, así que la
//     probabilidad de ese hipo sube justamente ahora.
//
// El Edge, además, ya sabe convivir con una config que no llega: conserva la que
// tiene. Lo que no sabe hacer es adivinar la que nunca le mandaron.
//
// Un eslabón nil se salta.
type chainConfigProvider struct {
	links []chainLink
	log   sharedlogger.Logger
}

func (c chainConfigProvider) ConfigsForConnect(ctx context.Context, tenantID string) ([]edgegrpc.ConfigPayload, error) {
	var out []edgegrpc.ConfigPayload
	for _, l := range c.links {
		if l.provider == nil {
			continue
		}
		cfgs, err := l.provider.ConfigsForConnect(ctx, tenantID)
		if err != nil {
			logConfigLinkError(c.log, l.kind, tenantID, err)
			continue
		}
		out = append(out, cfgs...)
	}
	return out, nil
}

// logConfigLinkError registra el fallo de UN eslabón de la cadena de config sin
// tumbarla. Tolera log nil (los tests montan la cadena sin logger).
//
// Es Error y no Warn a propósito: que un tenant se quede sin un kind al conectar es
// una degradación real, no una curiosidad. Lo que cambió con el best-effort no es la
// severidad, es el ALCANCE — antes un Error se llevaba los tres kinds por delante.
func logConfigLinkError(log sharedlogger.Logger, kind, tenantID string, err error) {
	if log == nil {
		return
	}
	log.Error("config push: un eslabón de la cadena falló; se entregan los demás",
		"kind", kind, "tenant_id", tenantID, "error", err)
}

// buildConfigProvider arma la cadena COMPLETA de config al conectar (ADR-0021), en
// este orden y con estas reglas:
//
//  1. jwks    — SIEMPRE, y sobrevive al fallo de cualquier otro (no hace I/O).
//  2. intents — solo si el tenant tiene llm_intent Y hay config persistida.
//  3. filters — SIEMPRE (Plan 046 · T2.1): el mapa de perfiles del tenant, SIN gate
//     de entitlement y también cuando no hay ni una sesión pasiva.
//
// 🔴 Existe como FUNCIÓN, y no inline en Run(), por una razón concreta: el criterio
// (d) de T2.1 exige un test sobre la cadena REAL —dos tenants, uno con llm_intent y
// otro sin ella, tres kinds vs dos— y una cadena armada dentro de Run() no se puede
// ejercer sin levantar el proceso entero. El test vive en filters_config_test.go y se
// pone ROJO si alguien le cuelga a `filters` un gate por entitlement.
func buildConfigProvider(
	jwks edgegrpc.ConfigPayload,
	intents intentConfigStore,
	ents entitlements.Resolver,
	profiles filtercfg.Source,
	log sharedlogger.Logger,
) edgegrpc.ConfigProvider {
	return jwksConfigProvider{
		jwks: jwks,
		log:  log,
		next: chainConfigProvider{
			log: log,
			links: []chainLink{
				{kind: intentcfg.Kind, provider: intentsConfigProvider{store: intents, ents: ents}},
				{kind: filtercfg.Kind, provider: filtersConfigProvider{src: profiles}},
			},
		},
	}
}
