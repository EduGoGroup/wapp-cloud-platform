# FX · Tareas — `TX.n`, repartidas por fase

> **Transversal**: estas tareas no forman sesiones propias. Cada fase **copia** a su `tareas.md` las
> suyas **citando el ID** (`TX.n`) y las mete en sus bloques; el estado (`[ ]`/`[~]`/`[x]` con SHA)
> se lleva **aquí**. Formato: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §3. Skills:
> `contrato-tdd` (cada fichero), `reconstruir-modulo` (el tramo), `validar-antes-de-cerrar` (gates).
>
> Gate de tests, siempre **sin pipe** y contando SKIP:
> `go test -count=1 -v ./internal/apipublica/... > "$TMPDIR/api.log" 2>&1; echo rc=$?; grep -c -- '--- SKIP' "$TMPDIR/api.log"` → `rc=0` y `0`.
>
> **Recalibrado el 2026-10-03** (`05` E-12, E-9, E-4): sin umbral de cobertura (P2). El nivel de cada fichero lo fija el
> **inventario E-12 de la fase que lo crea**; en nivel simple, el rojo y el verde de un fichero pueden ir en una sola
> pasada (las tareas `rojo`/`verde` de un tramo se cierran entonces juntas). El entorno (🌐/💻) que manda es el de la
> ficha de sesión. TX.1–TX.4 están cerradas y se dejan como se escribieron.

## Tramo F0 · el mecanismo · 🌐 · TX.1–TX.4

Sesión: [`F0-05`](../sesiones/F0-05-web-cara-vacia-platform-deriva.md) (cerrada).

Para cuando: el binario nuevo sirve las 73 rutas **por la cara vieja a través del compuesto**,
huella igual, `FaseActual = 0` y el candado de mudanzas en verde.

- [x] **TX.1 · rojo(apipublica): contratos de `Cara` y del estrangulador** · 🌐 · dep. `internal/pendiente` (F0) · cumple RX.1.a–d, RX.5.a — cerrada en `5a11f5b` (F0-05: `PENDIENTES=8`, `ROJOS=2`; `internal/apipublica` añadida a `PENDIENTE_DIRS`)
  - **Ficheros**: `internal/apipublica/apipublica.go`, `apipublica_test.go`, `estrangulador.go`, `estrangulador_test.go`
  - **Hecho cuando**: los dos ficheros tienen el contrato de [`diseno.md`](diseno.md) §2 con cuerpos `panic(pendiente.Implementar(…))`; los tests llevan `//go:build pendiente` y **fallan** corridos solos (`go test -tags pendiente -run '^TestComponer_GanaLaNueva$' ./internal/apipublica`)
  - **Gate**: `go vet -tags pendiente ./internal/apipublica/...; echo rc=$?` → `rc=0` · `make ci-local; echo rc=$?` → `rc=0`
  - **Commit**: `rojo(apipublica): contratos de la Cara y del estrangulador`
- [x] **TX.2 · verde(apipublica): `Cara` y estrangulador** · 🌐 · dep. TX.1 · cumple RX.1.a–d — cerrada en `de0c29b` (F0-05: un commit, 42 PASS, 0 SKIP, 100 % en los dos ficheros)
  - **Ficheros**: los cuatro de TX.1 (se quita la etiqueta)
  - **Hecho cuando**: los 8 tests de §2 pasan; `make cobertura-ficheros` ≥ 80 % en los dos; `pendiente.Implementar` en `internal/apipublica` → 0
  - **Gate**: el de cabecera · `make cobertura-ficheros; echo rc=$?` → `rc=0`
  - **Commit**: `verde(apipublica): la Cara y el estrangulador` (uno por fichero si se prefiere)
