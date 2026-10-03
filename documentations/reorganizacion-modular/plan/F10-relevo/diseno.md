# F10 · Diseño — el inventario medido, la prueba de UAT paso a paso, y lo de fuera del repo

## 1 · Lo que se borra (medido el 2026-09-28 sobre `dev` @ `1b18932`)

Comando: `for d in $(ls -d internal/*/ | grep -v '^internal/platform/$'); do echo "$d $(find $d -name '*.go' ! -name '*_test.go' | wc -l) $(find $d -name '*_test.go' | wc -l)"; done`.
Los números **crecerán** hasta F10 (arreglos «hechos dos veces», `huella_vieja_test.go` de F0): la
tarea T10.1 los vuelve a medir.

| Directorio | Prod | Test | | Directorio | Prod | Test |
|---|---|---|---|---|---|---|
| `internal/bootstrap/` | 22 | 20 | | `internal/intake/` | 24 | 38 |
| `internal/casebank/` | 4 | 3 | | `internal/intakeahead/` | 3 | 2 |
| `internal/catalogimport/` | 6 | 5 | | `internal/intakes/` | 28 | 59 |
| `internal/contracts/` | 0 | 1 | | `internal/integrations/` | 9 | 10 |
| `internal/degradation/` | 2 | 2 | | `internal/intentcfg/` | 2 | 2 |
| `internal/diagnostics/` | 2 | 2 | | `internal/llmvia/` | 4 | 10 |
| `internal/entitlements/` | 3 | 6 | | `internal/platformadmin/` | 4 | 5 |
| `internal/evidence/` | 1 | 1 | | `internal/prompts/` | 2 | 1 |
| `internal/filtercfg/` | 1 | 2 | | `internal/publicapi/` | 33 | 64 |
| `internal/flujos/` | 79 | 171 | | `internal/reanalisis/` | 1 | 2 |
| `internal/gateway/` | 28 | 50 | | `internal/receipts/` | 4 | 2 |
| `internal/iam/` | 44 | 31 | | `internal/tenantllm/` | 2 | 1 |
| `internal/inferstats/` | 1 | 1 | | `internal/tenantvars/` | 3 | 1 |
| `internal/ingest/` | 2 | 3 | | `internal/turnoacotado/` | 3 | 2 |

**Total: 28 directorios, 317 ficheros de producción y 497 de test.** Más `cmd/server-modular/`
(F0), `cmd/server/flows_integration_test.go` (D-F10-4) y, si D-F10-5, los 9 de §6.
Se queda `internal/platform/` (27 + 34), y `cmd/` con sus 5 `main.go`.

## 2 · La prueba en UAT en sustitución (D-9) — necesita UAT por SSH

Datos de UAT citados de la documentación de operación del ecosistema (fuera de este repo:
`documentations/operacion/despliegue-uat.md`, medido allí el 2026-08-30). **Ni un secreto aquí**: las
credenciales viven en el `EnvironmentFile` de la máquina
(`/root/source/wApp/cloud/wapp-cloud-platform/.env`, modo `600`, 33 variables).

| Dato | Valor |
|---|---|
| Acceso | `ssh wapp-vps` |
| Unidad | `wapp-cloud.service`; `ExecStart` = `/root/source/wApp/cloud/wapp-cloud-platform/bin/server` (el único cuyo binario vive **dentro** del checkout) |
| Log | `/root/source/wApp/logs/cloud.log` (`StandardOutput=append:`, **no** journald) |
| Base | Postgres 17 en Docker, contenedor `wapp-postgres` (`postgres:17-alpine`); esquema `0.48.0` |
| S3 | MinIO local (`minio-dev`), bucket `edugo-materials` |
| Rama | Repo con plan en curso ⇒ UAT se sirve de **`dev`** (`despliegue-uat.md` §3) |
| Dependientes | `wapp-bff` (`PartOf=wapp-cloud`: se reinicia con él), `wapp-client-console` y `wapp-platform-console` (**sin** `PartOf`: hay que reiniciarlas a mano), `wapp-edge` (reconecta solo) |

