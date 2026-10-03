# Traspaso F1 · bloque C · el adaptador y la conmutación de `nucleo/contact` (web → local)

Contexto: F1 (`nucleo/contact`, el piloto). La sesión web **F1-03** (2026-10-03) escribió el adaptador
`internal/arranque/bridge_contact.go` (T1.14 rojo, T1.15 verde) y conmutó el arranque nuevo para que cablee el
resolver nuevo (T1.16). Lo recibe la sesión local **F1-04** (bloque D, T1.17–T1.19), **junto con** el traspaso del
bloque B, [`TRASPASO-F1-B-suite-postgres.md`](TRASPASO-F1-B-suite-postgres.md) (T1.18, la suite contra Postgres), que
**sigue abierto** y no se cierra aquí: F1-04 cierra los dos.

═══ 0. BLOQUEANTE ═══

Hay que integrar en `dev` el PR de la rama `reorg/f1-c-adaptador` (**sin squash**: rojo, verde, refactor y conmutar son
commits distintos). Aparte de eso, ninguno. `make toolchain` → `TOOLCHAIN=OK`; en local, si falta el lint, `make tools`.

═══ 1. Rama y commits ═══

Rama `reorg/f1-c-adaptador`, partida de `origin/dev` @ `61a3c8b` (con el PR #23 del bloque B y el #24 de D-F1-9 ya
dentro). Todo está empujado; la sesión no deja nada sin pushear.

| SHA | Commit | Tarea |
|---|---|---|
| `09f4b72` | `rojo(arranque): contrato del adaptador de contact` | T1.14 |
| `0c2bddf` | `verde(arranque): adaptador de contact` | T1.15 |
| `a62abea` | `refactor(arranque): nombres en inglés en el adaptador de contact (E-11)` | T1.15 (tres identificadores del verde) |
| `ce98595` | `conmutar(nucleo): el arranque nuevo cablea nucleo/contact` | T1.16 |
| (cierre) | `docs(reorganizacion-modular): F1-03 cierra el bloque C` | tareas, README, DECISIONES, ESTADO, este traspaso |

═══ 2. go.mod ═══

Sin cambios. `go 1.26.5` intacto y `go.sum` intacto (el bloque no importa nada de fuera del módulo).

═══ 3. Gates que la web corrió ═══

Sobre `ce98595` (después solo cambian `.md`). El rc se leyó del log, sin pipe. Toolchain: `GO_EFFECTIVE=go1.26.5`,
`LINT_EFFECTIVE=v2.12.2 /usr/local/bin/golangci-lint`.

```
make toolchain → TOOLCHAIN_RC=0 · TOOLCHAIN=OK
GOWORK=off make ci-local → CI_RC=0 · lint 0 issues · FICHEROS_EVALUADOS=14 · POR_DEBAJO=0 · EXENTOS_POSTGRES=1 · 0 SKIP en el log
make vet-pendiente → rc=0
make test-pendiente → rc=0 · PENDIENTES=0 · ROJOS=0
grep -rn 'pendiente.Implementar' internal/nucleo internal/arranque | wc -l → 0
GOWORK=off go test -race -count=1 -v ./internal/arranque/ -run TestHuella → rc=0 · TestHuella PASS · TestHuellaEstatica PASS
GOWORK=off go test -race -count=1 -v ./internal/nucleo/... ./internal/arranque/... ./internal/modulos/... → rc=0 · 542 PASS · 0 FAIL · 0 SKIP
go test -coverprofile (./internal/arranque/) → bridge_contact.go 18/18 sentencias = 100,0 %  (a mano: cobertura-ficheros no mira internal/arranque)
GOWORK=off go list -deps ./cmd/server-modular | grep -c 'internal/nucleo/contact$' → 1
GOWORK=off go list -deps ./cmd/server | grep -c 'internal/nucleo/' → 0
grep -rn 'viejo\.NewPostgresResolver\|flujos/contact.*NewPostgresResolver' internal/arranque | wc -l → 0
git diff --stat 77df20f..HEAD -- internal/flujos internal/bootstrap internal/gateway internal/intakes internal/publicapi cmd/server → vacío
ls internal/platform/storage/postgres/migrations/structure | wc -l → 84
```

