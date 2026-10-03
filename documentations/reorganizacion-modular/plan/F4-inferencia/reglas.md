# F4 · Reglas de la fase

> Las reglas comunes (gates, lectura del `rc`, SKIP, commits, toolchain) están en
> [`../00-marco/tecnologia.md`](../00-marco/tecnologia.md) y
> [`../00-marco/flujo-web-local.md`](../00-marco/flujo-web-local.md). Aquí solo lo de F4.

## 1 · Lo que no se toca

- **Nada de `internal/{llmvia,prompts,tenantllm,degradation}`** ni de sus tests (E-1), con la única
  salvedad de **D-F4-1** (una línea de exclusión en el barrido del C2 viejo), que **no** pone F4:
  la pone F0 (T0.27) y F4 solo la verifica (T4.2).
- **`cmd/prompts`**: sigue importando `internal/prompts` hasta F10.
- **`wapp-shared/llm`**: ni se sube de versión ni se toca. Si una regla del prompt estorba, se
  arregla allí con release (I-ECO-5), nunca con un prompt propio aquí (C2 del ADR-0044: «este paquete
  no tiene un solo prompt propio»).
- Las migraciones `0071`, `0073`, `0075`: ni una letra (full-replay, I-CP-7).
- El literal de la métrica y de sus etiquetas.

## 2 · Trampas conocidas

| Id | Trampa | Dónde | Cómo se evita |
|---|---|---|---|
| **T-1** | 🔴 **El C2 viejo barre el árbol nuevo.** `internal/llmvia/c2_via_test.go:117` recorre `..` (= `internal/`) y exige que la lista de ficheros que comparan por vía sea **exactamente** la suya. El primer `verde(inferencia): tenantllm.go` (`ValidVia` compara con `ViaLocal`) lo pone rojo, y con él `make ci-local` | `c2_via_test.go:50-101,117,130` | D-F4-1, aplicada en **F0** (T0.27) y verificada en T4.2 **antes** del primer verde. Si no está, **parar** |
| T-2 | El detector de C2 es **ancho**: `esNombreDeVia` = `via`, prefijo `via` o **sufijo** `via` en minúsculas (`envia`, `todavia` también cuentan) | `c2_via_test.go:218-221` | No nombrar así variables comparadas fuera de los permitidos; prohibido esquivar el candado renombrando `via` |
| T-3 | **`WithLocalOptions` acumula**. Escribir `s.localOpts = opts` pasa todos los tests de una sola llamada y mata `WAPP_LLM_PROMPTS_DIR` | `llmvia.go:142-144` | Test con **dos** llamadas (R4.3 + techo) |
| T-4 | **Copiar las opciones por petición**: `append(s.localOpts, …)` sobre el slice del `Selector` compartido cruza la sesión de origen entre tenants «una de cada muchas» | `llmvia.go:241-252,363-368` | `make([]local.Option, 0, len+1)` + test con `-race` y dos `For` concurrentes |
| T-5 | Un `(*local.Provider)(nil)` devuelto como `llm.LLMProvider` deja de ser `nil` | `llmvia.go:253-258` | Devolver `nil, err` explícito |
| T-6 | `motivoDe`: el orden de ramas **es** el contrato (calidad primero; `ErrUnsupportedProvider` antes que `ErrInvalidConfig`) | `notify.go:64-102` | La tabla de `diseno.md` §2 entera en el test |
| T-7 | **El aviso sobre un ctx ya cancelado** falla justo cuando hay algo que contar | `notify.go:161-166,197` | `context.WithoutCancel` + 3 s; test con ctx cancelado |
| T-8 | **El observador cuelga del notificador** ⇒ subcuenta (dedupe) y depende de la BD | `notify.go:184-192` | `contarDegradacion` **antes** del `if s.notifier == nil` |
| T-9 | `MargenVeredicto` ≤ `DefaultInferGrace` ⇒ el veredicto lo decide el `select` al azar | `local.go:204-207` | Aserción `MargenVeredicto > edgegrpc.DefaultInferGrace` |
| T-10 | Un calentamiento que avisara al dueño lo mandaría a revisar un equipo sano | `llmvia.go:315-320` | `Warm` no pasa por `notifying`/`avisar`; test «no escribe aviso» |
| T-11 | `Parsear` con `TrimSpace` cambia P5 | `prompts.go:232-237` | Test de ida y vuelta sobre las 4 etapas |
| T-12 | La extensión se compara sin mayúsculas y el prefijo en minúsculas: `P4-x.TMPL` **sí** carga (el comentario viejo, `prompts.go:33-37`, sugiere lo contrario) | `prompts.go:167,206` | El contrato escribe lo que hace el código; caso de test |
| T-13 | `Notifier` literal (`&Notifier{store: s}`) con ventana 0 ⇒ `Truncate(0)` = un aviso por fallo | `degradation.go:351-362` | Default resuelto en el uso; test con literal |
| T-14 | El homónimo **DEK**: `api_key_dek` es del sobre de PII de negocio | `tenantllm/postgres.go:139` | No citar ADR-0007 en este contrato |
| T-15 | `api_key_kek_id` de la fila, no la KEK actual | `tenantllm/postgres.go:168-172,215` | La suite no lo ve (doble); se deja escrito en el contrato del adaptador y lo cubre F9 |
| T-16 | **Cero gasto**: ningún test ni tarea usa una clave real ni llama a Anthropic/Gemini | decisión D-11 (2026-08-29) | La vía `api` se prueba con `api.New` **fallando** (`ErrInvalidConfig`, sin red) y con dobles del store |
| T-17 | `local.plazo` usa `time.Until` (reloj real) | `local.go:514-523` | Tests con `context.WithTimeout` holgado y comparaciones por rango, no por igualdad |
| T-18 | Un test de `N` que importe el paquete **viejo** (p. ej. para comparar salidas) es un puente (import) | `05` §4.1 | Solo el adaptador `bridge_inferencia.go` y su test (en `internal/arranque`) importan lo viejo |