### 2.1 · Antes de la ventana (captura)

```bash
cd /root/source/wApp/cloud/wapp-cloud-platform
pid=$(systemctl show -p MainPID --value wapp-cloud)
go version -m /proc/$pid/exe | grep -E '^\s+path|vcs\.(revision|modified)'   # el viejo: path …/cmd/server
cp bin/server bin/server.viejo-$(git rev-parse --short HEAD)                 # marcha atrás
md5sum bin/server bin/server.viejo-*
curl -s 127.0.0.1:8100/metrics | grep -oE '^wapp_[a-z_]+' | sort -u > /root/metricas-antes.txt
docker exec wapp-postgres pg_dump -U wapp -d wapp -Fc > /root/backups/wapp-antes-f10-$(date +%F).dump
GOWORK=off go run ./cmd/migrate -status                                    # version y content_hash del viejo
```

### 2.2 · El cambio de binario

```bash
git fetch origin && git checkout dev && git reset --hard origin/dev         # el SHA que cerró F9
GOWORK=off go build -o bin/server ./cmd/server-modular                      # 🔴 el modular, en la MISMA ruta
GOWORK=off go run ./cmd/migrate -status                                     # mismo content_hash que en 2.1: si no, PARAR
systemctl restart wapp-cloud                                                # restart, nunca stop+start
systemctl restart wapp-client-console wapp-platform-console
```

No se toca la unidad systemd ni el `.env`: se sustituye **el fichero**. Es lo que hace que la marcha
atrás sea copiar un fichero.

### 2.3 · Verificación inmediata (los 10 primeros minutos)

1. **Qué corre**: `go version -m /proc/$(systemctl show -p MainPID --value wapp-cloud)/exe` → `path …/cmd/server-modular`, `vcs.revision` = SHA de 2.2, `vcs.modified=false`; `md5sum` de `/proc/$pid/exe` = `bin/server`.
2. **Las líneas de clave** (§9 del despliegue): `grep -E "clave pública de(l)? (cifrado|lease)" /root/source/wApp/logs/cloud.log | tail -2` → `key_source=config` o `file`. 🔴 `generated` = el Edge no puede abrir ni validar: **vuelta atrás ya**.
3. **Las nueve fases**: `grep 'arranque: fase completada' /root/source/wApp/logs/cloud.log | tail -9` (el orquestador nuevo conserva la línea).
4. **Salud**: `curl -s 127.0.0.1:8100/healthz` (200, postgres `healthy`), `:8104/healthz`, `:8106/healthz`, `:8107/healthz`.
5. **Edge**: la sesión vuelve a `online` en la consola de plataforma (`GET /admin/tenants/{id}/installations`) en menos de un minuto; `LeaseUpdate` válido en el log del Edge.
6. **Métricas**: `curl -s 127.0.0.1:8100/metrics | grep -oE '^wapp_[a-z_]+' | sort -u | diff /root/metricas-antes.txt -` → vacío (o solo `CounterVec` que aún no se incrementaron, `contratos.md` §8: se anotan y se re-miran al final).

### 2.4 · Durante la ventana (D-F10-1: 24 h mínimo) — criterios