- [x] **TX.3 · andamiaje(f0): el arranque nuevo compone las dos caras** · 🌐 · dep. TX.2 y la copia del arranque de F0 · cumple RX.2.a, RX.2.c — cerrada en `9dcf7e8` (F0-05: aserción `cara_nueva_cableado_test.go`, que muerde; `buildPublicAPIServer` devuelve el compuesto y el contenedor lo guarda)
  - **Ficheros**: `internal/arranque/http.go` (la copia de `bootstrap/arranque/http.go`: F0 copia **con el mismo nombre**, T0.10 y T0.16, y el nombre se conserva: [`../00-marco/estructura.md`](../00-marco/estructura.md) §2.2), su test
  - **Hecho cuando**: el mux viejo recibe **lo mismo que hoy** (los 7 de autenticación y `publicapi.Register`); se construye `apipublica.Nueva()` vacía; el `http.Server` de `:8103` sirve `PublicRateLimit(InstrumentHTTP(…))` **sobre `apipublica.Componer(cara, viejo)`**, una sola vez cada uno; una aserción de cableado lo comprueba
  - **Gate**: `go test -count=1 -v ./internal/arranque/... > "$TMPDIR/arr.log" 2>&1; echo rc=$?` → `rc=0`, 0 SKIP
  - **Commit**: `andamiaje(f0): la cara nueva vacía delante de la vieja`
- [x] **TX.4 · andamiaje(f0): candado de mudanzas y la huella por el compuesto** · 🌐 · dep. TX.3 y la tarea de huella de F0 · cumple RX.2.a, RX.3.a–c — cerrada en `7b7e01f` (F0-05: 95 filas + cabecera; `FaseActual` en `mudanzas.go`; la huella —bloque gemelo intacto— sigue por `ServeHTTP` y `TestMudanzas_HuellaPorElCompuesto` resuelve las 73 por `Resolver` sobre el contenedor de huella)
  - **Ficheros**: `internal/arranque/testdata/mapa.tsv` (95 filas: id · listener · patrón · fase, **copiadas del [`mapa-de-rutas.md`](mapa-de-rutas.md)**), `internal/arranque/mudanzas_test.go`, la constante `FaseActual = 0` en `internal/arranque` (con su test en el fichero que la declare)
  - **Hecho cuando**: con `FaseActual = 0` la cara nueva no resuelve ninguna fila; los tres casos de [`diseno.md`](diseno.md) §6 (fase, solape, familia) tienen su test con un mapa de prueba que falla; la huella resuelve los 73 patrones de `:8103` por `Compuesto.Resolver`
  - **Gate**: `go test -count=1 -v -run 'Mudanzas|Huella' ./internal/arranque > "$TMPDIR/m.log" 2>&1; echo rc=$?` → `rc=0`, 0 SKIP · `wc -l < internal/arranque/testdata/mapa.tsv` → 95 (+ cabecera si la lleva)
  - **Commit**: `andamiaje(f0): candado de mudanzas de la cara HTTP`

## Tramo F2 · lo común y `acceso` · 🌐 · TX.5–TX.7

Sesión: [`F2-04`](../sesiones/F2-04-web-bridge-conmutar-y-rutas.md).

Para cuando: 23 rutas de `:8103` por la cara nueva, J4–J11 con handlers nuevos, `FaseActual = 2`.

- [x] **TX.5 · rojo(apipublica): lo común, el arnés y las cuatro áreas de acceso** · 🌐 · dep. TX.4 y el verde de `modulos/acceso` · cumple RX.2.b, RX.2.d, RX.2.e, RX.5.a–d, RX.6.a — cerrada en `799d821` (F2-04; nombres en inglés, ver `F2-acceso/tareas.md` §F2-04; `response.go` sin exportados no tuvo rojo: nace en su verde)
  - **Ficheros**: `cadena.go`, `respuesta.go`, `apipublicahelpertest/arnes.go`, `autenticacion.go`, `roleplane.go`, `audit.go`, `entitlements.go` y sus 7 `_test.go` (16)
  - **Hecho cuando**: antes de escribir, leídos los tests viejos de F2 ([`diseno.md`](diseno.md) §5); cada `Montar*` declara la condición de montaje del mapa §2.1–2.3; los puertos usan tipos de `modulos/acceso` **nuevos**; cada test cubre 401/403/feature/feliz/auditoría/404-sin-dependencia de sus filas
  - **Gate**: `go vet -tags pendiente ./internal/apipublica/...; echo rc=$?` → `rc=0` · `make test-pendiente` cuenta los pendientes nuevos
  - **Commit**: `rojo(apipublica): comunes y acceso`
