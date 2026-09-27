# 03 · Lo que no se toca, lo que hay que resolver antes, y lo que falta decidir

---

## 1 · Lo que NO se toca: los contratos hacia fuera

Una reorganización de carpetas **no debe cambiar ni una de estas cosas**. Si un diff de una ola
toca alguna, la ola está mal hecha.

| Contrato | Quién lo consume | Dónde se define hoy | Cómo se comprueba que no cambió |
|---|---|---|---|
| 95 rutas HTTP (`:8103` `/api/v1/*`, `:8100` `/admin/*`, `/healthz`, `/metrics`) | BFF, consola del cliente, consola de plataforma, Edge (`/api/v1/signup`), CRM (`/integrations/callback`) | `internal/publicapi/`, `internal/bootstrap/arranque/http.go`, `internal/bootstrap/bootstrap.go` | Huella de patrones de ruta antes/después: diff vacío |
| Permisos de cada ruta (scope, `.any` de plataforma, I-CP-5) | Todo el RBAC | Los `protect(...)` de `publicapi` y la migración `0060` | `platform_permissions_test.go` verde **y** comprobado que sigue detectando `platformadmin.` |
| 2 rpc gRPC (`Connect` `:8101`, `EnrollEdge` `:8102`) | `wapp-edge-agent` | El proto vive en `wapp-cloudlink` v0.17.0; aquí solo se implementa | `go.mod` sin cambios en `wapp-cloudlink` |
| 47 tablas y el esquema **0.48.0** (84 migraciones embebidas) | La propia BD de UAT | `internal/platform/storage/postgres/migrations/structure/*.sql` | **No mover esa carpeta** (el runner es full-replay; ver `../esquema-postgres.md`) · `migrate -status` idéntico |
| 70 variables de entorno (`WAPP_*`) | Despliegue de UAT | `internal/platform/config` + loaders con prefijo | Huella de nombres antes/después ⚠️ el prefijo `WAPP_` se compone en el loader: busca el sufijo, no el nombre entero |
| Flags de los CLI (`cmd/migrate`, `cmd/prompts`, `cmd/casebank`) | Operador, scripts de despliegue | `cmd/*/main.go` | `cmd/` **no se mueve** |
| Nombres de métricas Prometheus | Monitorización | `internal/platform/metrics` | Huella de nombres antes/después |
| Textos de error que viajan en texto plano | BFF y `wapp-ctl` los enseñan al usuario | Prefijos de paquete en `errors.New("platformadmin: …")` y similares | **No renombrar paquetes** (`01` §3.5) |
| 🔒 Literal `AVISO_SESION_PASIVA_V1` | `wapp-edge-agent` lo compara byte a byte | `../literal-aviso-sesion-pasiva.md` + su test en `gateway/grpc` | No se edita. Si el test cambia de carpeta, **solo** cambia la ruta relativa al `.md` |
| Contrato CRM `wapp-crm-v1` | Puente CRM | `docs/contracts/wapp-crm-v1/` | La carpeta no se mueve; los tres tests que la leen solo ajustan su `../..` |
| Doble llave (DEK del cliente / Lease del servidor) | Todo el ecosistema | `internal/gateway/lease/` | Ningún cambio de lógica en `lease`; solo de ruta |

**Propuesta para la Ola 0**: un script que genere una **huella de contratos** (patrones de ruta
registrados, nombres de variables, nombres de métricas, prefijos de error) y que cada ola compare
antes/después. Es barato y convierte esta tabla en un gate.

---

## 2 · Lo que hay que resolver ANTES de pasárselo a Claude Code en la web

Claude Code en la web **solo ve lo que está en `origin`** de este repo. Hoy eso no coincide con lo
que hay en local.

| # | Pendiente | Por qué importa | Estado al 2026-09-27 |
|---|---|---|---|
| P-1 | **Publicar la rama `refactor/arranque-por-fases`** (2 commits: `0dc6b88` las nueve fases, `7cd3a0c` su documentación) en `dev` | En `origin/dev` (`9493cea`) sigue el `bootstrap.Run` de 991 líneas, y toda la documentación ya habla de nueve fases. Quien implemente vería código y documentación contradiciéndose, y reorganizaría sobre la versión vieja | 🔴 Solo en local |
| P-2 | **Resolver el trabajo a medio hacer**: 3 ficheros modificados (`internal/catalogimport/diff.go`, `internal/flujos/events/store.go`, `internal/platform/metrics/inferstats.go`) y `cmd/debug_inferencia/` sin versionar | Un movimiento de carpetas sobre cambios sin commitear los pierde o los mezcla. Y `cmd/debug_inferencia` lo cita la nota de arranque del análisis, pero **no existe en git** | 🔴 Sin commitear |
| P-3 | **Publicar esta carpeta** (`documentations/reorganizacion-modular/`) en `dev` | Sin ella, la web no tiene las instrucciones | 🔴 Sin commitear |
| P-4 | **Decidir el alcance y los nombres** (§3) y escribir entonces el plan ejecutable: olas, tareas, criterios de cierre, prompts | Este análisis no es un plan | ⏳ Pendiente de Jhoan |
| P-5 | **Ventana de congelación**: mientras dure cada ola de movimiento, nada más entra en `dev` | Un diff que toca casi todos los ficheros choca con cualquier rama viva | Hoy no hay ramas vivas por delante de `dev` (`feat/047-o10-puerta-plano-roles` ya está fusionada: 0 commits por delante) |
| P-6 | **Saber si el entorno web tiene Docker/Postgres** | Sin él, los 97 ficheros de integración se saltan (DT-52: 438 SKIP bajo rc=0). Si no lo tiene, la ola la **cierra una sesión local** con `make test-integration`, contando SKIP con `-v` y leyendo el `rc` sin pipe | ❓ Por averiguar |
| P-7 | **Toolchain fijada**: Go `1.26.5` y golangci-lint `v2.12.2` (`Makefile:11-12`) | El gate es `make ci-local`; otra versión de lint da otro resultado. (La máquina local tiene Go 1.27.1: `go.mod` manda) | Anotado |