| Criterio | Cómo se mira | Corte (vuelta atrás) |
|---|---|---|
| Un mensaje real entra y se contesta | El e2e con WhatsApp real (runbook del ecosistema `operacion/runbooks/e2e-con-whatsapp-real.md`: comprobar antes sus «tres puertas», que existen porque un e2e contra el binario equivocado **parece verde**). Alternativa sin cliente real: el inyector de entrantes sintéticos del Edge (ADR-0041, apagado por defecto) | Un entrante sin respuesta o una respuesta distinta de la del viejo |
| Un borrador nace por el pipeline P2→P4 (vía local, el Ollama de la máquina) | `select count(*) from intakes where created_at > <inicio>` y la bandeja de la consola del cliente | Jobs en `pending` sin avanzar: `select count(*) from intake_jobs where status in ('pending','processing') and created_at < now()-interval '10 min'` > 0 (vocabulario de `status`: `0072_…sql:479`) (el fallo de las goroutines de fondo es **mudo**: `documentations/operacion.md` §5.3) |
| La dueña aprueba y el CRM recibe (si el tenant de UAT tiene puente) | `webhook_outbox` entregado; log del puente | Entregas atascadas |
| Ni un error nuevo | `grep -c 'level=ERROR' cloud.log` por hora, contra la misma hora del día anterior con el viejo | Un tipo de error que el viejo no daba |
| Los crons de la máquina siguen vivos | `ls -la /root/source/wApp/logs/autoreply-streak.tsv` (horario) | `mtime` > 1 h |
| Los textos al cliente y a la dueña no cambian | Revisión del e2e: mismo menú, mismos avisos | Cualquier diferencia de texto |

### 2.5 · Marcha atrás (objetivo < 5 min)

```bash
cd /root/source/wApp/cloud/wapp-cloud-platform
cp bin/server.viejo-<sha> bin/server
systemctl restart wapp-cloud && systemctl restart wapp-client-console wapp-platform-console
# y repetir 2.3 · 1–4 sobre el viejo: path …/cmd/server
```

Es segura porque **el esquema no cambió** (mismo `content_hash` en 2.1 y 2.2). Lo que se pierde es lo
de cualquier reinicio (estado en memoria). Los datos de negocio escritos por el modular durante la
ventana los lee el viejo sin conversión: mismas tablas, mismo cifrado (misma KEK del `.env`). El volcado
de 2.1 es la red para el caso que nadie espera.

### 2.6 · Acta

En el acta de T10.6 (`traspasos/TRASPASO-F10-relevo.md`): hora de inicio y fin, SHA, salidas de 2.3, tabla de 2.4 rellenada con
números, veredicto de Jhoan («sigue» → D-F10-2; «vuelta atrás» → hallazgos y vuelta a la fase del
módulo culpable).

## 3 · Fuera del repo (F10-05: necesita la raíz de wApp y los repos hermanos)

### 3.1 · La documentación del ecosistema

| Qué | Comando (desde la raíz de wApp) | Hoy |
|---|---|---|
| Menciones de `internal/` (de **todos** los repos) | `grep -rno 'internal/' documentations --include='*.md' \| wc -l` | **2.623** en **246** ficheros |
| Menciones de los 28 paquetes viejos de este repo | `P='internal/(bootstrap\|casebank\|catalogimport\|contracts\|degradation\|diagnostics\|entitlements\|evidence\|filtercfg\|flujos\|gateway\|iam\|inferstats\|ingest\|intake\|intakeahead\|intakes\|integrations\|intentcfg\|llmvia\|platformadmin\|prompts\|publicapi\|reanalisis\|receipts\|tenantllm\|tenantvars\|turnoacotado)\b'; grep -rnoE "$P" documentations --include='*.md' \| wc -l` | **1.124** en **170** (cota superior: `internal/bootstrap` también existe en el BFF y las consolas) |
| Bóveda de análisis y `CLAUDE.md` de la raíz | el mismo `P` sobre `analisis` y `CLAUDE.md` | **16** (15 en 5 notas de `analisis/` + 1 en `CLAUDE.md`) |
| `.md` de repos hermanos | el mismo `P` con `--include='*.md'` en cada repo | BFF **1** (1 fichero) · `wapp-cloudlink` **3** (2) · `wapp-shared` **1** (1) |

**Cómo**: con la tabla de `04` §4 (gana el prefijo más largo: `internal/flujos/contact` →
`internal/nucleo/contact` aunque `internal/flujos` → `internal/modulos/conversacion`), más las filas
que cambió D-10 (`internal/publicapi` → `internal/apipublica`) y D-9 (`internal/bootstrap` y
`internal/bootstrap/arranque` → `internal/arranque`). Las citas **históricas** (bitácoras, planes
cerrados, ADR con fecha) no se reescriben: se les añade la ruta nueva entre corchetes, para no falsear
lo que era cierto el día que se escribió. 🔴 `docs/` de la raíz es la documentación **vieja y condenada**:
no se toca.