Mutaciones (cambio local, revertido, no commiteado): en `flows.go`, `newContactResolver(db, crypto.NewFieldCipher(kp), kp)`
→ el test de cableado rc=1 («no usa el FieldCipher de la fase 3»); `viejo.NewPostgresResolver(db, cipher, kp)` → rc=1
(«quiero *contactBridge»). Con `"nucleo"` en `Conmutados` → `go test -run Fronteras ./internal/modulos/` rc=1, 3 violaciones de la regla 3.

**No corrido en la web**: `make test-procesos` (el bloque C no toca `test/procesos`; lo que hay que correr es el T1.18
del otro traspaso), `make ci-docker`, `make test-integration`, el arranque real de `cmd/server-modular`.

═══ 4. Lo que solo la sesión local puede hacer ═══

1. **T1.17** — repetir la §3 con la toolchain local fijada (`make toolchain` → `TOOLCHAIN=OK`), sobre `dev` con el PR
   integrado. Esperado: lo mismo que arriba.
2. **T1.18** — la suite contra Postgres: la §4 de [`TRASPASO-F1-B-suite-postgres.md`](TRASPASO-F1-B-suite-postgres.md).
   Con este bloque dentro, además, `make test-procesos` contra el binario **nuevo** ya ejercita el resolver nuevo de
   punta a punta (P0 arranca `server-modular`, que ahora construye `contactBridge`):
   ```bash
   make test-procesos > /tmp/procesos.log 2>&1; echo rc=$?    # viejo y nuevo: RC=0 · FAIL=0 · SKIP=0
   ```
3. **Arranque real de `cmd/server-modular`** contra una base desechable (como T0.23, nunca a la vez que `cmd/server`
   contra la misma BD o los mismos puertos): las 9 fases arrancan y `/healthz` da 200. Es la única prueba de que la fase
   3 real (con `HeadBucket` y el KMS/keyring de verdad) construye el adaptador sin error; el test de cableado usa la fase
   3 del contenedor de la huella, con un S3 falso en proceso.
4. **T1.19** — el informe del piloto: costes de este bloque en las líneas `Piloto:` de los cuatro commits
   (11 · 2 · 3 · 20 min), y los hallazgos 30–34 del README de F1.

═══ 5. Lo que quedó sin tocar ═══

- `internal/modulos/fronteras_test.go`: **`Conmutados` sigue vacía** (hallazgo 30, D-F1-15 abierta).
- Los candados de fichero no evalúan `internal/arranque/bridge_*.go` (hallazgo 32, D-F1-16 abierta).
- Todo el código viejo; `fase3_almacenes.go`, `fase6_solicitudes.go` y `fase7_flujos.go` de la copia (no hizo falta:
  siguen pasando `c.flowDeps.contacts`, que ahora es el adaptador).
- El traspaso del bloque B: abierto, para F1-04.

═══ 6. Integración en dev ═══

Fusionar el PR de `reorg/f1-c-adaptador` en `dev` **sin squash** («Create a merge commit» o `merge --no-ff`). No hay que
reordenar nada. Después, F1-04 trabaja sobre `dev` y cierra este fichero y el del bloque B con su `## CERRADO <fecha>`.

═══ 7. Tres cosas que quiero que revises con ojo crítico, no que aceptes ═══