### 2.1 · Lo que queda DESPUÉS, fuera de este repo

Quien implemente no puede tocarlo; lo hace después una sesión con acceso a la raíz de wApp,
**con la misma tabla de mapeo** ruta vieja → ruta nueva:

- La **documentación del ecosistema** (`documentations/` de la raíz): **2.565 menciones de
  rutas `internal/` en 150 ficheros**, entre ellos los ADR con su «Cómo se comprueba».
- **ADR-0010** define «módulo» como *el primer segmento bajo `internal/`* para su regla de conteo
  de tablas compartidas. Si los módulos pasan a vivir un nivel más abajo, **esa regla cambia** y
  hay que reescribirla, o su medición dirá cosas falsas.
- La **bóveda de análisis** (`analisis/`) y los **5 comentarios** de repos hermanos que citan rutas
  de aquí (BFF `apiclient/auth.go:123` y `web/signup_test.go:85`, consola del cliente
  `web/solicitudes_acciones.go:160` y `web/flash.go:309`, Edge `cmd/wapp-ctl/auth.go:341`).

### 2.2 · Deriva documental que ya existe hoy (conviene corregirla antes, no después)

Detectada al preparar este análisis; son pequeñas pero confunden a quien llega nuevo:

- `README.md` de `documentations/` (§«cinco cosas», punto 2) dice que las rutas están en
  `internal/bootstrap/http.go`; hoy están en `internal/bootstrap/arranque/http.go`.
- `constitucion.md` (I-CP-5) sitúa el candado en `internal/bootstrap/platform_permissions_test.go`;
  vive en `internal/bootstrap/arranque/`.

---

## 3 · Decisiones abiertas

Son de Jhoan. Cada una tiene una recomendación, pero ninguna se da por tomada.

| # | Pregunta | Opciones | Recomendación |
|---|---|---|---|
| D-1 | **Alcance** | 1 solo mover · 2 mover + corregir ubicaciones · 3 módulos con API interna y sin tablas compartidas | **2**, en olas (`01` §4) |
| D-2 | **Forma del árbol** | `internal/modulos/<m>/…` · `internal/<m>/…` directo | `internal/modulos/<m>/` si se quiere que el árbol **diga** qué es módulo y qué es soporte (`platform`, `publicapi`, `bootstrap` quedan fuera de `modulos/`) |
| D-3 | **¿Aplanar o anidar?** | `modulos/conversacion/runtime` · `modulos/conversacion/flujos/runtime` | **Aplanar** donde el nombre del paquete ya es claro: menos profundidad, mismo nombre de paquete |
| D-4 | **¿Renombrar paquetes?** | Solo mover · permitir renombrar | **Solo mover**: el nombre del paquete aparece en textos de error observables y en un candado de seguridad (`01` §3.5, §3.1) |
| D-5 | **Los módulos y sus bordes** | La agrupación candidata de `02` §4 u otra | Revisar sobre todo: ¿`acceso` y `operador` son uno? · ¿dónde van `tenantvars`, `intentcfg`, `turnoacotado`, `degradation`? · ¿los nombres, en español como el resto del repo? |
| D-6 | **`publicapi`** | Cara HTTP única · repartir handlers entre módulos | **Cara única** en esta fase (`01` §5) |
| D-7 | **El ciclo de negocio** (conversación ↔ captación ↔ solicitudes) | Cortarlo ahora · delimitarlo y congelarlo | **Congelarlo** con el candado de fronteras y cortarlo en un plan propio si duele |
| D-8 | **Dónde vive el plan** | Aquí (`documentations/reorganizacion-modular/`) · en `documentations/planes/` del ecosistema | **Aquí**, porque quien implementa solo ve este repo; con una ficha-puntero en el ecosistema |