### 3.2 · ADR-0010 y su regla de conteo

Texto de hoy (ADR-0010, §«Cómo se comprueba», fuera de este repo): *«Regla de conteo declarada. "Un
módulo toca una tabla" = existe un `.go` **no-test** de ese módulo (**primer segmento bajo
`internal/`**) con una línea que casa `(FROM|UPDATE|INSERT INTO|JOIN|DELETE FROM)\s+(public\.)?<tabla>`,
insensible a mayúsculas. Es un subconteo […]»*, y su «Nota de escala» cuenta **29 módulos** con
`ls cloud/wapp-cloud-platform/internal/`.

Tras el relevo, el primer segmento es `modulos` para **todo** el negocio: la regla contaría **un** módulo
y su tabla de «14 tablas que toca más de un módulo» se vaciaría sin que nada hubiera mejorado.
**Propuesta** (D-F10-7): *«módulo» = el segundo segmento bajo `internal/modulos/` (`acceso`, `edge`,
`conversacion`, `catalogo`, `captacion`, `inferencia`, `solicitudes`), más `internal/nucleo`,
`internal/platform`, `internal/arranque` e `internal/apipublica` como unidades propias; y el
`ls` de escala pasa a `ls internal/modulos/`.* Hay que re-medir su tabla con la regla nueva y decirlo
en el ADR. También caducan sus citas `internal/bootstrap/bootstrap.go:1519`, `:817`, `:835`.

### 3.3 · Los comentarios de repos hermanos (12, no 5)

Comando (desde la raíz de wApp):
`P='internal/(casebank|catalogimport|contracts|degradation|diagnostics|entitlements|evidence|filtercfg|flujos|gateway|iam|inferstats|ingest|intake|intakeahead|intakes|integrations|intentcfg|llmvia|platformadmin|prompts|publicapi|reanalisis|receipts|tenantllm|tenantvars|turnoacotado)\b'; for r in guardian/wapp-guardian-bff guardian/wapp-client-console guardian/wapp-platform-console edge/wapp-edge-agent edge/wapp-edge-intent cloud/wapp-cloudlink shared/wapp-shared; do grep -rnE "$P" --include='*.go' $r | grep -vE '"github.com/EduGoGroup/'; done`

| Repo | Fichero:línea | Cita |
|---|---|---|
| `wapp-guardian-bff` | `internal/apiclient/auth.go:123` | `internal/platformadmin/signup.go` |
| `wapp-guardian-bff` | `internal/web/signup_test.go:85` | `internal/platformadmin/signup.go` |
| `wapp-client-console` | `internal/apiclient/intakes.go:79` | `internal/intakes/status.go` |
| `wapp-client-console` | `internal/apiclient/intakes_test.go:123` | `internal/publicapi/publicapi.go` |
| `wapp-client-console` | `internal/web/solicitudes_acciones.go:160` | `internal/intakes/…` |
| `wapp-client-console` | `internal/web/solicitudes_gate.go:14` | `internal/entitlements/middleware.go` |
| `wapp-client-console` | `internal/web/flash.go:309` | `internal/intakes/…` |
| `wapp-client-console` | `internal/web/solicitudes_estado.go:16` | `internal/intakes/status.go` |
| `wapp-platform-console` | `internal/web/catalog_test.go:239` | `internal/entitlements/postgres.go:151 y :235` |
| `wapp-edge-agent` | `cmd/wapp-ctl/auth.go:341` | `internal/platformadmin/…` |
| `wapp-cloudlink` | `internal/server/server.go:7` | `internal/gateway/grpc` |
| `wapp-shared` | `llm/plantilla.go:34` | `internal/prompts` |

