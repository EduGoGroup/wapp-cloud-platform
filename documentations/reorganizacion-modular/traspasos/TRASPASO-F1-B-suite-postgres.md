# Traspaso F1 · bloque B · la suite de `contact` contra Postgres (web → local)

Contexto: F1 (`nucleo/contact`, el piloto). La sesión web F1-02 (2026-10-02) pasó a verde los cuatro ficheros
de `internal/nucleo/contact` y escribió T1.13, `test/procesos/contact_contrato_test.go`. La corrida que cuenta
es la de la sesión local **F1-04**, en **T1.18**. Este traspaso cubre solo eso. El traspaso del bloque C
(`TRASPASO-F1-nucleo-contact.md`, T1.16) es otro fichero y aún no existe.

═══ 0. BLOQUEANTE ═══

Hay que integrar en `dev` el PR de la rama `reorg/f1-b-verde` (sin squash). Antes de eso, T1.18 se corre sobre
la rama. Aparte de esto, ninguno. `make toolchain` → `TOOLCHAIN=OK`; en local, si falta el lint, `make tools`.

═══ 1. Rama y commits ═══

Rama `reorg/f1-b-verde`, partida de `origin/dev` @ `5847ad4`. Todo está empujado; la sesión no deja nada sin
pushear.

| SHA | Commit | Tarea |
|---|---|---|
| `ccc9a6b` | `docs(reorganizacion-modular): D-F1-7 y D-F1-8 decididas por Jhoan (2026-10-02)` | registro de decisiones |
| `9e8f740` | `verde(nucleo): contact/contact` | T1.8 |
| `8e7a891` | `verde(nucleo): contact/resolver` | T1.9 |
| `222c4c8` | `verde(nucleo): contact/repository_memory` | T1.10 |
| `8307afb` | `verde(nucleo): contact/repository_postgres` | T1.11 |
| `4bbd138` | `refactor(nucleo): Estado gana la marca (D-F1-7)` | D-F1-7, antes de T1.13 |
| `4bc398d` | `procesos(contact): la suite de contrato contra Postgres` | T1.13 + el grep de R9.4.d (D-F1-8) |
| (cierre) | `docs(reorganizacion-modular): F1-02 cierra el bloque B` | tareas, ESTADO, este traspaso |

✎ Después del cierre, Jhoan integró en `dev` el PR #22 (D-F9-11, partir los tests largos de `test/procesos`; `dev` @ `0a377bc`). La rama lo trae por **merge** (`a5d17b5`, sin conflictos), no por rebase, para que los SHA de esta tabla sigan valiendo. El merge no toca `base_test.go` ni `claves_test.go`, que son lo único del arnés que usa T1.13.

T1.12 no tiene commit: no hizo falta ningún `refactor`; la medición está en `tareas.md` y en el PR.

═══ 2. go.mod ═══

Sin cambios. `go 1.26.5` intacto y `go.sum` intacto (`github.com/google/uuid` ya estaba).

═══ 3. Gates que la web corrió ═══

Todos sobre `4bc398d`. El rc se leyó del log, sin pipe.

```
make toolchain → TOOLCHAIN_RC=0 · TOOLCHAIN=OK · GO go1.26.5 · LINT_EFFECTIVE=v2.12.2 /usr/local/bin/golangci-lint
GOWORK=off make ci-local → GATE_RC=0 · 88 líneas ok · lint 0 issues · FICHEROS_EVALUADOS=14 · POR_DEBAJO=0 · EXENTOS_POSTGRES=1
make vet-pendiente → rc=0
grep -rn 'pendiente.Implementar' --include='*.go' internal/nucleo | wc -l → 0   (antes del bloque: 11)
make test-pendiente → PENDIENTES=0 · ROJOS=0                                  (antes: 11 · 4)
make cobertura-ficheros → contact.go 97,6 % · resolver.go 100,0 % · repository_memory.go 95,6 %
                          · contacthelpertest/estado.go 92,6 % · repository_postgres.go EXENTO postgres (31,1 %)
go test -v ./internal/{modulos,nucleo,arranque}/... | grep -c -- '--- SKIP' → 0
GOWORK=off go vet -tags integracion ./test/procesos/... → rc=0
GOWORK=off go test ./test/procesos/ -run SinBDViva → rc=0
R9.4.d (comando nuevo, F9-procesos/requisitos.md) → vacío; con una sonda que importa platform/storage/postgres → la lista
git diff --stat origin/dev -- internal/flujos internal/bootstrap cmd/server → vacío
```

**Pre-chequeo web** (W-1: no cierra nada). Docker 29.6.2, `dockerd` arrancado a mano:

