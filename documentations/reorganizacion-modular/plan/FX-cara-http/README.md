# FX · La cara HTTP única nueva (`internal/apipublica`), construida por olas — spec transversal

> **Estado: por empezar** (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`). Norma:
> [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> **Transversal**: no es una fase con sesiones propias. Sus tareas `TX.n` **se ejecutan dentro** de
> F0, F2–F8 y F10, y cada fase las copia a su `tareas.md` citando el ID.
>
> ✎ **D-F1-10 (Jhoan, 2026-10-02)**: el arnés y los dobles compartidos de la cara van en `apipublicahelpertest` (esta spec
> decía `apipublicatest`). Los candados de fichero recorren `internal/apipublica` y solo eximen el sufijo compuesto
> `helpertest` ([`DECISIONES.md`](../DECISIONES.md) §2): con el nombre viejo el paquete se mediría como producción.

## Objetivo, en tres líneas

1. Sustituir, **por olas**, la cara HTTP vieja (`internal/publicapi`, 33 ficheros de producción,
   8.762 líneas) por una **nueva y única**, `internal/apipublica`, que el arranque nuevo monta
   **delante** de la vieja (patrón estrangulador, 🔒 D-10 de Jhoan, 2026-09-27).
2. Cada fase de módulo **muda** a la cara nueva las rutas que le tocan según el
   [`mapa-de-rutas.md`](mapa-de-rutas.md), en el mismo ciclo en que conmuta. Al cerrar F8 la cara
   vieja no sirve **ninguna** ruta en el binario nuevo; en F10 se borra.
3. Sin que nada cambie hacia fuera: las 73 rutas de `:8103` con sus patrones **byte a byte**, sus
   cadenas de middleware, sus condiciones de montaje, sus 404/405 y sus etiquetas de métricas.

**Por qué** (D-10, medido): `publicapi.Deps` guarda dos singletons con estado en memoria —el
`*gatewaygrpc.Server` (`internal/bootstrap/arranque/fase8_transporte.go:173,199,254`) y el
`*flowruntime.Runtime` (`:177,229`)— y sus puertos importan tipos de los dominios viejos
(`publicapi.go:24-36`). Una ruta cuyo singleton conmuta no puede seguir en una cara que exigiría la
instancia vieja.

## Entradas (tiene que ser cierto para empezar cada tramo)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | Para TX.1–TX.4 (en F0): `internal/arranque` es la copia de F0 y compila | `go build ./cmd/server-modular; echo rc=$?` |
| E2 | Para cada tramo de fase Fn: el módulo de Fn está en verde y **listo para conmutar** (paso 5 de `05` §4) | la `tareas.md` de Fn |
| E3 | El mapa sigue siendo verdad | §0 del mapa: los dos `grep -c` dan 51·14·1·2·22 y 6 |
| E4 | `internal/publicapi` no cambió desde esta spec | `git log --oneline 1b18932..origin/dev -- internal/publicapi internal/bootstrap/arranque/http.go internal/bootstrap/arranque/rutas_admin.go` vacío; si no, se re-mide el mapa |

## Salidas (es cierto al cerrar F8, y lo remata F10)

- `internal/apipublica` sirve las **73** rutas de `:8103`; `internal/arranque` ya no llama a
  `publicapi.Register` (`grep -rn 'publicapi\.' internal/arranque` → vacío).
- El candado `internal/arranque/mudanzas_test.go` (TX.4) confirma, fase a fase, que la cara nueva sirve
  **exactamente** las filas del mapa con fase ≤ la fase actual.
- La huella (`internal/arranque/huella_test.go`, F0) igual entre los dos binarios en cada cierre.
- En F10: `internal/publicapi/` (33 + 64 tests) y `apipublica/estrangulador.go` borrados.

## Orden de lectura

1. [`mapa-de-rutas.md`](mapa-de-rutas.md) — **la autoridad**: 95 rutas, su fase y por qué.
2. [`requisitos.md`](requisitos.md) — qué se exige, en EARS.
3. [`arquitectura.md`](arquitectura.md) — el estrangulador, la cadena de middlewares, los `Deps`
   huérfanos por fase y los singletons.
4. [`diseno.md`](diseno.md) — los ficheros de `apipublica`, sus contratos y el patrón de test.
5. [`reglas.md`](reglas.md) — trampas con `fichero:línea` y definición de hecho.
6. [`tareas.md`](tareas.md) — `TX.n`, repartidas por fase.

## Dónde caen las tareas (no hay sesiones propias)

| Fase | Tareas TX | Qué deja hecho | Rutas `:8103` mudadas |
|---|---|---|---|
| F0 | TX.1–TX.4 | `apipublica` con `Cara` y `estrangulador` en verde; el arranque compone las dos caras; candado de mudanzas; la huella resuelve por la composición | 0 |
| F2 | TX.5–TX.7 | ficheros comunes (`cadena`, `respuesta`, arnés) · acceso · 8 handlers de `:8100` | 23 |
| F3 | TX.8–TX.11 | `plazos`, `limits` · edge · handlers de sesión portados (también para `:8100`) · identidad del centinela vía `platform` (D-F3-2, sin puente) · el gw nuevo inyectado en la cara vieja para E1–E2 | 6 |
| F4 | TX.12–TX.14 | inferencia | 4 |
| F5 | TX.15 | solo `FaseActual = 5`: sus 4 rutas escriben por `flujos/store` (mapa §2.9) | 0 |
| F6 | TX.16–TX.18 | solicitudes (G7 con plazo inyectado) | 18 |
| F7 | TX.19–TX.21 | re-análisis · intenciones (E1–E2, D-FX-1/D-F7-4) | 3 |
| F8 | TX.22–TX.24 | conversación · la cara vieja deja de construirse | 19 |
| F10 | TX.25 | borrar `publicapi` y el estrangulador | — |

## Decisiones que necesita (de Jhoan)

| # | Pregunta | Recomendación |
|---|---|---|
| **D-FX-1** (= D-F7-4) | `GET/PUT /api/v1/intents`: su código es de captación (F7) pero el `PUT` usa el gw directo. ¿Se mudan en **F3** con un puente declarado `apipublica → internal/intentcfg` (D-10 literal), o se quedan en la cara vieja hasta **F7** recibiendo el gw **nuevo** por su puerto estructural (`publicapi.ConfigPusher`, solo tipos de stdlib; su error solo se registra, `intents.go:167`)? | **F7** (recomendación de [`../DECISIONES.md`](../DECISIONES.md), la línea base del plan): cero puentes en `apipublica` (encaja sin excepciones con la regla 4 de fronteras de F0), un solo gw y sin re-toque en F7; es válida **porque** el mismo mecanismo de inyección ya es obligatorio para G1–G7, G17 e I4 (mapa §4.1). **El mapa y las tareas planifican F7.** *Nota — alternativa (D-10 literal, F3 con puente)*: si Jhoan la eligiera, E1–E2 vuelven a la fila F3 del mapa, TX.8 añade `intents.go` con el puente, TX.21 lo retira, y F0 activa la vía de excepción de su regla 4 (contradicción 7) |
| **D-FX-2** | Los handlers `SetSessionProfileHandler`/`SetSessionStatusHandler` viven hoy en `internal/flujos/admin/sessions.go` (conversación, F8) pero están tipados con `fleet` (edge, F3) y los usan **los dos** listeners. ¿Dónde nacen en F3? | En `apipublica/sessionadmin.go`, **exportados** para que `rutas_admin.go` los use en `:8100`. Consecuencia: `04` §3 `conversacion/admin/sessions.go` **no se reconstruye** en F8 (queda en la cara) |
| **D-FX-3** *(alternativa a D-F3-2)* | Del cierre de F3 al de F8, `POST /api/v1/flows/{id}/start` y `/admin/flows/start` comparan el centinela **viejo** `session.ErrSessionOffline` con errores que devuelve el gw **nuevo** (mapa §4.4) | **Solo si D-F3-2 = no.** La recomendación es **D-F3-2**: desde el ✎ de F0 (T0.17) el centinela viejo **es** el de `platform`, y `modulos/edge/session` declara el mismo: identidad compartida sin puente (TX.10 queda en un test de identidad). La alternativa: `modulos/edge/session` declara `var ErrSessionOffline = <viejo>.ErrSessionOffline` (puente de **identidad** en `fronteras_test.go`), retirado al cerrar F8. Igual para `ErrPushTimeout`/`ErrPushAbandonado` si algún consumidor viejo los compara (hoy: ninguno fuera de `publicapi/messages.go`, que se muda en F3) |
| **D-FX-4** | El adaptador SQL `publicapi/eventstelemetry_store.go` (lee `flow_events`) vive en la cara HTTP. ¿Se conserva así o baja a `conversacion/events`? | **Se conserva** en `apipublica` (F6), como hoy; bajarlo es una mejora de diseño que el plan no hace (sería cambiar dos cosas a la vez). Queda fuera del 80 % por E-6 |
| **D-FX-5** | Los ficheros comunes de la cara: ¿nacen en F0/F1 o con su primer consumidor? | **Con su primer consumidor** (F2 y F3). F0 solo crea el mecanismo (`Cara`, `estrangulador`) porque la huella lo necesita desde el día uno; F1 no muda ninguna ruta y un piloto no debe crecer |

## Contradicciones encontradas (medidas contra el código)

1. **`05` §9.2 y `03` D-10 (recomendación)** proponían repartir `publicapi` en
   `modulos/<m>/http/`. **Sustituido por D-10 cerrada** (cara única nueva por olas). `04` quedó
   marcado en §0, §1, §3 y §4 remitiendo aquí.
2. **`04` §0 cita `§8.2` de `05`**; el apartado es **§9.2**.
3. **`contratos.md` §«Cómo se contó»** cita `internal/bootstrap/http.go` y
   `bootstrap.go → registerAdminRoutes (:1425-1476)`: hoy son `internal/bootstrap/arranque/http.go`
   y `internal/bootstrap/arranque/rutas_admin.go:63-114`. El total (95) sí coincide.
4. **`04` §3 `conversacion/admin/sessions.go`**: el fichero es de edge por sus tipos (D-FX-2).
5. **`04` §3 `catalogo/`**: el módulo se reconstruye en F5, pero sus 4 rutas no pueden mudarse
   hasta F8 (escriben por `flujos/store`, `catalogimport.go:36-38`, `tenantcontent.go:35-39`).
6. **`05` §4 «los dos binarios no comparten los paquetes de dominio»** no basta para los
   singletons: en el binario nuevo, entre F3 y F8, **servicios viejos** tienen que recibir el gw
   **nuevo** (el notificador de solicitudes, el selector LLM, el runtime). No es un puente de import
   (el arranque ya importa lo viejo) pero sí un acoplamiento que F3 debe cablear y probar
   ([`arquitectura.md`](arquitectura.md) §4). Uno de esos puertos **no es estructural**:
   `llmvia/local.Frame.Infer` recibe `gatewaygrpc.InferRequest` (`internal/llmvia/local/local.go:270-272`).
7. **`plan/F0-andamiaje/diseno.md` §4.1, regla 4** prohíbe a `internal/apipublica` importar
   **cualquier** paquete viejo, sin `Puente` posible; `05` §4.1 sí admite puentes declarados en un
   paquete nuevo, y D-FX-1 en su forma literal necesita uno (`apipublica → internal/intentcfg`,
   F3–F7). **Resuelta** con la recomendación de D-FX-1 (F7): la regla de F0 queda como está y F0
   anota la vía de excepción (su `diseno.md` §4.1, regla 4) por si Jhoan eligiera la forma literal.
8. **`plan/F0-andamiaje/diseno.md` §8** describe la cara con un *fallback*; el API exacto es el de
   [`diseno.md`](diseno.md) §2 (`Componer(nueva *Cara, vieja *http.ServeMux)`, sin comodín `"/"`),
   compatible con su test («con la cara vacía, toda petición llega al viejo sin tocar»). La tarea
   `T0.16` de F0 equivale a TX.1–TX.2; TX.3–TX.4 caen en su montaje de `http.go` y en su huella.