1. **El campo del adaptador es el puerto nuevo, no el tipo concreto.** Arquitectura §3 decía
   `struct{ nuevo *contact.PostgresResolver }`; se escribió `next contact.Resolver` para poder probar `Resolve`/`Destino`
   con un doble y sin BD (T1.15 pide ≥ 80 %). El test de cableado compensa afirmando el tipo concreto **y** la misma
   instancia de `db`, `cipher` y `kp` **por reflexión sobre campos no exportados** de `nucleo/contact`. Refútalo: ¿hay
   algún camino por el que el arranque nuevo construya el resolver sin pasar por `newContactResolver`? (`grep -rn
   'NewPostgresResolver' internal/arranque cmd/server-modular`). ¿Es aceptable que un test de `arranque` lea campos
   privados de `nucleo`?
2. **«Huella igual» no prueba nada de `contact`** (T-9: sin rutas, rpc, métricas ni goroutines), y el `go list -deps` que
   pide la tarea daba 1 **desde el rojo** (hallazgo 31). Lo que doy por cierto es que la fase 3 real construye el
   adaptador con las claves de la fase; lo vi con el contenedor de la huella (S3 falso, keyring de env), **no** con el
   arranque real ni con `WAPP_KEK_PROVIDER=kms`. Haz el §4.3 y, si puedes, un `Resolve` real a través de
   `server-modular` (P3 aún no existe: basta con que P0 y la suite de T1.18 pasen contra el binario nuevo).
3. **La equivalencia viejo ↔ nuevo se midió sobre un corpus escrito a mano** (109 casos de R-01…R-07, R-22, N-05; cero
   diferencias). Que el corpus sea suficiente para afirmar que `contacts.value_bidx`, `fleet_sessions.self_pn_bidx` y el
   anti-self-loop no cambian (T-10) es una afirmación por lectura del código de `Normalize`, no una prueba exhaustiva.
   Compara `normalizePhone`/`normalizeLID`/`normalizeUsername` de `internal/flujos/contact/contact.go` y
   `internal/nucleo/contact/contact.go` línea a línea, y busca una entrada (Unicode, dígitos no ASCII, `@` repetidos) que
   las separe. Ojo también: el `refactor(arranque)` `a62abea` solo renombra; compruébalo con `git show a62abea`.

═══ 8. Decisiones que necesitan a Jhoan ═══

- **D-F1-15** (nueva, abierta): ¿cuándo entra un módulo en `Conmutados` si sus tipos viejos siguen vivos tras un
  `bridge_<x>.go`? Recomendación: cuando muere su último adaptador (F8 para `nucleo`), corrigiendo el comentario.
- **D-F1-16** (nueva, abierta): ¿los candados de fichero evalúan `internal/arranque/bridge_*.go`? Recomendación: sí,
  solo esos.
- El resto de **D-F1-9** (¿`puente_<x>.go` → `bridge_<x>.go` en F2–F7?) y **D-F1-14** siguen abiertas.
- **T1.20 · la parada**: después del informe (T1.19), nadie empieza F2 sin la decisión de Jhoan.

## CERRADO 2026-10-02

