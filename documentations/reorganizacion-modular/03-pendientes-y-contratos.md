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

Claude Code en la web **solo ve lo que está en `origin`** de este repo. El 2026-09-27 se publicó en
`dev` todo lo que estaba solo en local (P-1 a P-3): desde entonces **`dev` es la fuente de la
verdad**.

| # | Pendiente | Por qué importa | Estado al 2026-09-27 |
|---|---|---|---|
| P-1 | **Publicar la rama `refactor/arranque-por-fases`** (2 commits: `0dc6b88` las nueve fases, `7cd3a0c` su documentación) en `dev` | En `origin/dev` (`9493cea`) sigue el `bootstrap.Run` de 991 líneas, y toda la documentación ya habla de nueve fases. Quien implemente vería código y documentación contradiciéndose, y reorganizaría sobre la versión vieja | ✅ En `dev` desde el 2026-09-27 |
| P-2 | **Resolver el trabajo a medio hacer**: 3 ficheros modificados (`internal/catalogimport/diff.go`, `internal/flujos/events/store.go`, `internal/platform/metrics/inferstats.go`) y `cmd/debug_inferencia/` sin versionar | Un movimiento de carpetas sobre cambios sin commitear los pierde o los mezcla. Y `cmd/debug_inferencia` lo cita la nota de arranque del análisis, pero **no existía en git** | ✅ Commiteado y en `dev` el 2026-09-27 |
| P-3 | **Publicar esta carpeta** (`documentations/reorganizacion-modular/`) en `dev` | Sin ella, la web no tiene las instrucciones | ✅ En `dev` el 2026-09-27 |
| P-4 | **Decidir el alcance y los nombres** (§3) y escribir entonces el plan ejecutable: olas, tareas, criterios de cierre, prompts | Este análisis no es un plan | ⏳ Pendiente de Jhoan |
| P-5 | **Ventana de congelación**: mientras dure cada ola de movimiento, nada más entra en `dev` | Un diff que toca casi todos los ficheros choca con cualquier rama viva | Hoy no hay ramas vivas por delante de `dev` (`feat/047-o10-puerta-plano-roles` ya está fusionada: 0 commits por delante) |
| P-6 | **Docker solo hace falta en local** | Con el método de `05`, los tests de fichero son unitarios (sin BD) y los de integración se escriben de cero en F9 con **testcontainers**, que necesita Docker: los corre **Claude Code en local** (`05` §7.3). La web escribe y compila; no cierra F9 | ✅ Resuelto por el método (2026-09-27) |
| P-7 | **Toolchain fijada**: Go `1.26.5` y golangci-lint `v2.12.2` (`Makefile:11-12`) | El gate es `make ci-local`; otra versión de lint da otro resultado. (La máquina local tiene Go 1.27.1: `go.mod` manda) | Anotado. ✎ 2026-10-02: la fija sola el `Makefile` (`GOTOOLCHAIN`, `make tools`, `make toolchain`; hoy `Makefile:15-16`): [`06`](06-entorno-web.md) §6 |

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

- `Makefile:57` (`test-integration`) levanta `postgres:16`, y UAT corre `postgres:17-alpine`: la
  batería de integración vieja no prueba contra la versión mayor de producción. La suite nueva de
  `05` §7.2 fija la 17. ⏳ **F0 no la cierra**: la cierra F9 (la suite por proceso con
  testcontainers sobre `postgres:17-alpine`), y F10 retira la batería vieja; hoy la línea es
  `Makefile:123`.

- `README.md` de `documentations/` (§«cinco cosas», punto 2) dice que las rutas están en
  `internal/bootstrap/http.go`; hoy están en `internal/bootstrap/arranque/http.go`.
  ✅ cerrada en F0 (T0.20, 2026-09-30).
- `constitucion.md` (I-CP-5) sitúa el candado en `internal/bootstrap/platform_permissions_test.go`;
  vive en `internal/bootstrap/arranque/`. ✅ cerrada en F0 (T0.20, 2026-09-30).
- Y el resto que midió F0 (T0.20, 2026-09-30): 18 líneas de `README`, `constitucion`,
  `contratos`, `operacion` y `deuda` citaban en presente `internal/bootstrap/<fichero>.go` o
  `internal/publicapi/flows.go` (el `HeadBucket` vive en `internal/bootstrap/arranque/flows.go:75`),
  más «8 `*_cableado_test.go` en `internal/bootstrap/`» (son 9, en `arranque/`) y tres
  referencias sueltas (`contratos.md` y `operacion.md` a `bootstrap.go`, `arquitectura.md` a
  `internal/bootstrap/`). ✅ cerrada en F0 (T0.20): el `grep` de T0.20 solo
  devuelve ya la historia de `deuda.md` (D-10 cerrada).