- [x] **TX.6 · verde(apipublica): comunes y acceso, fichero a fichero** · 🌐 · dep. TX.5 · cumple RX.2.b, RX.2.e, RX.5.a — cerrada en `3745079`, `52a5d09`, `e546523`, `8a11c8d`, `4f28f4f` (F2-04; las etiquetas de los cinco tests se quitan en el último: el rojo los dejó acoplados, hallazgo 39 de F2)
  - **Ficheros**: los 8 de producción de TX.5, con su cabecera `// Porta …`
  - **Hecho cuando**: pendientes de `internal/apipublica` = 0; un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9
  - **Gate**: el de cabecera · `make cobertura-ficheros` como informe (la tabla va al PR; no bloquea)
  - **Commit**: `verde(apipublica): <fichero>` — uno por fichero
- [x] **TX.7 · conmutar(acceso): 23 rutas a la cara nueva** · 🌐 · dep. TX.6 · cumple RX.3.a, RX.4.a — cerrada en `8482dad` (F2-04; `FaseActual = 2`)
  - **Ficheros**: `internal/arranque/http.go`, `rutas_admin.go`, la constante `FaseActual`
  - **Hecho cuando**: A1–A7 ya no se registran en el mux viejo; `Montar{Autenticacion,RolePlane,Auditoria,Derechos}` montados; en los `Deps` viejos `Roles`, `Members`, `Invitations`, `Audit` = `nil` y `Entitlements` = **el resolver nuevo** (una sola caché, [`arquitectura.md`](arquitectura.md) §4); J4–J11 con `platformadmin` **nuevo**, construidos inline (T-16); `FaseActual = 2`
  - **Gate**: `go test -count=1 -v -run 'Mudanzas|Huella|PlatformPermissions' ./internal/arranque > "$TMPDIR/m.log" 2>&1; echo rc=$?` → `rc=0`, 0 SKIP · `make ci-local; echo rc=$?` → `rc=0`
  - **Commit**: parte del `conmutar(acceso): …` de F2

## Tramo F3 · `edge` · 🌐 (TX.10 🌐→💻) · TX.8–TX.11

Sesión: [`F3-04`](../sesiones/F3-04-web-bridge-conmutar-y-rutas.md) (TX.8–TX.11); el e2e local de TX.10 lo confirma [`F3-05`](../sesiones/F3-05-cli-cierre-mtls.md).

Para cuando: 6 rutas más (29), J12–J17 nuevos, un solo gw (también el de E1–E2, que siguen en la
cara vieja hasta F7: D-FX-1/D-F7-4), `FaseActual = 3`.

- [x] **TX.8 · rojo(apipublica): edge** · 🌐 · dep. TX.7 y el verde de `modulos/edge` · cumple RX.2.b, RX.2.d, RX.5.a, RX.6.a — cerrada en `0e5b69a`, `be818c9`, `488539b` (F3-04; un rojo por grupo de ficheros; `plazos.go` nace como `deadlines.go` y `limits.go`, sin exportados, nace en su verde; ver `F3-edge/tareas.md` T3.26)
  - **Ficheros**: `plazos.go`, `limits.go`, `messages.go`, `sessions.go`, `health.go`, `sessionadmin.go`, `diagnostics.go` y sus 7 tests (14)
  - **Hecho cuando**: `sessionadmin.go` **exporta** los dos constructores (D-FX-2) para `:8100`; `messages.go` mapea los centinelas del `edge/session` **nuevo**; `fronteras_test.go` sin ningún puente desde `apipublica`. *(Alternativa D-FX-1 literal: se añade aquí `intents.go` sobre `internal/intentcfg` viejo con su puente «retira: TX.21».)*
  - **Gate**: `go vet -tags pendiente ./internal/apipublica/...; echo rc=$?` → `rc=0`
  - **Commit**: `rojo(apipublica): edge`
- [x] **TX.9 · verde(apipublica): edge, fichero a fichero** · 🌐 · dep. TX.8 — cerrada en `d1722ba`, `d32b26f`, `1aeadf4`, `7017b28`, `06c4cb9`, `291cd76`, `d4ef6e6` (F3-04)
  - **Hecho cuando**: pendientes = 0; un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9; `SendBudgetFrom` de `plazos.go` da el mismo valor que el viejo para 10 s (9 s) y para ≤ 1 s (0)
  - **Gate**: el de cabecera · `make cobertura-ficheros` como informe (la tabla va al PR; no bloquea)
  - **Commit**: `verde(apipublica): <fichero>` — uno por fichero
