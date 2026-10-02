# F4 · `inferencia` — el LLM: vía, prompts, credencial del tenant y degradación

> **Estado**: spec escrita el 2026-09-28 sobre `dev` @ `1b18932` (código) / `bad573a` (plan). Sin
> una línea de código. Marco común: [`../00-marco/`](../00-marco/README.md) (no se repite aquí).
> Norma: [`05`](../../05-metodo-contratos-y-tdd.md). Rutas: autoridad
> [`../FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md) (filas F1–F4).
>
> ✎ **D-F1-10 (Jhoan, 2026-10-02)**: los paquetes de suite de contrato y de dobles llevan el sufijo compuesto
> **`helpertest`**, el único que los candados de fichero eximen ([`DECISIONES.md`](../DECISIONES.md) §2). Esta spec los
> nombraba con `…test` (`degradationtest`, `tenantllmtest`): se actualizó el sufijo, nada más.

## Objetivo en tres líneas

Reconstruir por contrato → rojo → verde los **10 ficheros de producción** (3.072 líneas) de
`internal/{llmvia,llmvia/local,prompts,tenantllm,degradation}` en `internal/modulos/inferencia/`,
con dobles en memoria para los dos puertos que no los tienen, y **conmutar**: el arranque nuevo
construye el selector de vía, los almacenes y la carga de prompts nuevos, y la cara `apipublica`
sirve las 4 rutas de la fase. Hacia fuera no cambia nada (ni una ruta, métrica, texto ni tabla).

## Qué es este módulo (lo mínimo para no perderse)

- 🔴 **ADR-0045** (fuera de este repo, vigente desde 2026-08-23): *«El Cloud es el **único**
  orquestador LLM: construye los prompts, trocea, ordena las llamadas y valida. El Edge se comporta
  como un servidor de inferencia estilo Ollama — prompt entra → JSON sale — y **no interpreta
  nada**.»* Aquí se ve en `llmvia/local`: arma el prompt con los `Build…Prompt` de
  `wapp-shared/llm`, lo empuja por el frame `InferenceRequest` del gateway y aísla el JSON con
  `llm.ExtractJSON`; **no tiene un solo prompt propio** (`internal/llmvia/local/local.go:10-23`).
- **ADR-0030 + E-3** (fuera): dos vías que **no se mezclan** — `local` (el Ollama del Edge del
  tenant, por frame) y `api` (proveedor externo con la credencial del tenant cifrada aquí). Si la vía
  falla se degrada al **Nivel A con aviso al dueño**, nunca a la otra vía. **El Cloud nunca tiene
  Ollama propio.**
- 🔒 **Cero gasto** (decisión D-11 de Jhoan, 2026-08-29, fuera de este repo): *«No se compra una
  credencial de pago para demostrar la vía `api`»*. La vía `api` **nunca corrió en campo con
  credencial real**; su única implementación cableada es `anthropic` (`gemini` es un stub que falla
  nombrado, `internal/tenantllm/tenantllm.go:36-43`). ⇒ En esta fase la vía `api` se prueba **solo**
  con dobles; ninguna tarea pide una clave real, y ningún test llama a un proveedor.
- **P1 no vive aquí**: el prompt de clasificación lo gobierna el catálogo de intenciones
  (`internal/intentcfg`, módulo `captacion`, F7). Este módulo solo ajusta por fichero **P2–P5**.
- **No hay `//go:embed` de plantillas** en este módulo: el texto compilado de P2–P5 vive en
  `wapp-shared/llm@v0.4.5` (`plantilla.go:97 PlantillaPorDefecto`). La excepción de `embed.go` de
  E-3 **no aplica** (ver «Contradicciones»).

## Entradas (tiene que ser cierto para empezar)

| # | Condición | Cómo se comprueba |
|---|---|---|
| E1 | F0 cerrado: `internal/pendiente`, `make test-pendiente`, `make cobertura-ficheros`, los tres candados de `internal/modulos/`, la huella y `apipublica` vacía | `ls internal/pendiente internal/arranque internal/apipublica` · `make -n test-pendiente cobertura-ficheros` |
| E2 | **F3 conmutado**: `internal/modulos/edge/grpc` existe en verde y el arranque nuevo usa su `*Server`, con el **adaptador de `local.Frame`** de F3 que hace hablar al `llmvia` viejo con el gw nuevo (`../FX-cara-http/arquitectura.md` §4.1) | `grep -rn 'modulos/edge/grpc' internal/arranque/*.go` no vacío |
| E3 | F2 conmutado (el gate `entitlements.RequireFeature` que usan las 4 rutas vive en `modulos/acceso/entitlements`) | `ls internal/modulos/acceso/entitlements` |
| E4 | 🔴 **D-F4-1 aplicada en F0** (T0.27: los candados viejos que barren todo `internal/` no ven el árbol nuevo). Sin ella, el primer `verde` de `tenantllm.go` pone rojo `make ci-local` | [`reglas.md`](reglas.md) T-1 |
| E5 | `dev` verde y al día | skill `validar-antes-de-cerrar` |

## Salidas (es cierto al cerrar)

| # | Condición | Verifica |
|---|---|---|
| S1 | 26 ficheros nuevos bajo `internal/modulos/inferencia/` (10 de producción + 4 de dobles/suites + 12 de test) en verde, sin etiqueta `pendiente` | `grep -rln 'go:build pendiente' internal/modulos/inferencia` vacío |
| S2 | Cobertura ≥ 80 % por fichero, fuera los dos `postgres.go` | `make cobertura-ficheros` rc=0 |
| S3 | El arranque nuevo construye `tenantllm`, `degradation`, `prompts` y `llmvia` **nuevos**; los viejos solo los usan los adaptadores `puente_inferencia.go` hasta F7/F8 | `grep -n 'internal/llmvia\|internal/tenantllm"' internal/arranque/*.go` → solo `puente_inferencia.go` |
| S4 | Las 4 rutas F1–F4 las sirve `apipublica`; en la vieja `TenantLLM` y `DegradationNotices` = `nil`; `FaseActual = 4` | TX.14 · huella idéntica |
| S5 | El candado C2 nuevo (`modulos/inferencia/llmvia/c2_via_test.go`) en verde, y el viejo sigue verde | `make ci-local` rc=0 |
| S6 | 0 `t.Skip`, 0 `pendiente.Implementar` en el módulo | `grep -rn 't.Skip\|pendiente.Implementar' internal/modulos/inferencia` vacío |

## Orden de lectura

[`requisitos.md`](requisitos.md) → [`arquitectura.md`](arquitectura.md) → [`diseno.md`](diseno.md)
→ [`reglas.md`](reglas.md) → [`tareas.md`](tareas.md).

## Bloques de sesión

| Bloque | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| **A** · inventario verificado + D-F4-1 verificada (la aplicó F0, T0.27) | 🌐 | T4.1–T4.2 | Números de [`diseno.md`](diseno.md) §1 reconfirmados; candados viejos ciegos al árbol nuevo; `ci-local` rc=0 |
| **B** · contratos y rojo de todo el módulo | 🌐 | T4.3–T4.10 | `make test-pendiente` cuenta las llamadas de la tabla de T4.10; `vet -tags pendiente` rc=0; `ci-local` rc=0 |
| **C** · verde, hojas (`prompts`, `tenantllm`, `degradation`, dobles) | 🌐 | T4.11–T4.18 | 9 ficheros sin etiqueta; cobertura ≥ 80 % |
| **D** · verde, `llmvia/local` y `llmvia` + candado C2 | 🌐 | T4.19–T4.23 | todo el módulo en verde; C2 nuevo verde |
| **E** · conmutar + 4 rutas a `apipublica` (TX.12–TX.14) | 🌐 | T4.24–T4.28 | huella idéntica; `FaseActual = 4` |
| **F** · cierre | 🌐→💻 | T4.29–T4.31 | gates; `ESTADO.md`; traspaso a la local si F9 adelantado |

## Decisiones que necesita (con recomendación)

| Id | Pregunta | Recomendación | Bloquea |
|---|---|---|---|
| 🔴 **D-F4-1** | Dos candados **viejos** barren **todo** `internal/` y se ponen rojos con ficheros **nuevos**: `internal/llmvia/c2_via_test.go:117` (`WalkDir("..")`, lista exacta de ficheros que comparan por vía) y `internal/iam/infra/postgres/membresia_unica_ast_test.go:74` (`"../../.."`, lista exacta de escritores de `tenant_members`, F2). E-1 prohíbe tocarlos | **Segunda excepción a E-1, en F0** (T0.27, según `DECISIONES.md`): los dos barridos saltan los directorios nuevos de primer nivel (`internal/modulos`, `internal/nucleo`, `internal/arranque`, `internal/apipublica`, `internal/pendiente`, y `internal/candados` de F0) con **una línea** cada uno, comparando la **ruta relativa a `internal/`** (no `d.Name()`: `internal/bootstrap/arranque` también se llama `arranque` y debe seguir barriéndose); el árbol nuevo tiene su propio C2. Alternativa peor: ampliar la lista vieja fase a fase (toca código viejo 3 veces: F4, TX.13, F7). Prohibido: renombrar la variable `via` para esquivar el barrido | **F4** (y F2) |
| **D-F4-2** | El candado C2 (I-CP-3, `constitucion.md`) y el de vocabulario Go ↔ `.sql` (`degradation_test.go:170`) **no están** en la tabla de `05` §3.2, y E-7 prohíbe tests que lean código como texto salvo esos | Añadirlos a `05` §3.2: C2 → contrato de `inferencia/llmvia` (AST); vocabulario → contrato de `inferencia/degradation` (lee la `0075`) | T4.9 |
| **D-F4-3** | ¿Se parten las fases del arranque nuevo (`fase4_inferencia.go` de `04` §3) al conmutar? | **No en F4**: se sustituye dentro de las copias de `fase3_almacenes.go`, `fase5_captacion.go` y `prompts.go`; el orden de fases y el primer error visible no cambian. El reparto por módulo, en F10 o en un `refactor(arranque)` aparte | T4.24 |
| **D-F4-4** | Los dos adaptadores de transición (`turnoacotado` viejo necesita `llmvia.TurnoRequest` y `ErrViaSinTurnoAcotado` **viejos**; `reanalisis` viejo necesita `tenantllm.Config` **viejo**) | En `internal/arranque/puente_inferencia.go` (patrón F1), con test de equivalencia. Mueren en F7 (`reanalisis`) y F8 (`turnoacotado`) | T4.25 |

## Contradicciones encontradas (con `04`/`05`/el encargo)

1. **No hay `embed` de plantillas en F4** (`grep -rn 'go:embed' internal cmd` → solo
   `internal/platform/storage/postgres/migrations/embed.go:15`). El texto compilado de P2–P5 está en
   `wapp-shared/llm@v0.4.5/plantilla.go`. La excepción E-3 para `embed.go` no tiene aplicación aquí.
2. **`05` §3.2 no lista el candado C2** ni el de vocabulario de `degradation` (D-F4-2).
3. **`04` §3 da `cmd/prompts/main.go` como «no se toca»**, pero importa `internal/prompts`
   (`cmd/prompts/main.go:28`): en **F10**, al borrar el paquete viejo, ese import cambia a
   `internal/modulos/inferencia/prompts`. En F4 **no** se toca (E-1): el CLI sigue con el viejo, que
   produce los mismos ficheros byte a byte (lo fija el test de ida y vuelta, R4.3.a).
4. **El comentario de paquete viejo nombra mal la variable**: `internal/prompts/prompts.go:9` y `:58`
   dicen `WAPP_PROMPTS_DIR`; el nombre efectivo es **`WAPP_LLM_PROMPTS_DIR`** (loader con prefijo
   `WAPP_` + `LLM_PROMPTS_DIR`, `internal/platform/config/config.go:826`). El contrato nuevo lo
   escribe bien; el viejo no se corrige (E-1).
5. **`04` §2.3 (sustituida) y `04` §3 suponían que F4 era «pequeña y valida el script»**: con el
   método de `05` es una fase **con conmutación delicada** (tres consumidores viejos con tipos del
   paquete viejo, [`arquitectura.md`](arquitectura.md) §4).