## 3 · Prohibiciones

- 🚫 `t.Skip` (también en los dobles). 🚫 Un Postgres vivo: las suites corren contra Postgres solo con el arnés (testcontainers, F9-A).
- 🚫 Un segundo `*llmvia.Selector` en el arranque nuevo (R4.7.b).
- 🚫 Recarga en caliente de prompts, vigilante de ficheros o goroutine en `prompts`.
- 🚫 Ampliar el vocabulario de motivos o de vías (exigiría tocar la `0075`/`0073`).
- 🚫 Mover la construcción del selector a otra fase del arranque en F4 (D-F4-3).
- 🚫 Declarar pasado un gate con un `golangci-lint` que no sea el fijado (`../00-marco/tecnologia.md`).

## 4 · Definición de hecho

F4 está hecha cuando, **leído del log y sin pipe**:

1. `GOWORK=off make ci-local` → `GATE_RC=0` (incluye el C2 viejo, el C2 nuevo y `fronteras_test`).
2. `GOWORK=off go vet -tags pendiente ./...` → `rc=0`.
3. `grep -rn 'pendiente.Implementar\|go:build pendiente\|t.Skip' internal/modulos/inferencia internal/arranque/bridge_inferencia*.go` → vacío.
4. Un test por promesa del contrato; mutantes en el nivel complejo; procesos de F9. Sin umbral de cobertura (P2):
   `make cobertura-ficheros` es informe (la tabla va al PR; no bloquea) e incluye `bridge_inferencia.go`.
5. `GOWORK=off go test -v ./internal/modulos/inferencia/... 2>&1 | grep -c -- '--- SKIP'` → `0`.
6. Huella idéntica (`internal/arranque/huella_test.go`) con `FaseActual = 4`.
7. `grep -rln '"github.com/EduGoGroup/wapp-cloud-platform/internal/\(llmvia\|tenantllm\|degradation\|prompts\)' internal/arranque` → solo `bridge_inferencia.go` y su test (lo afirma su test de cableado).
8. El adaptador de `local.Frame` que dejó F3 (`bridge_gateway.go`) **ya no existe**.
9. `ESTADO.md` y este README actualizados; SHA de cada tarea en `tareas.md`.
10. La sesión local corrió las suites `tenantllmhelpertest.Contrato` y `degradationhelpertest.Contrato` contra Postgres
    con el arnés (T4.31 = T9.25) y los procesos de F9 contra el binario nuevo, con 0 SKIP.
11. **`Conmutados`** (`internal/modulos/fronteras_test.go`): `inferencia` **no** entra al cerrar F4. Un módulo entra
    cuando muere su último adaptador (`05` §4.2). En F4 **nace** `bridge_inferencia.go` (`llmConfigBridge`,
    `turneroBridge`) y **muere** `bridge_gateway.go` de F3. `llmConfigBridge` muere en F7; `turneroBridge`, y con él el
    fichero, en **F8**: ahí entra `inferencia` en `Conmutados`. `FaseActual = 4` sí se fija en F4.

## 5 · Ceremonia, tests y adaptadores (`05` E-12, E-4, §4.2)

- **Niveles** (los fija el inventario E-12, T4.1; provisional en [`diseno.md`](diseno.md) §6):
  simple = contrato, test y lógica en **una pasada**, varios archivos por sesión;
  medio = rojo y verde por archivo, **agrupados por paquete**, un test por promesa;
  complejo = esquema completo E-2…E-9, con **mutantes** donde haga falta.
- **No se relaja en ningún nivel**: equivalencia viejo ↔ nuevo, `make ci-local` rc=0 con **0 SKIP**, procesos de F9.
- **Auxiliares no exportados** (`motivoDe`, `avisar`, `plazo`…): nacen en el verde; su test, también en el verde y
  solo si llevan regla de negocio o ramas no triviales. No se testea fontanería ni `if err != nil`; el resto lo cubre F9.
- **Puertos con BD** (`tenantllm.Store`, `degradation.Store`): suite `Contrato(t, func(t) Montaje)` corrida en memoria
  **y** en Postgres con el arnés. La marca de estado vigila todas las columnas que la operación puede tocar (hallazgo 35).
- **Adaptador** `bridge_inferencia.go`: nivel simple, con test de cableado obligatorio y completo (hallazgo 39).
  `un_fichero_un_test` y el informe de cobertura lo incluyen.
- **Corpus de equivalencia adversario** (hallazgo 40): los corpus viejo ↔ nuevo llevan casos adversarios, no solo
  felices: separadores repetidos (`a@@b`; aquí, marcadores `--- INSTRUCCION ---` duplicados o pegados), dígitos no
  ASCII y espacios Unicode (en nombres de fichero `pN-`, en la vía y en el motivo).