- [~] **TX.10 · identidad del centinela `ErrSessionOffline` (D-F3-2)** · 🌐→💻 · dep. decisión D-F3-2 · cumple RX.4.c — 🟡 test de identidad en `dd4cbd2` (F3-04); falta el e2e local, que confirma F3-05
  - **Ficheros**: con **D-F3-2** (recomendación): `internal/modulos/edge/session/registry.go` declara `var ErrSessionOffline = <el de platform>` (el mismo que el viejo desde F0 T0.17) y un test de identidad en `internal/arranque`; **sin** puente. *(Alternativa D-FX-3, si D-F3-2 = no: la línea apunta al viejo y `fronteras_test.go` declara el puente de identidad, «retira: TX.24».)*
  - **Hecho cuando**: `errors.Is(<nuevo>.ErrSessionOffline, <viejo>.ErrSessionOffline)` es `true`; la sesión local lo confirma con el e2e de `cmd/server-modular` (`/admin/flows/start` a una sesión offline → mismo código que el binario viejo)
  - **Gate**: `go test -count=1 -v ./internal/arranque/... > "$TMPDIR/a.log" 2>&1; echo rc=$?` → `rc=0`
  - **Commit**: `refactor(edge): el centinela de sesión offline conserva su identidad`
- [x] **TX.11 · conmutar(edge): 6 rutas a la cara nueva, un solo gw** · 🌐 · dep. TX.9, TX.10 · cumple RX.3.a, RX.4.a–b — cerrada en `0ebb743` (F3-04; `Mount{Messages,Sessions,Diagnostics}`, `FaseActual = 3`, 29 rutas)
  - **Hecho cuando**: `Montar{Mensajes,Sesiones,Diagnosticos}` montados; en los `Deps` viejos `Sender`, `DiagnosticsRequester`, `Sessions`, `SessionProfiles`, `SessionStatus`, `ProfilePush`, `Diagnostics` = `nil`; `ConfigPush` = el gw **nuevo** e `Intents` = el `intentcfg` viejo (E1–E2 siguen en la vieja hasta TX.21, D-FX-1/D-F7-4); J12–J15 reciben el gw **nuevo**, J16–J17 los handlers de `sessionadmin.go` **en el mismo commit que D3–D4** (T-10); aserciones de identidad: el `MessageSender` del notificador viejo, el `ConfigPusher` del `filtercfg` nuevo, el `Deps.ConfigPush` de la cara vieja y el `Frame` (o su adaptador) del selector LLM viejo apuntan al **mismo** gw; `FaseActual = 3`
  - **Gate**: el de TX.7 · `make ci-local; echo rc=$?` → `rc=0`
  - **Commit**: parte del `conmutar(edge): …` de F3

## Tramo F4 · `inferencia` · 🌐 · TX.12–TX.14

Sesión: [`F45-02`](../sesiones/F45-02-web-conmutar-inferencia-y-catalogo.md).

- [ ] **TX.12 · rojo(apipublica): `tenantllm.go` y `degradationnotices.go`** · 🌐 · dep. TX.11 y el verde de `modulos/inferencia` · cumple RX.2.b, RX.2.d
  - **Hecho cuando**: el puerto de `tenantllm.go` sigue **sin** método que devuelva la credencial (`publicapi/tenantllm.go:57-60`); el de avisos sigue siendo de solo lectura
  - **Gate**: `go vet -tags pendiente ./internal/apipublica/...; echo rc=$?` → `rc=0` · **Commit**: `rojo(apipublica): inferencia`
- [ ] **TX.13 · verde(apipublica): inferencia** · 🌐 · dep. TX.12 · **Hecho cuando**: pendientes = 0; un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9 · **Gate**: el de cabecera · **Commit**: `verde(apipublica): <fichero>`
- [ ] **TX.14 · conmutar(inferencia): 4 rutas** · 🌐 · dep. TX.13 · cumple RX.3.a
  - **Hecho cuando**: F1–F4 por la nueva; `TenantLLM`, `DegradationNotices` = `nil` en la vieja; `FaseActual = 4` · **Gate**: el de TX.7 · **Commit**: parte del `conmutar(inferencia)`