---

## 3 · Decisiones abiertas

Son de Jhoan. 🔒 **Actualización 2026-09-28**: D-2, D-5, D-9, D-11 y D-12 se **cerraron** según su
recomendación el 2026-09-27; **D-10 se cerró distinto** —una cara HTTP **única y nueva**,
`internal/apipublica`, construida **por olas** (ver [`plan/FX-cara-http/`](plan/FX-cara-http/README.md))—;
D-13 y las decisiones nuevas que salieron al escribir el plan viven en
[`plan/DECISIONES.md`](plan/DECISIONES.md), que desde hoy es el registro vivo. Esta tabla queda como
historia del análisis.

| # | Pregunta | Opciones | Recomendación |
|---|---|---|---|
| D-1 | **Alcance** | 1 solo mover · 2 mover + corregir ubicaciones · 3 módulos con API interna y sin tablas compartidas | **2**, en olas (`01` §4) |
| D-2 | **Forma del árbol** | `internal/modulos/<m>/…` · `internal/<m>/…` directo | `internal/modulos/<m>/` si se quiere que el árbol **diga** qué es módulo y qué es soporte (`platform`, `publicapi`, `bootstrap` quedan fuera de `modulos/`) |
| D-3 | **¿Aplanar o anidar?** | `modulos/conversacion/runtime` · `modulos/conversacion/flujos/runtime` | **Aplanar** donde el nombre del paquete ya es claro: menos profundidad, mismo nombre de paquete |
| D-4 | **¿Renombrar paquetes?** | Solo mover · permitir renombrar | **Solo mover**: el nombre del paquete aparece en textos de error observables y en un candado de seguridad (`01` §3.5, §3.1) |
| D-5 | **Los módulos y sus bordes** | La agrupación candidata de `02` §4 u otra | Revisar sobre todo: ¿`acceso` y `operador` son uno? · ¿dónde van `tenantvars`, `intentcfg`, `turnoacotado`, `degradation`? · ¿P5 (`intakes/quotetext`) con solicitudes o con el resto de etapas LLM? · ¿dónde vive el orquestador `reanalisis`? (`02` §4.1) · ¿los nombres, en español como el resto del repo? |
| D-6 | **`publicapi`** | Cara HTTP única · repartir handlers entre módulos | **Cara única** en esta fase (`01` §5) |
| D-7 | **El ciclo de negocio** (conversación ↔ captación ↔ solicitudes) | Cortarlo ahora · delimitarlo y congelarlo | **Congelarlo** con el candado de fronteras y cortarlo en un plan propio si duele |
| D-8 | **Dónde vive el plan** | Aquí (`documentations/reorganizacion-modular/`) · en `documentations/planes/` del ecosistema | **Aquí**, porque quien implementa solo ve este repo; con una ficha-puntero en el ecosistema |
| D-9 | **El arranque paralelo de la transición** (`04` §2) | Nombre del `cmd` temporal: `cmd/server-modular` u otro · ¿se despliega en UAT en sustitución antes del relevo, o solo se prueba en local? | `cmd/server-modular`, que en F10 (relevo) desaparece y `cmd/server` pasa a usar el arranque nuevo, para que el despliegue (`go build -o bin/server ./cmd/server`) no cambie. Probarlo en UAT en sustitución **una vez** antes del relevo |
| D-10 | **`publicapi` con el método de `05`** | Repartir sus handlers en `modulos/<m>/http/` · reconstruirla como una cara única nueva · otra | **Repartir**: el arranque viejo sigue con el `publicapi` viejo y el nuevo monta el transporte de cada módulo (`05` §8.2). Cambia el árbol de `04` |
| D-11 | **Las etiquetas** | `pendiente` (rojo) e `integracion` (procesos, F9) u otros nombres | Esos dos, y **ningún `t.Skip`** en código nuevo (`05` E-5) |
| D-12 | **Umbral de cobertura por fichero al llegar a verde** | 80 % · otro · sin umbral | **80 %** de sentencias por fichero, fuera los adaptadores Postgres (`05` E-9). Recalibrar tras el piloto F1 |
| D-13 | **La lista de procesos de integración** (F9) | La candidata de `05` §7.2 u otra | Cerrarla antes de F9; escribir cada proceso **contra el binario viejo primero** |
