# F2 · `acceso` — IAM, derechos comerciales y operador de plataforma

> **Estado: cerrada** el 2026-10-04 (sesión F2-05 💻, cierre local sobre `dev` @ `bfd31ce`, PR #32 integrado; último commit de
> código de la fase `73b4541`; informe de fase al final de «Contradicciones encontradas»). En curso desde el 2026-10-04 (sesión F2-01 🌐, arranque sobre `dev` @ `9a77307`; inventario E-12 aprobado por
> Jhoan el 2026-10-04, [`diseno.md`](diseno.md) §1.1). Spec escrita el 2026-09-28 sobre `dev` @ `1b18932`, releída en `bad573a`.
> Norma: [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> Marco común (no se repite aquí): [`00-marco/`](../00-marco/README.md). Rutas: **autoridad**
> [`FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md). Patrones heredados de
> [`F1`](../F1-nucleo-contact/README.md): rojo **solo con exportados** (T-1 de F1: `unused` rompe el
> lint) y adaptador de arranque viejo ← nuevo en `internal/arranque/bridge_<x>.go` (`05` §4.2).
>
> Recalibrado el 2026-10-03 tras la parada de F1 (`05` E-12, §4.2, E-9, E-4; `plan/DECISIONES.md` §3).
>
> ✎ **D-F1-10 (Jhoan, 2026-10-02)**: los paquetes de suite de contrato y de dobles llevan el sufijo compuesto
> **`helpertest`**, el único que los candados de fichero eximen ([`DECISIONES.md`](../DECISIONES.md) §2). Esta spec los
> nombraba con `…test` (`entitlementstest`, `outtest`, `platformadmintest`): se actualizó el sufijo, nada más.

## Objetivo, en tres líneas

1. Reconstruir `internal/iam/**` (la única zona hexagonal), `internal/entitlements` e
   `internal/platformadmin` en `internal/modulos/acceso/…` con la ceremonia que fije el inventario E-12
   (simple / medio / complejo), con **suites de contrato** para los puertos con BD —corridas en memoria
   y en Postgres— y dobles nuevos donde faltan.
2. Conmutar: `cmd/server-modular` cablea el **único** resolver de derechos nuevo, el IAM nuevo y el
   plano de plataforma nuevo, y muda **23 rutas** de `:8103` a `internal/apipublica` y **8** de
   `:8100` a los handlers nuevos (mapa FX §5), con la huella idéntica.
3. Conservar los candados de seguridad: I-CP-5 (`.any`), el canje anti-oráculo y «una sola empresa
   por usuario», y el gate fail-closed de features (I-CP-6).

## Entradas (tiene que ser cierto para empezar)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F0 cerrado: `internal/arranque` (copia), `internal/apipublica` (vacío + estrangulador), `internal/pendiente`, candados de `05` §5, targets `test-pendiente`/`cobertura-ficheros` | `ls internal/arranque internal/apipublica internal/pendiente` · `grep -n 'test-pendiente\|cobertura-ficheros' Makefile` |
| E2 | Los tres ✎ de F0 en `platform` hechos, en particular `in.AuditInput` es **alias** del DTO de `platform/httpapi` (F0 `tareas.md` T0.18) | `go list -f '{{.Imports}}' ./internal/platform/httpapi \| grep -c internal/iam` → `0` |
| E3 | F1 cerrado **y** la parada del piloto resuelta por Jhoan a favor de seguir | `plan/F1-nucleo-contact/informe-piloto.md` con la decisión fechada |
| E4 | El código viejo de referencia no cambió desde esta spec | `git log --oneline 1b18932..origin/dev -- internal/iam internal/entitlements internal/platformadmin` vacío; si no, se relee [`diseno.md`](diseno.md) §E-8 |
| E5 | `dev` verde con la toolchain fijada | skill `validar-antes-de-cerrar` |
| E6 | Los ajustes de código previos a F2 (sesión F1-06: P2 en `Makefile` e `internal/candados`, marca de `Estado` de P4) y el cierre de F9-B (sesión F9-04) están en `dev` | `ESTADO.md` y los `[x]` con SHA de esas sesiones |

**Comprobadas en T2.1 (2026-10-04, `dev` @ `9a77307`)**: E1 ✔ (`internal/{arranque,apipublica,pendiente,candados}` existen;
`test-pendiente` y `cobertura-ficheros` en el `Makefile`) · E2 ✔ (`platform/httpapi` no importa `iam`: la única mención es un
comentario, `audit_mw.go:31`) · E3 ✔ (parada de F1 resuelta el 2026-10-03, `informe-piloto.md` §10) · E4 ✔ con matiz: `git diff
1b18932 origin/dev -- internal/{iam,entitlements,platformadmin}` solo trae los dos ✎ de F0 (`dd1e2bd`, +3 líneas en
`membresia_unica_ast_test.go`; `b65b788`, el alias de `AuditInput` en `ports/in/usecases.go`), previstos; `git log` lista además
`8096232` porque el clon es superficial y ese commit es su raíz, no porque cambie el código · E5 ✔ (go1.26.5, lint v2.12.2) ·
E6 ✔ (F1-06 y F9-04 en `ESTADO.md`). **D-F4-1** verificada: `grep -c SkipDir internal/iam/infra/postgres/membresia_unica_ast_test.go`
→ 1 y `dd1e2bd` en `origin/dev`. **D-F2-\***: 1, 3, 4, 5, 6 y 8 = sí (2026-09-30); 2 ⊂ D-F4-1 (sí); 7 ⊂ D-F9-1 (sí).

## Salidas (es cierto al cerrar)

- `internal/modulos/acceso/{entitlements,iam/**,platformadmin}` con **51 + 6 ✚** ficheros de producción en
  verde, cada uno con su `x_test.go`, y los paquetes `…test` de [`diseno.md`](diseno.md) §2.
- `grep -rn 'pendiente.Implementar' internal/modulos/acceso | wc -l` → **0**; SKIP en `acceso` → **0**.
- Sin umbral de cobertura (P2): un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9.
  `make cobertura-ficheros` es un **informe**: la tabla va al PR; no bloquea.
- Las suites de los puertos con BD (7 de `outhelpertest`, `ContratoResolver`, `platformadminhelpertest`) verdes
  **en memoria y en Postgres** con el arnés, sin divergencias.
- `cmd/server-modular`: una sola instancia de `acceso/entitlements.Postgres`, las 23 rutas públicas
  servidas por `apipublica`, las 8 de plataforma con los handlers nuevos; `huella_test` igual;
  `cmd/server` sin un byte cambiado.
- `internal/arranque/bridge_iam.go` en verde, con su test de cableado completo (nace en F2, muere en F3).
  `acceso` **no** entra en `Conmutados` al cerrar F2: entra cuando muere ese adaptador (F3).
- Traspaso web → local solo mientras existan los dos entornos, y solo si algo lo cierra la local.

## Orden de lectura

1. [`requisitos.md`](requisitos.md) — historias y criterios EARS.
2. [`arquitectura.md`](arquitectura.md) — paquetes, imports, estado, adaptador de arranque, cableado, rutas.
3. [`diseno.md`](diseno.md) — contratos por paquete, niveles E-12 provisionales, suites, dobles, reglas E-8, candados.
4. [`reglas.md`](reglas.md) — trampas con `fichero:línea` y definición de hecho.
5. [`tareas.md`](tareas.md) — tareas y sesiones.

## Bloques de sesión

Un bloque = una sesión de 45–90 min (duración **sin medir** para F2). Cada una cierra con tres cosas: tareas `[x]`
con SHA, un bloque en `ESTADO.md` y los hallazgos nuevos en este README.

| Sesión | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| **F2-01** · inventario E-12 + hojas simples (`entitlements` sin `postgres.go`, `iam/domain`, `ports`, suites, dobles `infra/memory`) | 🌐 | T2.1, T2.34, T2.2–T2.3, T2.5–T2.9, T2.17–T2.21 | **Jhoan aprueba el inventario** (antes no hay código) · esos paquetes a 0 pendientes · las 7 suites verdes en memoria · `ci-local` rc=0, 0 SKIP · PR |
| **F2-02** · `usecase` e `identity` (rojo y verde por paquete) | 🌐 | T2.10–T2.11, T2.16, T2.22–T2.23 | 0 pendientes en los dos paquetes · `ci-local` rc=0, 0 SKIP · PR |
| **F2-03** · `infra/postgres`, `entitlements/postgres.go`, `transport/http`, `platformadmin` | 🌐 | T2.4, T2.12–T2.15, T2.24–T2.27 | 0 pendientes en `acceso` · candados AST verdes · `vet -tags integracion` rc=0 · PR |
| **F2-04** · `bridge_iam.go`, conmutación y rutas | 🌐 | T2.28–T2.31 | huella igual · 23+8 rutas nuevas · `go list -deps` · test de cableado completo · PR |
| **F2-05** · cierre local | 💻 | T2.32–T2.33 | suites contra Postgres sin divergencias · procesos de acceso contra los dos binarios, 0 SKIP · PR a `dev` desde la rama de la sesión (regla 6) |

## Contradicciones encontradas (con `04`/`05`/FX, medidas contra el código)

1. **`05` §3.2 manda los tres candados AST del canje a «Proceso de acceso: necesitan BD».** No
   necesitan BD (leen fuente con `go/parser`) y **no se pueden** expresar como proceso: sus cabeceras
   dicen que lo que vigilan es **inobservable** desde fuera (`canje_orden_ast_test.go:6-26`: el
   rollback iguala los dos órdenes; `canje_una_consulta_ast_test.go:6-19`: la simetría de latencia se
   mediría con relojes; `membresia_unica_ast_test.go:11-15`). Propuesta: se quedan como **candados
   AST** del paquete nuevo (excepción de `05` E-7) y el proceso de F9 cubre la atomicidad (D-F2-1).
2. **`05` E-1 choca con `internal/iam/infra/postgres/membresia_unica_ast_test.go:74,97,128`**: barre
   **todo** `internal/` y exige que el único fichero con `INSERT INTO public.tenant_members` sea
   `iam/infra/postgres/memberships.go`. El verde de `acceso/iam/infra/postgres/memberships.go` lo pone
   **rojo** en `ci-local`. **Resuelto en F0** por D-F4-1 (T0.27: el barrido viejo salta el árbol
   nuevo), que subsume D-F2-2; F2 solo lo **verifica** (T2.1 y T2.24).
3. **`05` E-6 lista `entitlements` entre los 12 sin gemelo en memoria**: lo tiene, `Fake`
   (`internal/entitlements/entitlements.go:211-296`), en un fichero de producción. `platformadmin`
   sí carece de gemelo **y de puerto**: sus handlers reciben `*Repository` concreto
   (`handlers.go:47,111,138,183,227`, `access_requests.go:548,572,694`, `signup.go:122`) (D-F2-3).
4. **`04` §3 da a `iam/infra/memory` 7 ficheros**: falta el gemelo de `out.InvitationRedeemRepo`
   (`ports/out/canje.go:31`), el único de los **7** puertos de salida persistentes sin doble
   (medido: `grep -n 'var _ out\.' internal/iam/infra/memory/*.go` → 6 puertos; los otros 3 de
   los 10 son clientes HTTP de identity). Se añade ✚ `infra/memory/redeem_store.go`.
5. **`05` E-3 da suite de contrato a los ficheros «solo de interfaces»**: `ports/out/*.go` (3) y
   `ports/in/{active_tenant,canje}.go` lo son; `ports/in/usecases.go` **no** (25 exportados: DTOs,
   `CallerResolverFunc` con método). Ver D-F2-5.
6. **`02` §4 ciclo 1** (`acceso · operador · edge · plataforma · nucleo`): verificado que **se
   deshace** con la fusión (`acceso→operador` y `operador→acceso` quedan internos) más los ✎ de F0.
   Tras F2, `acceso` importa solo `platform` y a sí mismo (arquitectura §3): cero puentes.
7. **`constitucion.md` I-CP-5** sitúa el candado en `internal/bootstrap/`; vive en
   `internal/bootstrap/arranque/platform_permissions_test.go` (ya anotado en `03` §2.2).
8. **FX `mapa-de-rutas.md` §4.4 / `arquitectura.md` §5** proponen un puente de identidad para
   `session.ErrSessionOffline`; **no es de F2**, pero el mismo problema (centinelas por identidad) sí
   aparece aquí: el `gatewaygrpc` viejo compara `domain.ErrInvalidCredentials` & co. **viejos**
   (`internal/gateway/grpc/auth.go:200-206`). Lo resuelve el adaptador de arranque (arquitectura §4).
   ✎ 2026-10-03: ese adaptador se llama `bridge_iam.go` (P5, `05` §4.2).

**De la sesión F2-01 (2026-10-04)** — medido contra el código; lo que pide decisión está marcado 🟡:

9. **T2.34 nombraba mal el fichero del candado de invitaciones.** Mandaba que `invitaciones_cableado_test.go:33` parseara
   `auth_invitaciones.go`, pero lo que busca, `iamusecase.NewInvitationService` (`auth.go:742`), vive en `buildRolePlane`,
   que va a `auth_roleplane.go`. Jhoan (P3 del inventario): parsea `auth_roleplane.go` (`f46a107`).
10. **D-F2-5 chocaba con el candado `un_fichero_un_test`.** Ese candado solo exime un fichero de solo interfaces si existe
    `<dir>/<paquete>helpertest` con una función `Contrato(t *testing.T, …)`; D-F2-5 dice que `ports/in` no lleva suite, y
    `ports/in/{active_tenant,canje}.go` mordían. Jhoan (2026-10-04): excepción **en el candado**, verificada y por lista
    cerrada, `internal/candados/inbound_ports.go` (`InboundPortDirsWithoutSuite`); añadir un directorio exige decisión.
11. **El candado pide una función llamada exactamente `Contrato`.** Las suites `ContratoX` de diseño §2 no eximen el puerto
    por sí solas: `outhelpertest` añade `Contrato(t, Montajes)`, que corre las siete (y sirve a F9 para correrlas de una vez).
    Lo mismo valdrá para `platformadminhelpertest` (F2-03).
12. **E-11 mal aplicado en el primer verde de `iam/domain`.** Conservó siete símbolos con el nombre español del viejo
    (`EvaluarCanje`, `ResultadoCanje`, `Canje*`, `RolTransversalID`) invocando «lo ya decidido»; esa excepción cubre lo ya
    escrito en el árbol nuevo, no los nombres que la spec cita del viejo. Corregido en `2776d82`; correspondencias en
    `tareas.md`. Conviene que los prompts de sesión lo digan con un ejemplo.
13. ✅ ~~🟡~~ **El valor cero de `RedemptionVerdict` era `RedemptionProceeds`** (`iota`, como el viejo `CanjeProcede`): un veredicto
    sin inicializar dejaría pasar un canje. Se mantuvo el orden del viejo (equivalencia) y el test lo fijaba. **Resuelto
    (D-F2-10, Jhoan, 2026-10-04, tras el cierre de F2): se invierte ya**, sin esperar a que muera el viejo: el cero es
    `RedemptionMissing` y `TestRedemptionVerdict_ZeroValueRejects` lo fija. El número no sale del proceso (ni HTTP ni base),
    así que nada observable cambia.
14. ✅ ~~🟡~~ **`HashInvitationToken` no recortaba U+200B ni U+FEFF.** **Resuelto (D-F2-11, Jhoan, 2026-10-04, tras el cierre de
    F2): se recortan de los bordes**, y solo de los bordes (por dentro siguen dando otro digest; sin NFKC). Aquí el nuevo
    **se aparta del viejo**: un token pegado con uno de esos invisibles, que el viejo rechaza, el nuevo lo canjea; el corpus
    marca esas entradas (`divergesFromOld`) y ningún proceso de F9 ejerce el caso. Texto original del hallazgo: `strings.TrimSpace` quita U+00A0, U+2003, U+202F, U+3000,
    U+0085 y los ASCII, pero no el espacio de ancho cero ni el BOM: un token pegado con uno de ellos no se canjea. Se
    mantiene la conducta del viejo y el corpus adversario (22 entradas) la fija; cambiarla (NFKC, quitar invisibles) es
    decisión.
15. **El `Fake` de `entitlements` y Postgres no responden igual a un tenant inexistente** (`("basic", [])` frente a
    `("", nil, nil)`), igual que en el viejo. Contra el `Fake`, `ContratoResolver` prueba la mecánica del doble; las reglas de
    resolución (override en los dos sentidos, plan NULL ⇒ `basic`, tenant inexistente) solo las prueba de verdad la misma
    suite contra Postgres (F2-03/F9). El `Seed` de Postgres tendrá que sembrar antes de la primera consulta (caché), crear
    los planes `contract_*` y añadir claves a `basic` sin borrar las de las migraciones; el caso de orden usa
    `contract_a_b`/`contract_ab` para cazar un `ORDER BY` con *collation*.
16. **Reglas de los dobles viejos que no se mantienen** (los dobles nuevos se alinean con Postgres, que es lo que promete el
    puerto): desempate de listados por ordinal → `created_at DESC, id DESC` en invitaciones y `(created_at, tenant_id|user_id)`
    en membresías; `InvitationStore.Create` rechaza un digest de ≠ 32 bytes (el viejo lo aceptaba). La suite de invitaciones
    necesitará del montaje Postgres `Seed` y `DeleteRole` por SQL; la de roles da por sembrado el rol transversal (migración
    0059).
17. **Los *worktrees* de sub-agente nacen en `2da10b4`**, un ancestro viejo sin `internal/modulos`: cada sub-agente tuvo que
    hacer `git reset --hard` a la rama antes de empezar. Conviene decirlo en el prompt.
18. 🟡 **`05` E-3 no recoge la excepción de los puertos de entrada** (hallazgo 10): su tabla de excepciones verificadas y su
    «ninguna más sin decisión escrita» siguen sin ella; la decisión escrita está en `DECISIONES.md` (D-F2-5, ✎ 2026-10-04) y
    en el comentario de `UnFicheroUnTest`. La norma solo la toca Jhoan.

19. **`grants.go` no tiene exportados** (`grantsToAuth`, `resolveEffectiveGrants`): nivel medio, pero no hay contrato que
    poner en rojo (T-14: un no exportado sin uso rompe `unused`; E-4: los auxiliares nacen en el verde). Nació entero en su
    verde (`d09ff88`) con el test de sus reglas (R-U32). Un fichero sin exportados no tiene rojo: convendría decirlo en `05` E-12.
20. **Los contratos de un paquete se citan entre sí y fuerzan el orden**: `exchange` usa `Config` y `delegated_auth` usa
    `TokenValidator`, así que los simples (`config`, `context_token`, `audit`) nacieron completos **antes** del rojo de los
    medios, no en el orden de T2.23. Sus auxiliares compartidos (`withDefaults`, `verifyWithValidator`, `tokenTypeBearer`)
    sobreviven a `unused` porque `.golangci.yml` tiene `tests: true` y el test del simple los ejerce; los *helpers* de test
    comunes viven en el `_test.go` del primer fichero que los usa en verde (no hay `helpers_test.go`).
21. **El candado `exportados_cubiertos` del árbol real vive en `./internal/modulos/`**, no en `./internal/candados/...`: un
    sub-agente que solo corría este último commiteó un rojo sin mencionar `ContextTokenService` (corregido antes de integrar).
    El gate de los sub-agentes debe incluir `go test ./internal/modulos/`.
22. **El clon de la web es superficial** (`git rev-parse --is-shallow-repository` → `true`): `git log -1 --format=%h -- <fichero
    viejo>` devuelve el límite del injerto (`048412a`), no el último cambio real. Las cabeceras `// Porta … @` de `acceso` usan
    `9a77307` (sin diff en `internal/iam/` hasta `origin/dev`); corregido en `86912f7`.
23. **`make test-pendiente` cuenta también los *worktrees* de sub-agentes** (`grep -rn … .` incluye `.claude/worktrees/`): con un
    *worktree* vivo dio `PENDIENTES=29 · ROJOS=7` sobre una rama con 0. Hay que retirar los *worktrees* antes de leer la cifra
    (o excluir `.claude` en el target).
24. **Un mutante equivalente en el M2M**: cambiar `>` por `>=` en `usableLifetime` no se distingue con 60 s (`60−30 = 60/2`). El
    `select` de «ctx cancelado» elige al azar entre dos casos listos: el test que lo mata repite la llamada 64 veces.
25. **Dos reglas que el rojo no afirmaba y el verde destapó**: `DelegatedAuthService.Refresh` propagando el rechazo de identity
    (test añadido en `7295586`) y, en canje, un falso positivo del test (`bytes.Contains(digest, "x")` con un token de un
    carácter). `TenantsOfCaller` normaliza a lista vacía un `nil` del repositorio (R-U17 lo promete en el servicio; ningún
    adaptador devuelve `nil` hoy).

**De la sesión F2-03 (2026-10-04)** — medido contra el código; lo que pide decisión está marcado 🟡:

26. **El candado `ProcessImports` no admitía la zona hexagonal.** Su regla 3b solo deja a un `*_contrato_test.go` importar
    el **padre** del `…helpertest`; en `iam` la suite cuelga de `ports/out` y los adaptadores viven en `infra/postgres`.
    Jhoan eligió una **lista cerrada** de pares en el candado: D-F2-9, `internal/candados/contract_adapters.go`
    (`7da5367` → `a9a5fdf`). Y para los tipos del dominio que cruzan los montajes (`InvitationTables.Seed`, `RedeemState`),
    alias en `outhelpertest` (`Invitation`, `Membership`, `f812cfb`) en lugar del rodeo con genéricos restringidos por la
    forma del struct que el sub-agente había escrito (cumplía la letra del candado, no su espíritu).
27. **Las pasadas contra Postgres no caben dentro del paquete.** T2.4/T2.12/T2.15 pedían `postgres_integracion_test.go` /
    `suites_integracion_test.go` en el paquete, pero el arnés de F9-A (`nuevaBase`, `Abrir`) no se exporta y
    `test/procesos/base_test.go` es el único fichero que puede abrir conexiones (`SinBDViva`). Viven en
    `test/procesos/{entitlements,iam,platformadmin}_contrato_test.go`, la convención de H9.5 que estrenó F1 (T1.13).
28. **Pre-chequeo en la web (Docker respondió, no cierra nada; cierra F2-05)**: las cuatro suites (`contact`, `entitlements`,
    las 7 de `iam`, `platformadmin`) contra testcontainers con `WAPP_PROCESOS_BINARIO=viejo`: rc=0, **148 PASS, 0 FAIL,
    0 SKIP**. **Sin divergencias** memoria ↔ Postgres en ninguna: los hallazgos 15 y 16 se resolvieron en los montajes
    (`Seed` de `entitlements` antes de la primera consulta, planes `contract_*`, `basic` ampliado sin borrar; invitaciones
    y canje sembrados por SQL; el rol transversal de la migración 0059).
29. 🟡 **Cuatro mutantes de `canje.go` sobreviven incluso contra Postgres**: el `UPDATE` sin `revoked_at IS NULL`, sin
    `redeemed_at IS NULL`, sin comprobar filas afectadas, y `GrantTenantAccess` sobre `r.db` en vez de la `tx`. Los tres
    primeros solo los destapa una **carrera** (el viejo ya lo había medido: el veredicto previo los enmascara); el cuarto,
    porque el comentario viejo dice que `canje_orden_ast_test.go` vigila que el canje pase la `tx`, y **solo vigila el
    orden**. Además, `Add` sin su transacción en `memberships.go` solo muere 1 de cada 6 corridas (el caso de altas
    concurrentes es probabilístico). ¿Se añade al candado AST la comprobación «`GrantTenantAccess` recibe la `tx`», y un
    proceso de carrera en F9 (R-P8)?
30. **Probar la transacción sin BD funciona** con un *driver* `database/sql` en memoria que registra BEGIN/COMMIT/ROLLBACK
    y marca lo que sale por el pool con la `tx` abierta: mató los 16 mutantes unitarios de `access_requests_postgres.go`
    (incluida la atomicidad de R-A7). El mismo patrón sirvió al unitario de la caché de `entitlements/postgres.go`, que
    por eso **no** porta `lookupFn`/`listFn` (los campos no exportados no caben en el rojo, T-1). Podría servir a
    `iam/infra/postgres` para sus mutantes de Tx.
31. **Los mutantes contra Postgres destaparon dos huecos de alcance por empresa** en la suite de `platformadmin` (el rol del
    reintento leído sin la empresa; el lease unido solo por `edge_id`), que el doble ya hacía bien porque sus claves son por
    empresa: `7a32260` añade los casos (29). La suite gana cuando se mutan los dos lados.
32. **El orden lo fuerzan los tipos, también al revés del hallazgo 20**: `ports.go` (simple, solo interfaces) necesita los
    DTO y centinelas de `postgres.go` y `access_requests.go` (medios), así que sus rojos van **antes** del simple, y el rojo
    de `access_requests.go` se partió en dos commits (tipos y centinelas; lógica y handlers). Y `http.go` de
    `transport/http` no pudo nacer entero: `toVerifyResultDTO` escribe en un DTO de `auth.go`; vive en `auth.go`.
33. **Un medio ya verde no admite un rojo nuevo**: `NewRepository` (que esperaba al `FeatureResolver` de `iam/infra/postgres`)
    llegó directo en verde (`a7e72b4`), porque un segundo `_test.go` tras `pendiente` lo rechaza `un_fichero_un_test`.
    Convendría decirlo en `05` E-12.
34. **`platformadmin` declara 10 centinelas, no 9** (diseño §3/§5): `ErrSignupNotAvailable` (`signup.go`), que nada devuelve.
    Y 🟡 `net/mail` acepta un correo con nombre visible (`Ana <ana@x.com>`, se guarda `ana <ana@x.com>`) y no recorta
    U+200B/U+FEFF (como el hallazgo 14): se mantiene por equivalencia y el corpus adversario (26 entradas) lo fija.
35. **Lint en el verde de los tests**: con la etiqueta `pendiente` el rojo no se lintea, y el verde destapó staticcheck ST1023,
    gosec G101 (literales en campos `Token`) y gocyclo en tablas de casos; se resolvió solo en los tests, sin tocar promesas.
    `invitations.go` (HTTP) y el handler del código de enrolamiento siguen con `time.Now()` como el viejo (sin reloj
    inyectable sin decisión).
36. **Ficheros de más de 500 líneas (Jhoan, tras abrir el PR #31: «no es aceptable»).** Los 6 de F2-03 (el mayor,
    `platformadminhelpertest/contrato.go`, 887; el que nombró, `entitlements/postgres_test.go`, 822) y, a continuación, los
    de F2-02 que pasaban de 600 (`identity/m2m.go` 802 y `m2m_test.go` 1.141, `f26af82`; `usecase/exchange_test.go` 761,
    `4f6e178`) se partieron por tema **solo moviendo declaraciones**, con el sufijo de su origen. `exchange.go` (500) cabe.
    Jhoan lo hizo regla: **D-R-7**, `05` **E-13** (objetivo 500, tolerancia 600, estricto por encima) y el candado
    `internal/modulos/file_size_test.go` (`181458d` → `bec5116`). Tres reglas que el corte respeta: un trozo de producción
    lleva su gemelo de test (E-3) con sus exportados (E-9); el `x_test.go` que conserva el nombre sigue nombrando todos los
    exportados de `x.go` (lo destapó el corte: `M2MOption` e `IdentityTokenVerifier` solo los nombraba un helper); los
    trozos de suite se llaman `<tema>_contrato.go` (D-F1-13). 🟡 **Quedan 5 del árbol nuevo de más de 600**, en la lista
    cerrada `candados.OversizedFiles` con su techo (no crecen): `arranque/bridge_contact_test.go` (809),
    `arranque/huellatest/huellatest.go` (732) y su test (669), `candados/sinbdviva_openers_test.go` (629) y
    `nucleo/contact/repository_postgres.go` (618). ¿Se parten en una sesión dedicada o cuando su fase los toque?

37. **R2.5.d no podía verificarse con su comando** (`go list -deps … | grep` → «solo `iam/{domain,ports/in}`»): `publicapi`
    (sirve aún 72 rutas) importa el `internal/entitlements` viejo y `internal/iam/transport/http`, y `flujos/{events,runtime}`
    y `reanalisis` el `internal/entitlements` viejo. Tras conmutar, el binario nuevo enlaza de lo viejo **solo**
    `internal/entitlements`, `iam/domain`, `iam/ports/in` e `iam/transport/http` (ningún usecase, infra ni `platformadmin`
    viejo), y sus importadores son esos paquetes viejos sin reconstruir más `internal/arranque`, donde solo `bridge_iam.go`.
    **Decisión de Jhoan (F2-04)**: se verifica por la cláusula del requisito; comando corregido en
    [`requisitos.md`](requisitos.md) R2.5.d.
38. **`bridge_iam.go` adapta más de lo que dice arquitectura §4**: también `in.VerifyResult` (el `Authenticator` viejo
    embebe `TokenVerifier`), y el auditor **también** traduce centinelas (el `AuditService.Record` nuevo devuelve el
    `ErrInvalidInput` nuevo). Y la traducción no es `fmt.Errorf("%w", viejo.ErrX)` como dice §4: eso cambiaría el `Error()`
    y perdería el prefijo; se usa el `bridgeError` de `bridge_contact.go` (texto byte a byte, `Unwrap() []error{orig, viejo}`).
    La regla del **nil de verdad** se conserva: sin identity, el gateway recibe una interfaz nil, no un adaptador vacío.
39. **El rojo de un paquete medio dejó los tests acoplados entre ficheros** (`audit_test.go` usa ayudantes de
    `chain_test.go`, éste dobles de `roleplane_test.go`, `auth_test.go`…): con `//go:build pendiente` por fichero, ningún
    test se puede destapar solo. El verde fue un commit por fichero, comprobado con `-tags pendiente -run …`, y las cinco
    etiquetas se quitaron en el último (`4f28f4f`). Y `chain.go` (solo `Common` exportado) no pudo nacer solo: sus ayudantes
    (`protect`, `protectRead`…) no tienen consumidor hasta una ruta W, y `unused` no cuenta los tests etiquetados; nacieron
    con `roleplane.go` (`52a5d09`). Lección para el rojo de un paquete medio: los ayudantes comunes de test, en un
    `<paquete>_helpers_test.go` sin dobles de otras áreas.
40. **Elecciones del contrato de `apipublica` que el mapa no dice** (aceptadas al revisar el rojo): `panic` de cableado **al
    montar** (MW nil si va a registrar algo, `Verifier` nil, `M2M` sin `SignupRequests`); A5 y A6 se montan **juntas**; el
    limitador del alta (`rate.Every(time.Minute), 5`) nace dentro de `MountAuth`, y el `Warn` de «signup sin M2M» se emite
    allí (no se duplica en `http.go`); `parseIntQuery` vive en `response.go` (lo usarán F4, F6, F8); la rama muerta de C2
    «resolver nil ⇒ 500» no se porta (la ruta solo se monta con resolver; ya era inalcanzable).
41. **C2 queda registrada en las dos caras**: el `publicapi` viejo la monta porque `Deps.Entitlements` (el resolver nuevo)
    no es nil, y la cara nueva la tapa. Ni la huella ni el candado de mudanzas lo rechazan; muere con `publicapi`. Los
    candados viejos reapuntados (`roleplane_cableado_test.go`, `invitaciones_cableado_test.go`) conservan sus nombres
    `TestCableado_*` (código ya escrito, E-11) y ahora miran `apipublica/roleplane.go` y los `RolePlaneDeps`;
    `http.go` guarda la cara real en `c.publicCara` para que `TestMudanzas_HuellaPorElCompuesto` mire la del arranque.
42. **La caché compartida de `golangci-lint` entre *worktrees* da falsos positivos** (3 gosec bajo la ruta de **otro**
    *worktree*). Con `GOLANGCI_LINT_CACHE` propio por *worktree*, 0 issues. Va al prompt de los sub-agentes.

**De la sesión F2-05 (2026-10-04, 💻, cierre local)** — medido contra lo que corre; lo que pide decisión está marcado 🟡:

43. **El «e2e de `cmd/server-modular` (`integration_test.go` de F0)» que pedía T2.32 no existe**: F0 `diseno.md` §5.3 decidió
    no copiar `cmd/server/integration_test.go` (compone paquetes en proceso y no ejerce el arranque nuevo); `cmd/server-modular`
    solo tiene `main.go`. **Decisión de Jhoan (F2-05)**: la cláusula se cumple con `BINARIO=nuevo make test-procesos` más el
    arranque real 9/9 con `/healthz` 200; texto de T2.32 corregido en [`tareas.md`](tareas.md).
44. **T2.33 decía que el proceso P2 «incluye» R-P1…R-P8, R-A5…R-A7, I-CP-5 y la 0038; por caja negra no las incluía todas.**
    **Decisión de Jhoan (F2-05)**: matriz regla → prueba contra Postgres, y al proceso solo lo que no pide decisión.

    | Regla | Quién la prueba contra Postgres |
    |---|---|
    | R-P1 (membresía y rol en una llamada; la guarda antes del rol; sin rol global) | suite `InvitationRedeemRepo/HappyPath_WithRole_AssignsItScopedToTheTenant`, `…/MemberOfAnotherCompany_Conflict_InvitationNotBurned`; `RoleRepo/AssignToUser_CompanyRoleWithGlobalScope_ErrRoleScopeInvalid`; el orden guarda → rol y el rol global, **solo unitarios** (`TestGrantTenantAccess_*`) |
    | R-P2 (cerrojo → guarda → `INSERT`) | candado AST `TestSingleMembershipWriter_LockThenGuardThenInsert` y unitarios; por conducta, proceso `TestP2_InvitacionUnSoloCanje/ocho canjes simultáneos`; la suite (`Add_ConcurrentInTwoCompanies_OnlyOneWrites`) **no** lo distingue (hallazgo 45) |
    | R-P3 (409 idéntico sin `multi_empresa`) | suite `MembershipRepo/Add_SecondCompanyWithoutMultiCompany_SameConflictAndNothingWritten`; proceso `…/quien ya es de otra empresa no quema la invitación` |
    | R-P4 (`MembersOf` solo ese tenant) | suite `MembershipRepo/MembersOf_OnlyThatTenant_InOrder` |
    | R-P5 (una transacción; acceso antes de marcar) | candado AST `TestRedeem_GrantsAccessBeforeMarkingInvitation`; proceso «ocho canjes simultáneos» (mata `GrantTenantAccess` fuera de la `tx`); unitarios con *driver* falso (hallazgo 46) |
    | R-P6 (una consulta, `now()` de la base) | candado AST `TestRedeem_ReadInvitationMakesOneQuery`; proceso `…/inexistente y caducada contestan lo mismo`; el reloj de la base, unitario con *driver* falso (hallazgo 46) |
    | R-P7 (las cuatro NULLables) | unitario `TestInvitationFromRow`; suite `InvitationRedeemRepo/DeletedRole_InvitationStaysAliveWithoutRole` |
    | R-P8 (`redeemed_at IS NULL AND revoked_at IS NULL`) | `redeemed_at`: proceso «ocho canjes simultáneos». 🟡 `revoked_at` **por carrera: nadie** (hallazgo 45) |
    | R-A5…R-A7 (reintento, otro rol, atomicidad) | suite `TestPlatformadminContrato_Postgres` (`ExecuteApprovalTx_*`, `CheckRetryApproved_*`) y unitarios con *driver* falso. **Sin caja negra**: el arnés no levanta el M2M de identity y la aprobación por HTTP contesta 503 |
    | I-CP-5 | proceso `TestP2_RutasDePlataformaDenegadasAlCliente` (10 rutas × 2) y la petición real de T2.32 (403) |
    | Migración 0038 | **nuevo**: `TestP2_ExchangeAndPermissions/el IAM propio no sobrevive a la 0038` (`8677404`, `test/procesos/p2_canje_schema_test.go`) |

45. 🟡 **Hallazgo 29, medido otra vez (33 mutantes sobre `memberships.go` y `canje.go`)**. De los cuatro de carrera de `canje.go`,
    **tres mueren ya, pero solo por el proceso P2** («ocho canjes simultáneos», 3 de 3 corridas cada uno): el `UPDATE` sin
    `redeemed_at IS NULL`, sin comprobar filas afectadas, y `GrantTenantAccess` sobre `r.db`. La suite de contrato contra
    Postgres no mata ninguno (0 de 4). **Siguen vivos dos**: el `UPDATE` sin `revoked_at IS NULL` (el caso «revocada» de P2
    revoca antes del canje y lo corta el paso 1) y `Add` sin su transacción en `memberships.go` (muere **0 de 6**, no 1 de 6:
    `Add_ConcurrentInTwoCompanies_OnlyOneWrites` lanza dos goroutines y no produce la carrera; tampoco ve quitar el cerrojo
    entero, que solo matan los unitarios y el candado AST). No se resolvió nada (decisión pendiente de Jhoan): lo que los
    mataría es una revocación intercalada entre el paso 1 y el 3 (o N canjes contra N revocaciones), y subir la concurrencia
    del caso de `Add` o exigir por AST que `Add` pase la `tx`.
46. **Cinco mutantes vivos nuevos del nivel complejo, arreglados con su commit; ninguno tocó producción**:
    `do` sin el prefijo `Bearer ` y `NewM2M` con `timeout < 0` (`40d1582`; el doble recortaba el prefijo, y el 0 exacto no
    tenía test) · `ListAccessRequests` sin `ORDER BY` (`d243f15`: el caso creaba las solicitudes en el orden en que esperaba
    leerlas; `State` gana `SetRequestCreatedAt` y el doble ordena de verdad) · `Redeem` sin `Rollback` en el `defer` y el `now`
    del proceso en vez del `now()` de la base (`7f2b745`, con un *driver* `database/sql` falso, el patrón del hallazgo 30;
    de paso, ese *driver* mata también por unitario `GrantTenantAccess` sobre `r.db`, uno de los cuatro del hallazgo 29:
    efecto lateral del camino feliz, no una decisión sobre el 29).
    Y **cuatro equivalentes nuevos**, con su razón: `rows.Close` ignorado en `entitlements/postgres.go` (`database/sql` cierra
    al agotar la iteración y entrega el fallo por `rows.Err`: la rama del `defer` no se alcanza; el caso `closeFails` de
    `8c53ecf` fija la conducta real) · el `AND tf.enabled` del `UNION` (la PK `(tenant_id, feature)` y el anti-join lo
    hacen redundante) · `storeToken` sin limpiar la caché negativa (solo se canjea con el fallo ya caducado) · el 503 de
    `mapExchangeError` (lo mapea igual el mapper de cada operación). `iam/infra/identity/client.go` repite el patrón
    `timeout <= 0` sin test del 0 exacto: no es del nivel complejo y no se mutó.
47. **Lo que solo ve Postgres, y lo que Postgres no ve.** En `entitlements/postgres.go`, los cinco mutantes de semántica SQL
    (override en los dos sentidos, anti-join, plan NULL, filtro por tenant) **solo** los mata la suite contra Postgres; el
    `fakeDB` unitario clasifica las consultas por literal y mata uno de ellos por dejar de reconocerla, no por la regla. En
    `platformadmin`, 11 de 31 solo mueren con Postgres y 3 solo con unitarios; quitar el `Rollback` de `ExecuteApprovalTx`
    hace que la suite contra Postgres **se cuelgue** (ningún aserto lo dice; lo cazan los unitarios), y el `UPDATE` de la
    aprobación por el pool en vez de la `tx` solo lo ven los unitarios. Sin `test/procesos`, esas reglas quedan sin red.
48. **El arranque real pide más que `.env`**: además del R2 de desarrollo, `WAPP_KEK_PROVIDER=env` con su material, y correr
    desde la raíz del repo (lee `certs/ca.crt` relativo). Se hizo contra un `postgres:17-alpine` efímero en puerto libre, con
    claves generadas en el momento. Y **la cara que sirve C2 no se distingue en ejecución** (hallazgo 41): el log dice
    `petición pública … /api/v1/entitlements 200` venga de donde venga; que es la nueva lo prueba `TestMudanzas_HuellaPorElCompuesto`.
49. **Lo que la web dio por cierto, repetido en local: idéntico.** `GATE_RC=0` con 113 `ok`, 148 PASS en las suites y 94 en
    `TestP2_*` + `TestP10_Platform` sobre `bfd31ce`; R2.5.d por su cláusula. Tras los commits de esta sesión las cifras suben
    por los casos nuevos, no por otra cosa. Las suites corren los **mismos 144 casos** en memoria y en Postgres.
50. 🟡 **`TestP5_OwnerInbox/sugerencia_con_plazo` es intermitente contra el binario viejo** (no es de `acceso`: es la bandeja
    y `quotetext`, F6). En una de las tres pasadas completas de `make test-procesos` de esta sesión dio rojo contra el viejo
    (`RC=1 · PASS=665 · FAIL=2`; el nuevo, `RC=0 · PASS=667`): `p5_bandeja_quote_test.go:134` encontró 0 líneas de log
    «quotetext: el proveedor no redactó la cotización; sale el texto determinista» y quería 1, con la respuesta HTTP
    correcta. Repetida: verde en la pasada completa siguiente y en tres corridas sueltas de `TestP5_OwnerInbox`. La sesión no
    tocó ni el código viejo ni ese proceso. Esa aserción lee el log **sin esperar** (`p9LogLines`; existe `p9WaitLogLines`):
    es la causa probable, **no medida**. No se arregló: ¿se cambia a la lectura con espera en F9-D o al reconstruir
    `quotetext` (F6)?
51. **Un `errcheck` propio que solo vio el gate entero**: `d243f15` dejó `n, _ := res.RowsAffected()` en el montaje de
    Postgres de `platformadmin` y `make ci-local` dio `GATE_RC=2` (1 issue); corregido en `73b4541`. El lint acotado a un
    paquete que corren los sub-agentes no cubre `test/procesos`: el que cierra es `make ci-local`.

### Informe de fase (plantilla de [`tareas.md`](tareas.md), al cerrar F2)

- **Minutos por sesión (D-R-6)**: F2-01 ≈ 81 (activos) · F2-02 ≈ 60 · F2-03 ≈ 90 · F2-04 ≈ 65 · F2-05 ≈ 47 (de pared) →
  ≈ 343 min para la fase. En F2-05 el cuello fueron los mutantes contra Postgres (tres sub-agentes: 12, 18 y 23 min).
- **Ficheros**: `internal/modulos/acceso` cierra con 56 de producción, 17 de suites y dobles (`…helpertest`) y 71 de test;
  `internal/apipublica` con 8 de producción más su arnés; `internal/arranque/bridge_iam.go`. **Subieron de nivel**: ninguno
  respecto del inventario aprobado; `apipublica` no estaba en él y entró como **medio** (F2-04).
- **Pendientes en cada cierre**: `PENDIENTES=0 · ROJOS=0` al cerrar F2-01 (sus paquetes), F2-02, F2-03, F2-04 y F2-05.
- **Cobertura por fichero (informe, no gate)**: 51 ficheros de `acceso` medidos, mínimo 10,7 % (`platformadmin/postgres.go`),
  media 90,6 %; 6 por debajo de 80 %, todos adaptadores Postgres (`iam/infra/postgres/{active_tenant,audit,invitations,
  memberships,roles}.go` y `platformadmin/postgres.go`), cuyo SQL prueban las suites contra Postgres, que el perfil no
  ve. `canje.go` salió de la lista (20 % → por encima de 80 %) con el *driver* falso de `7f2b745`: efecto, no objetivo. `apipublica` 87,5–100 %; `bridge_iam.go` 100 %.
- **Mutantes del nivel complejo (F2-05, a mano, contra unitarios y Postgres)**:

  | Pieza | Sembrados | Muertos | Vivos | Equivalentes |
  |---|---|---|---|---|
  | `entitlements/postgres.go` | 28 | 26 | 0 | 2 |
  | `iam/infra/identity/m2m*.go` | 38 | 35 | 0 | 3 |
  | `iam/infra/postgres/memberships.go` | 18 | 17 | 1 🟡 | 0 |
  | `iam/infra/postgres/canje.go` | 15 | 14 | 1 🟡 | 0 |
  | `platformadmin/access_requests_postgres.go` (+ 1 en `postgres.go`) | 32 | 32 | 0 | 0 |
  | **Total** | **131** | **124** | **2 🟡** | **5** |

  Cifras **después** de los arreglos de la sesión (hallazgo 46): antes sobrevivían 8 (5 arreglados, 1 resultó equivalente). Los dos que quedan son del hallazgo 45.
- **Candados que estorbaron**: `un_fichero_un_test` frente a D-F2-5 (hallazgo 10) y a un rojo nuevo en un medio ya verde
  (33); `ProcessImports` frente a la zona hexagonal (26, D-F2-9); `SinBDViva` (27: las pasadas contra Postgres no caben en el
  paquete); el tamaño (36, E-13). En F2-05, ninguno.
- **Reglas E-8 que no se mantienen**: las de los dobles viejos del hallazgo 16 (desempate por ordinal; digest de ≠ 32 bytes
  aceptado) y la rama muerta de C2 (hallazgo 40). El resto, por equivalencia, incluidas las 🟡 13, 14 y 34.
- **Lo que la web no pudo correr y cerró la local**: la pasada que cuenta de las suites contra Postgres, los mutantes contra
  Postgres, `make test-procesos` contra los dos binarios, el arranque real y las tres peticiones.
- **🟡 abiertas al cerrar** (no bloquean): 13, 14, 18, 29 (re-medida en 45), 34, 36 y 50 (intermitencia de P5, de F6).

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F2-1 | Los 3 candados AST del canje: ¿se quedan como candados AST del paquete nuevo (y no como proceso)? | **Sí**. Y el de «cuatro columnas NULLables» pasa a test **de conducta** sobre la función pura de mapeo extraída (E-6), que ya no necesita AST |
| ~~D-F2-2~~ | ~~¿Se permite tocar **una línea** de `internal/iam/infra/postgres/membresia_unica_ast_test.go` (añadir el escritor nuevo a `escritoresEsperados`) como excepción a E-1?~~ | **Subsumida por D-F4-1** ([`../DECISIONES.md`](../DECISIONES.md)): la línea se pone en **F0** (T0.27) y hace que el barrido viejo **ignore** el árbol nuevo. Solo si D-F4-1 = no vuelve esta forma: añadir el escritor nuevo a `escritoresEsperados` en el mismo commit que el verde de `memberships.go` |
| D-F2-3 | `platformadmin` sin puerto: ¿se crea `platformadmin/puertos.go` ✚ (solo interfaces) y se separa el SQL de `access_requests.go` en `access_requests_postgres.go` ✚? | **Sí**: es la única forma de que su lógica (aprobación, reintento, unión de systems) nazca cubierta sin BD (E-6). Cambia el árbol de `04` §3 en dos ficheros |
| D-F2-4 | El `Fake` de `entitlements` ¿se queda en `entitlements.go` o se muda a `entitlementshelpertest`? | **A `entitlementshelpertest`** (patrón E-6); `entitlements.go` queda con constantes y el puerto |
| D-F2-5 | ¿Suites de contrato también para los puertos **de entrada** (`ports/in`), que implementa un solo usecase? | **No**: excepción escrita — los cubre el test del usecase que los implementa. Suites **sí** para los 10 de salida |
| D-F2-6 | Reloj inyectable (`WithReloj(func() time.Time)`) en `entitlements.Postgres` y `iamidentity.M2MClient`, exportado nuevo sin equivalente viejo | **Sí**: `contrato-tdd` prohíbe reloj real; no cambia nada observable |
| D-F2-7 | Si F9 se adelanta (`plan/F9-procesos/`), ¿cierra F2 la pasada de los procesos de acceso contra el binario nuevo? | **Subsumida por D-F9-1** (recomendación: sí): T2.33 **es** la pasada 9C de `acceso` (T9.23); solo se tacha si D-F9-1 = no |
| D-F2-8 | *(2026-09-30, revisión del PR de F0-04)* ¿Se parte la copia `internal/arranque/auth.go` (835 l: auth stack, JWT, cadena de config al Edge, plano de roles, canje de invitaciones, empresa activa) antes de que la editen T2.31 y F3? | **Sí, T2.34, primera tarea de F2**: `auth_{stack,jwt,roleplane,invitaciones,empresa_activa}.go` y `edge_config.go` (la cadena al Edge no es auth). Solo mover; la huella prueba que nada cambia. **Decidido: sí (2026-09-30)** |