## Tramo F5 · `catalogo` · 🌐 · TX.15

Sesión: [`F45-02`](../sesiones/F45-02-web-conmutar-inferencia-y-catalogo.md).

- [ ] **TX.15 · conmutar(catalogo): ninguna ruta, solo `FaseActual = 5`** · 🌐 · dep. el conmutar de F5 · cumple RX.3.a
  - **Hecho cuando**: `FaseActual = 5` y el candado sigue verde **sin** mudar I14–I17 (se quedan en la vieja hasta F8: escriben por `flujos/store`) · **Gate**: el de TX.7 · **Commit**: parte del `conmutar(catalogo)`

## Tramo F6 · `solicitudes` · 🌐 · TX.16–TX.18

Sesión: [`F6-05`](../sesiones/F6-05-web-cara-http-y-conmutar.md).

- [ ] **TX.16 · rojo(apipublica): solicitudes** · 🌐 · dep. TX.15 y el verde de `modulos/solicitudes` · cumple RX.2.b, RX.2.d, RX.2.f, RX.2.g
  - **Ficheros**: `instantes.go`, `intakes.go`, `intakes_llm_gate.go`, `export.go`, `summary.go`, `quotesuggestion.go`, `plazoescritura.go`, `tenantvariables.go`, `integrations.go`, `crmcallback.go`, `eventstelemetry.go`, `eventstelemetry_store.go` y sus 12 tests (24)
  - **Hecho cuando**: `plazoescritura.go` recibe el plazo **por parámetro** (mapa §4.3); G17 sin `Authenticate` y con access-log; el gate de G2 oculta los campos LLM igual que `intakes_llm_gate.go`; `eventstelemetry_store.go` (nivel complejo) con test unitario de mapeo y su puerto con la suite `Contrato(t, func(t) Montaje)`, corrida en memoria aquí y en Postgres con el arnés de F9-A (P4)
  - **Gate**: `go vet -tags pendiente ./internal/apipublica/...; echo rc=$?` → `rc=0` · **Commit**: `rojo(apipublica): solicitudes`
- [ ] **TX.17 · verde(apipublica): solicitudes, fichero a fichero** · 🌐 · dep. TX.16 · **Hecho cuando**: pendientes = 0; un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9; la verdad de `eventstelemetry_store.go` la da la suite contra Postgres (P4) y F9 · **Gate**: el de cabecera · **Commit**: `verde(apipublica): <fichero>`
- [ ] **TX.18 · conmutar(solicitudes): 18 rutas** · 🌐 · dep. TX.17 · cumple RX.3.a, RX.2.f
  - **Hecho cuando**: G1–G18 por la nueva (G2 · G9 · G10 en el **mismo** commit: T-1); en la vieja `Intakes`, `QuoteSuggestions`, `TenantVariables`, `Integrations`, `CRM*`, `EventTelemetry` = `nil`; el arranque pasa a G7 `pipeline.PlazoPorLlamadaSuelo + 12 s` con el **mismo** `PlazoPorLlamadaSuelo` que da a `quotetext.ConPlazo` (aserción); `FaseActual = 6`
  - **Gate**: el de TX.7 · **Commit**: parte del `conmutar(solicitudes)`

## Tramo F7 · `captacion` · 🌐 · TX.19–TX.21

Sesión: [`F7-04`](../sesiones/F7-04-web-cara-http-y-conmutar.md).

Para cuando: 3 rutas más (H1, E1, E2), `FaseActual = 7`, ningún puente desde `apipublica`.