Son **comentarios**: el cambio no altera ningún binario. Cada repo recibe su commit a `dev`
(`docs: rutas de wapp-cloud-platform tras la reorganización modular`), con **su** gate local
(`make ci-local` de ese repo). `wapp-shared` es monorepo con releases por módulo: un cambio de
comentario **no** exige tag.

Aparte, y sin ruta (no cambian): `platformadmin.ApprovePartialResult` en
`wapp-platform-console/internal/adminclient/{transport.go:81,access_requests.go:95}` — el nombre del
paquete se conserva (D-4).

## 4 · La documentación de este repo (commit 9)

Comando: `P='internal/(bootstrap|casebank|…|turnoacotado)\b'` (el de §3.1) `; for f in CLAUDE.md README.md documentations/*.md .claude/skills/*/SKILL.md; do n=$(grep -oE "$P" $f | wc -l); [ $n -gt 0 ] && echo "$n $f"; done`.

| Fichero | Menciones hoy |
|---|---|
| `documentations/constitucion.md` | 41 |
| `documentations/deuda.md` | 40 |
| `documentations/contratos.md` | 25 |
| `documentations/arquitectura.md` | 21 |
| `documentations/esquema-postgres.md` | 6 |
| `documentations/README.md` | 5 |
| `documentations/operacion.md` | 3 (una es el `internal/publicapi/flows.go:75` equivocado: ver F9 README) |
| `CLAUDE.md` | 2 (y la sección «Reorganización modular en curso» pasa a «hecha») |
| `README.md` | 1 |
| `.claude/skills/{contrato-tdd,procesos-testcontainers,reconstruir-modulo}/SKILL.md` | 1 + 1 + 1 (y `procesos-testcontainers` pierde `WAPP_PROCESOS_BINARIO`; `reconstruir-modulo` y `contrato-tdd`, lo que diga D-F10-3) |
| **Total** | **147** |

`documentations/reorganizacion-modular/` **no** se reescribe: es la historia de la reconstrucción.
Solo `ESTADO.md` y `README.md` cambian de estado («relevo hecho»).

## 5 · La dorada de la huella

`internal/arranque/testdata/huella-vieja.golden`: la misma huella que F0 mide (rutas registradas en
ejecución, rpc, nombres de métricas, goroutines de fondo, variables efectivas), serializada **ordenada**
y en texto, una entrada por línea. La genera un test del arranque viejo **mientras existe** (el
`huella_vieja_test.go` de F0 con una bandera de regeneración) y `huella_test.go` la compara contra el
arranque único. Regenerarla es cambiar un contrato hacia fuera (`03` §1): commit propio y decisión escrita.

## 6 · Los tests con BD de `internal/platform` (D-F9-4 / D-F10-5)

«Qué protege» sale del **nombre** del fichero: T9.35 los lee enteros (E-8) antes de escribir P10.

| Fichero | Qué protege |
|---|---|
| `internal/platform/crypto/rekey_integration_test.go` | Rotación de KEK sobre filas reales |
| `internal/platform/metrics/flowlifecycle/collector_integration_test.go` | El colector de `flow_events` |
| `internal/platform/storage/postgres/contacts_integration_test.go` | Contactos cifrados en la capa de almacenamiento |
| `internal/platform/storage/postgres/drop_pii_claro_integration_test.go` | La migración `0070` (sin PII en claro) |
| `internal/platform/storage/postgres/integration_test.go` | Conexión, pool, tenant |
| `internal/platform/storage/postgres/migrations/grants_integration_test.go` | Grants sembrados por migración |
| `internal/platform/storage/postgres/migrations/replay_integration_test.go` | Full-replay idempotente |
| `internal/platform/storage/postgres/profile_replay_integration_test.go` | Réplica de perfiles |
| `internal/platform/storage/postgres/replay_integration_test.go` | Réplica |

9 ficheros, 25 `Test*` (`grep -rh '^func Test' <los 9> | wc -l`), 12 usos de `WAPP_TEST_DB_DSN`.
Con P10 (F9 · T9.35) en verde, se borran en el commit 8.