Sesión **F1-04** (💻 CLI), sobre `dev` @ `ddcf7de`. La rama `reorg/f1-c-adaptador` ya estaba integrada sin squash
(PR #25, merge `ddcf7de`): se ven por separado `09f4b72` rojo, `0c2bddf` verde, `a62abea` refactor y `ce98595` conmutar.
No se re-fusionó nada. Toolchain: `make toolchain` → `TOOLCHAIN=OK` · `go1.26.5` · lint `v2.12.2` (`.bin/`).

### Qué se hizo

- **T1.17 (§3 repetida)**: idéntica a la de la web.
  - `make ci-local` → `GATE_RC=0` · 0 issues · `FICHEROS_EVALUADOS=14` · `POR_DEBAJO=0`.
  - `make vet-pendiente` rc=0 · `make test-pendiente` `PENDIENTES=0 · ROJOS=0` · `pendiente.Implementar` en `nucleo` y
    `arranque` = 0.
  - `go test -race -count=1 -v ./internal/{nucleo,arranque,modulos}/...` → rc=0 · **542 PASS · 0 FAIL · 0 SKIP**.
  - `bridge_contact.go`: 18/18 sentencias, 100 %.
  - `go list -deps`: 1 · 0. `grep viejo.NewPostgresResolver` → 0.
  - `git diff --stat 77df20f..HEAD` del código viejo → vacío. Migraciones: 84.
- **T1.18**: ver el `CERRADO` de [`TRASPASO-F1-B-suite-postgres.md`](TRASPASO-F1-B-suite-postgres.md).
  - `make test-procesos`: viejo y nuevo `RC=0 · 196 PASS · 0 FAIL · 0 SKIP`. P0 arranca `server-modular` con
    `contactBridge` dentro.
- **§4.3, arranque real de `cmd/server-modular`** (como T0.23):
  - Entorno: `postgres:16` efímero con base vacía, puertos `181xx`, `HeadBucket` real contra el R2 de desarrollo de
    `.env` (credenciales no impresas) y `WAPP_KEK_PROVIDER=env` con claves generadas al vuelo.
  - Resultado: **9/9 fases** (fase 1 en 363 ms con migraciones `0.48.0`; fase 3 en 479 ms), `:8100/healthz` **200**,
    0 líneas `ERROR`/`WARN`. SIGINT → `servidor detenido limpiamente`, `EXIT=0`. Contenedor borrado.
  - **No corrido**: `WAPP_KEK_PROVIDER=kms`, porque no hay KMS en local.
- **T1.19**: [`informe-piloto.md`](../plan/F1-nucleo-contact/informe-piloto.md).

### §7, punto por punto

1. **El resolver se construye solo por `newContactResolver`: confirmado en el código; la red de seguridad, refutada en parte.**
   - El `grep` da un único constructor (`bridge_contact.go:66`), y `server-modular` no enlaza `internal/bootstrap`.
   - Los dos consumidores (`fase6_solicitudes.go:43`, `fase7_flujos.go:229`) reciben `c.flowDeps.contacts`.
   - Pero un mutante que en `fase7_flujos.go` pasa el `NewPostgresResolver` **viejo** sobrevive: vet rc=0, 536 PASS en
     `arranque`, y el `grep` de la §3 da 0. El test de cableado mira `flowDeps.contacts`, no lo que reciben las fases
     (hallazgo 39).
   - La reflexión sobre campos privados es aceptable porque falla ruidosamente. La alternativa limpia, una costura con
     espía en `arranque`, está en el hallazgo 39.
2. **«Huella igual no prueba `contact`»: confirmado.**
   - El arranque real con fase 3 y `HeadBucket` construye el adaptador sin error.
   - P0 y la suite pasan con el binario nuevo.
   - KMS sin probar.
3. **Equivalencia viejo ↔ nuevo: confirmada.**
   - `a62abea` solo renombra: tras invertir los tres renombres, `diff` rc=0 byte a byte.
   - `normalize*`, `Normalize`, `NewRef`, `RefsFrom`, `Sendable`, `pickDestino` y `dedupeRefs` son idénticos token a
     token.
   - Prueba diferencial, con cero diferencias:
     - tabla adversaria, 728 comparaciones;
     - 75.712 tripletas de `RefsFrom`;
     - *fuzz* de 60 s por función, 3.370.994 ejecuciones.
   - El corpus a mano de 109 casos **no** caza el mutante `IndexByte` → `LastIndexByte` en `normalizeLID`: la equivalencia
     se sostiene por la identidad del código (hallazgo 40).
   - En producción sigue normalizando el código viejo, así que `value_bidx`, `self_pn_bidx` y el anti-self-loop no pueden
     cambiar por este bloque.

### Qué queda

- **T1.20, la parada**: Jhoan contesta P1–P7 en la §10 del informe. **No empieza F2.**
- Abiertas: D-F1-14, D-F1-15, D-F1-16 y el resto de D-F1-9. Hallazgos 35–41 sin decisión.
- **No corrido**: KMS, `make test-integration` (no se tocó código viejo), `make ci-docker`, UAT.