- [ ] **TX.19 · rojo(apipublica): `reanalyze.go` e `intents.go`** · 🌐 · dep. TX.18 y el verde de `modulos/captacion` · cumple RX.2.b, RX.2.d, RX.6.a · **Ficheros**: `reanalyze.go`, `intents.go` y sus 2 tests · **Hecho cuando**: `reanalyze.go` sin gate en la cadena y el 400 de forma antes que los 403 del servicio (T-8); `intents.go` sobre `modulos/captacion/intentcfg` **nuevo** (sin puente), con el gate `llm_intent` dentro del handler y el `ConfigPush` best-effort (mapa E2) · **Gate**: `go vet -tags pendiente ./internal/apipublica/...; echo rc=$?` → `rc=0` · **Commit**: `rojo(apipublica): re-análisis e intenciones`
- [ ] **TX.20 · verde(apipublica): `reanalyze.go` e `intents.go`** · 🌐 · dep. TX.19 · **Hecho cuando**: pendientes = 0; un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9 · **Gate**: el de cabecera · **Commit**: `verde(apipublica): <fichero>` — uno por fichero
- [ ] **TX.21 · conmutar(captacion): H1, E1 y E2** · 🌐 · dep. TX.20 · cumple RX.3.a, RX.6.a
  - **Hecho cuando**: H1, E1 y E2 por la nueva (E1 y E2 en el **mismo** commit: familia, mapa §4.2); `Reanalysis`, `Intents` y `ConfigPush` = `nil` en la vieja; en G7 el arranque lee el plazo del `pipeline` **nuevo**; `FaseActual = 7`. *(Alternativa D-FX-1 literal: aquí solo se retira el puente de `intents.go` hacia `internal/intentcfg`, con commit `refactor(apipublica): intenciones sin puente`.)*
  - **Gate**: el de TX.7 · **Commit**: parte del `conmutar(captacion)`

## Tramo F8 · `conversacion` · 🌐 · TX.22–TX.24

Sesión: [`F8-06`](../sesiones/F8-06-cli-cara-http-y-conmutar.md) (💻: el tramo entero se hace en local).

- [ ] **TX.22 · rojo(apipublica): conversación** · 💻 · dep. TX.21 y el verde de `modulos/conversacion` · cumple RX.2.b, RX.2.d
  - **Ficheros**: `flows.go`, `media.go`, `tenantcontent.go`, `catalogimport.go`, `catalogtabular.go`, `catalogtemplate.go`, `conversationevents.go`, `conversationeventcancel.go` y sus 8 tests (16)
  - **Hecho cuando**: `flows.go` monta I1 e I11–I13 con los handlers de `modulos/conversacion/admin` **nuevos** y usa `nucleo/contact.Ref`; I16–I17 montan con la misma condición que I14 · **Commit**: `rojo(apipublica): conversación`
- [ ] **TX.23 · verde(apipublica): conversación, fichero a fichero** · 💻 · dep. TX.22 · **Hecho cuando**: pendientes = 0; un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9 · **Gate**: el de cabecera · **Commit**: `verde(apipublica): <fichero>`
- [ ] **TX.24 · conmutar(conversacion): 19 rutas y la cara vieja deja de construirse** · 💻 · dep. TX.23 · cumple RX.3.a, RX.3.d, RX.4.a, RX.6.b
  - **Hecho cuando**: I1–I19 por la nueva; el mux viejo es `http.NewServeMux()` **vacío** (sin `publicapi.Register`) → `grep -rn 'publicapi\.' internal/arranque` vacío; J18–J22 con handlers nuevos y el **mismo** rt; lista de puentes de `apipublica` vacía (con D-F3-2 no hubo puente de identidad; con D-FX-3 se retira aquí); `FaseActual = 8`; se corre el e2e de `cmd/server-modular`
  - **Gate**: el de TX.7 · **Commit**: parte del `conmutar(conversacion)`

## Tramo F10 · relevo · 💻 · TX.25

Sesión: [`F10-04`](../sesiones/F10-04-cli-relevo.md).

- [ ] **TX.25 · relevo: se borra la cara vieja y el estrangulador** · 💻 · dep. F9 cerrada · cumple RX.6.c
  - **Hecho cuando**: `internal/publicapi/` borrado (33 + 64 ficheros); `apipublica/estrangulador.go` y su test borrados; el arranque sirve `apipublica.Cara` directamente; `mudanzas_test.go` y `testdata/mapa.tsv` borrados (su trabajo terminó); la huella resuelve contra la `Cara`
  - **Gate**: `make ci-local; echo rc=$?` → `rc=0` · `ls internal/publicapi internal/apipublica/estrangulador.go` → «No such file»
  - **Commit**: parte de `relevo: …`