```
WAPP_PROCESOS_BINARIO=viejo GOWORK=off go test -tags integracion -race -v -run Contact ./test/procesos/
  → rc=0 · 20 --- PASS (19 casos + el padre) · 0 FAIL · 0 SKIP · padre 6,48 s · paquete 13,5 s
WAPP_PROCESOS_BINARIO=nuevo  (lo mismo)  → rc=0 · 20 PASS · 0 SKIP
```

Postgres estuvo listo en unos 2,5 s (7,5 s con la imagen en frío) y la plantilla migró en unos 0,6 s; estas
cifras las dio el sub-agente, que leyó el log. No hubo divergencias memoria ↔ Postgres.

**Repetidos sobre el merge `a5d17b5`** (con el PR #22 dentro), mismo método:

```
make toolchain → TOOLCHAIN=OK · GOWORK=off make ci-local → GATE_RC=0 · 88 ok · 0 issues · FICHEROS_EVALUADOS=14 · POR_DEBAJO=0
make test-pendiente → PENDIENTES=0 · ROJOS=0 · SKIP en código nuevo 0 · vet -tags integracion rc=0 · R9.4.d vacío
pre-chequeo -run Contact: viejo rc=0 · 20 PASS · 0 FAIL · 0 SKIP (padre 2,73 s) · nuevo rc=0 · 20 PASS · 0 FAIL · 0 SKIP
```

**No corrido**: `make test-procesos` completo, `make test-integration` y UAT.

═══ 4. Lo que solo la sesión local puede hacer ═══

T1.18, la corrida que cuenta (R1.3.c–d):

```bash
git fetch origin && git checkout reorg/f1-b-verde      # o dev, ya integrado
make toolchain                                          # TOOLCHAIN=OK
WAPP_PROCESOS_BINARIO=viejo GOWORK=off go test -tags integracion -race -v -run Contact ./test/procesos/ > /tmp/p.log 2>&1; echo rc=$?
grep -c -- '--- PASS' /tmp/p.log; grep -c -- '--- SKIP' /tmp/p.log; grep -c -- '--- FAIL' /tmp/p.log
# esperado: rc=0 · 20 PASS · 0 SKIP · 0 FAIL ; anotar tiempo total y arranque del contenedor
make test-procesos > /tmp/tp.log 2>&1; echo rc=$?       # viejo y nuevo; el contrato de contact va dentro
```

Además, para el informe: la lista de divergencias memoria ↔ Postgres (la web no vio ninguna) y los minutos.

═══ 5. Lo que quedó sin tocar ═══

- El código viejo entero: `internal/flujos/contact`, `internal/bootstrap` y `cmd/server`.
- Los tests viejos de integración de `contact` (`deadlock_integration_test.go`, `push_name_cifrado_integration_test.go`,
  `rekey_integration_test.go`), que se borran en F10.
- El bloque C (T1.14–T1.16: `puente_contact.go` y la conmutación), que no se empezó.
- D-F1-9 y D-F1-14, que siguen abiertas.

═══ 6. Integración en dev ═══

El PR de `reorg/f1-b-verde` va hacia `dev` **sin squash** (`gh pr merge --merge`): cada `verde` y el `refactor`
quedan como commits distintos. No hay que reordenar nada.

═══ 7. Tres cosas que quiero que revises con ojo crítico, no que aceptes ═══

1. **La marca de `Estado` va en `current_node`, y la suite la da por buena porque pasa.** El argumento es que
   `fuseDB` poda la fila del huérfano en conflicto o le cambia solo el `contact_id`, y nunca toca `current_node`.
   Lo leí en el código, no lo vi fallar. Para refutarlo: un mutante en `fuseDB` (copia del nuevo, nunca del viejo)
   que en conflicto conserve la fila del huérfano y borre la del canónico. `Fusion_ConflictoConservaElCanonico`
   tiene que caer contra Postgres. Si no cae, la marca no vigila lo que dice.
2. **El grep de R9.4.d mira imports directos, y eso deja pasar lo transitivo a propósito.** `contacthelpertest`
   arrastra `nucleo/contact`, y este, `platform/storage/postgres`. Con `-deps` todo eso saldría. Comprueba dos
   cosas: que la regla escrita en `F9-procesos/requisitos.md` (R9.4.d) dice lo mismo que el comando, y que un
   proceso de caja negra (P1…) que importe un paquete de dominio por error sí sale en la lista. La web lo probó
   solo con una sonda de `storage/postgres`.
3. **La afirmación aburrida: «el SQL de `repository_postgres.go` es el viejo, byte a byte».** Se comprobó con el
   `diff` de `public.*` (vacío) y comparando las 9 cadenas SQL. Pero `Resolve`, `fuseDB` y `attachRef` solo los
   ejerce la suite de 19 casos; el resto (31,1 % de cobertura del fichero) no lo cubre ningún test nuevo. Los tests
   viejos de deadlock (`40P01`), rekey y nombre cifrado no se portaron: los recoge P3 de F9 (D-F1-11). Mientras
   F9-03 no exista, el reintento de `postgres.WithTx` y la ráfaga sin `40P01` **no tienen test en el árbol nuevo**.

═══ 8. Decisiones que necesitan a Jhoan ═══

- **D-F1-9** (abierta): ¿se traducen `puente_contact.go`, `puenteContact` y `nuevoResolverDeContactos`? Hay que
  decidirla antes de T1.14 (bloque C, F1-03).
- **D-F1-14** (abierta, no bloquea).
- Lo nuevo del bloque está en los hallazgos 27–29 del [README de F1](../plan/F1-nucleo-contact/README.md):
  - el grep ciego de R9.4.d;
  - `make test-pendiente`, que cuenta los *worktrees* de `.claude/worktrees`;
  - E-11 en `test/procesos`: lo nuevo va en inglés, junto a un arnés que está en castellano.

## CERRADO 2026-10-02

Sesión **F1-04** (💻 CLI), sobre `dev` @ `ddcf7de`. La rama `reorg/f1-b-verde` ya estaba integrada sin squash (PR #23,
merge `ca364de`): no se re-fusionó nada. Toolchain: `make toolchain` → `TOOLCHAIN=OK` · `go1.26.5` · lint `v2.12.2`
(`.bin/`). Docker Desktop 29.8.0. Ningún `rc` se leyó con pipe.

### Qué se hizo

- **§3 repetida** (T1.17): `GOWORK=off make ci-local` → `GATE_RC=0` · 88 `ok` · 0 issues · `FICHEROS_EVALUADOS=14` ·
  `POR_DEBAJO=0` · `EXENTOS_POSTGRES=1`. `make test-pendiente` → `PENDIENTES=0 · ROJOS=0`. `vet -tags integracion` rc=0.
  R9.4.d vacío. Cobertura: 97,6 · 100 · 95,6 · 92,6 %. **Idéntico a la §3.**
- **T1.18, la corrida que cuenta**:
  - `-run Contact`, con `-race -count=1`:
    - `viejo`: rc=0 · 20 PASS · 0 FAIL · 0 SKIP · padre 1,77 s · paquete 14,9 s · Postgres listo en 2,58 s · plantilla en 0,65 s.
    - `nuevo`: rc=0 · 20 PASS · 0 FAIL · 0 SKIP · padre 1,87 s · paquete 10,7 s · Postgres listo en 1,59 s · plantilla en 0,57 s.
  - `make test-procesos`: viejo y nuevo `RC=0 · PASS=196 · FAIL=0 · SKIP=0` (33 s en total).
  - **Divergencias memoria ↔ Postgres: ninguna** (19/19 en los dos).

### §7, punto por punto

1. **La marca en `current_node`: confirmada, con un hueco.**
   - Dos mutantes de `fuseDB` caen: en conflicto se borra el canónico, y se copia el `current_node` del huérfano. En los dos
     falla `Fusion_ConflictoConservaElCanonico` (rc=1, 18 PASS · 2 FAIL).
   - Un tercero, que copia del huérfano `vars`, `last_wa_message_id`, `event_id` y `flow_version`, **sobrevive**
     (rc=0, 20 PASS): la marca solo vigila `current_node` (hallazgo 35 del README de F1).
2. **R9.4.d: refutada en parte.**
   - La sonda que importa `internal/flujos/contact` sí sale.
   - La regla y el comando no dicen lo mismo: el comando mira el paquete, no el fichero. Una sonda `p2_*_test.go` que
     importa `internal/nucleo/contact` y usa `NewMemoryResolver` **no** sale (hallazgo 36).
   - El comando no está en ningún gate (hallazgo 37).
3. **SQL byte a byte: confirmado.**
   - Las 9 cadenas SQL y sus 9 listas de argumentos son iguales en orden y bytes.
   - La cifra de cobertura se leía mal: la suite contra Postgres cubre el **80,7 %** de `repository_postgres.go` (85,2 %
     con los unitarios). El 31,1 % es solo el de los unitarios.
   - El reintento de `WithTx` (36,6 %) **no tiene test de ejecución en ningún árbol**: la afirmación de la §7.3 se queda
     corta (hallazgo 38). T9.15 tiene que cubrirlo antes de F10.

### Qué queda

- La parada **T1.20** (Jhoan, con [`informe-piloto.md`](../plan/F1-nucleo-contact/informe-piloto.md) delante).
- Hallazgos 35–38 sin decisión. Están en el README de F1 y entran en la P7 del informe.
- **No corrido**: `make test-integration`, porque el bloque no toca código viejo (diff desde `77df20f` vacío). Tampoco
  `make ci-docker` ni UAT.
