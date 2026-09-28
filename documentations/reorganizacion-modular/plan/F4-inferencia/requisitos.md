# F4 · Requisitos — historias y criterios EARS

> Roles y patrones EARS: [`../00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2.
> `N` = `internal/modulos/inferencia` · `V` = paquetes viejos de referencia (`internal/llmvia`, …).

## H4.1 · El módulo nace con contrato y test, sin lógica

> Como **la sesión web**, quiero **crear los 10 ficheros de producción de `inferencia` y sus dobles
> con solo su contrato y un test escrito desde él**, para **que la lógica llegue con algo que ya la
> valide**.

- **R4.1.a** · **EL** contrato de cada fichero de `N` **DEBERÁ** tener cuerpos
  `panic(pendiente.Implementar("<paquete>.<Símbolo>"))` y ningún valor cero como cuerpo. — Verifica:
  revisión de T4.3–T4.9 y `make test-pendiente` > 0.
- **R4.1.b** · **EL** test de cada `x.go` **DEBERÁ** llevar `//go:build pendiente` mientras esté en
  rojo y compilar. — Verifica: `GOWORK=off go vet -tags pendiente ./internal/modulos/inferencia/...; echo rc=$?` → `rc=0`.
- **R4.1.c** · **CUANDO** se corre un test rojo solo, **EL** test **DEBERÁ** fallar por el `panic`. —
  Verifica: `go test -tags pendiente -run '^TestFor$' ./internal/modulos/inferencia/llmvia/; echo rc=$?` → rc≠0.
- **R4.1.d** · **EL** rojo **DEBERÁ** declarar solo exportados (el linter `unused` rompe el gate con
  no exportados sin uso; regla T-1 de F1). — Verifica: `make lint` rc=0 tras cada `rojo(inferencia)`.
- **R4.1.e** · **SI** un test de `N` llama a `t.Skip`, **ENTONCES EL** gate **DEBERÁ** fallar. —
  Verifica: `grep -rn 't.Skip' internal/modulos/inferencia` vacío.

## H4.2 · La vía se pregunta en un solo sitio (C2, I-CP-3)

> Como **Jhoan**, quiero **que la regla «si hay un `if via` fuera del adaptador, es defecto» siga
> custodiada en el árbol nuevo**, para **que no nazcan dos pipelines**.

- **R4.2.a** · **EL** candado `N/llmvia/c2_via_test.go` **DEBERÁ** fallar si un fichero de
  producción bajo `internal/{modulos,nucleo,arranque,apipublica}` compara (`==`, `!=`, `switch`) un
  identificador cuyo nombre en minúsculas sea `via`, empiece por `via` o acabe en `via`, y no está en
  su lista de permitidos; y **DEBERÁ** fallar también si un permitido ya no compara. — Verifica:
  mutación (añadir `if cfg.Via == "api"` en un fichero no permitido ⇒ rojo) en T4.23.
- **R4.2.b** · **EL** único `switch` por vía que elige adaptador **DEBERÁ** vivir en
  `N/llmvia/llmvia.go` (`For`), con sus tres hermanos (`Warm`, `PlazaDe`, `Turno`) **en el mismo
  fichero**. — Verifica: el mismo candado.
- **R4.2.c** · **MIENTRAS** dure la transición, **EL** candado C2 **viejo** (`internal/llmvia/c2_via_test.go`) **DEBERÁ** seguir verde sin editar su lista. — Verifica: `make ci-local` rc=0 tras cada verde (requiere D-F4-1).

## H4.3 · Los prompts P2–P5 se ajustan por fichero y una plantilla inválida no arranca

> Como **la operación de UAT**, quiero **que `WAPP_LLM_PROMPTS_DIR` funcione igual en el binario
> nuevo**, para **seguir afinando texto sin release de `wapp-shared`**.

- **R4.3.a** · **CUANDO** se vuelca un directorio y se carga, **EL** cargador **DEBERÁ** devolver,
  byte a byte, `llm.PlantillaPorDefecto(e)` para las cuatro etapas. — Verifica: `TestVolcarYCargar_*` en `N/prompts/volcar_test.go`.
- **R4.3.b** · **SI** un `.tmpl` tiene un prefijo que no es `p2-`…`p5-`, dos ficheros reclaman la
  misma etapa, falta un marcador, las secciones van al revés o **el esquema contiene un valor que su
  propio validador rechaza** (`"package_size": 0` en P4), **ENTONCES EL** cargador **DEBERÁ**
  devolver un error que envuelve `ErrPromptsDir` y nombra el fichero y el motivo. — Verifica: tabla de casos en `prompts_test.go` (5 casos, textos de `diseno.md` §4).
- **R4.3.c** · **SI** `prompts.Cargar` falla, **ENTONCES EL** arranque nuevo **DEBERÁ** abortar
  (sin degradar al texto compilado), con el error `prompts ajustables de P2-P5: …`. — Verifica: test
  de la copia de `prompts.go` en `internal/arranque` (T4.26).
- **R4.3.d** · **EL** arranque **DEBERÁ** dejar en el log de qué fichero (o `compilada`) salió cada
  etapa. — Verifica: mismo test, con logger de captura.
- **R4.3.e** · **EL** cargador **NO DEBERÁ** recargar en caliente (no hay vigilante de ficheros ni
  goroutine). — Verifica: `grep -n 'go func\|fsnotify\|time.Tick' internal/modulos/inferencia/prompts` vacío.

## H4.4 · La credencial del tenant no sale de donde debe

> Como **la dueña del negocio**, quiero **que mi clave del proveedor siga cifrada y que solo la vea
> quien llama al proveedor**, para **no perderla por un log**.

- **R4.4.a** · **EL** tipo `tenantllm.Config` **NO DEBERÁ** tener campo para la clave (solo
  `HasAPIKey`). — Verifica: test de reflexión sobre los campos en `tenantllm_test.go`.
- **R4.4.b** · **CUANDO** el tenant no tiene fila, **EL** selector **DEBERÁ** usar la vía `local` y
  no pedir la credencial (REQ-33). — Verifica: `TestFor` caso «sin fila» con `pedidasLa == 0`.
- **R4.4.c** · **SI** se pide `APIKey` de un tenant sin fila, sin sobre o con `via='local'`,
  **ENTONCES EL** store **DEBERÁ** devolver `ErrNotConfigured`. — Verifica: suite `tenantllmtest.Contrato` sobre el doble (y sobre Postgres en F9).
- **R4.4.d** · **CUANDO** un `Upsert` pasa de `api` a `local`, **EL** store **DEBERÁ** retirar la
  credencial y el consentimiento. — Verifica: la misma suite.
- **R4.4.e** · **SI** un `Upsert` en vía `api` llega sin clave o sin consentimiento, o con una vía
  fuera de `local|api`, **ENTONCES EL** store **DEBERÁ** devolver error sin escribir. — Verifica: la misma suite + el unitario de `postgres_test.go` para la validación previa al SQL.

## H4.5 · La degradación avisa una vez por ventana y solo de lo que es un fallo

> Como **la dueña del negocio**, quiero **un aviso cuando mi LLM cae al Nivel A, y no uno por
> mensaje ni por el sistema funcionando**, para **que el aviso siga significando algo**.

- **R4.5.a** · **EL** vocabulario de motivos **DEBERÁ** ser exactamente los 8 literales
  `ollama_down breaker_open edge_offline timeout api_error credencial lease_invalid edge_sin_capacidad`
  y coincidir como conjunto con el `CHECK owner_degradation_notices_reason_check` de la `0075`. —
  Verifica: test que lee `…/migrations/structure/*.sql` (D-F4-2).
- **R4.5.b** · **SI** `Record` recibe un motivo fuera del vocabulario (incluido uno sano:
  `fastlane`), una vía fuera de `local|api` o un tenant vacío, **ENTONCES EL** escritor **DEBERÁ**
  devolver `ErrMotivoDesconocido` / `ErrViaDesconocida` / `ErrTenantVacio` **sin llamar al store**. —
  Verifica: doble que cuenta llamadas (`saves == 0`).
- **R4.5.c** · **CUANDO** N fallos del mismo (tenant, motivo, vía) caen en la misma ventana de
  15 min (`VentanaDe`: `at.UTC().Truncate(v)`), **EL** escritor **DEBERÁ** producir una sola fila.
  — Verifica: suite `degradationtest.Contrato` sobre el doble; la del índice único, en F9.
- **R4.5.d** · **EL** mapeo error→motivo (`motivoDe`) **DEBERÁ** cumplir la tabla de
  [`diseno.md`](diseno.md) §3.2 — incluidos los 6 casos que **no** avisan (`llm.ErrLLMQuality`
  envuelto o no, `api.ErrUnsupportedProvider`, motivo inventado, motivo sano, error cualquiera). —
  Verifica: tabla en `notify_test.go`.
- **R4.5.e** · **CUANDO** un fallo tiene motivo, **EL** selector **DEBERÁ** contar la caída en el
  observador **aunque no haya notificador**, y escribir el aviso con un contexto **desacoplado**
  (`context.WithoutCancel`) y un techo de 3 s. — Verifica: `TestDegradacion_SeCuentaAUNQUENoHayaNotificador`-equivalente y el de «contexto muerto».

## H4.6 · La vía local obedece al reloj de su llamante

> Como **el Edge**, quiero **recibir un `timeout_ms` que nunca sea más estricto que lo que el Cloud
> está dispuesto a esperar**, para **que el veredicto lo emita quien lo sabe**.

- **R4.6.a** · **CUANDO** el ctx trae deadline D, **EL** adaptador local **DEBERÁ** pedir
  `D − 7 s` (`MargenVeredicto`) al Edge; **SI** eso es ≤ 0, **ENTONCES** **DEBERÁ** devolver
  `ErrSinPresupuesto` sin tocar el cable; sin deadline, 30 s (`DefaultTimeout`). — Verifica: `local_test.go`.
- **R4.6.b** · **EL** adaptador **DEBERÁ** fijar por etapa `max_output_tokens` P1 192 · P2 512 ·
  P3 512 · P4 1024 · P5 768, calentamiento 16, y `class` `interactivo` solo en P1 (y el turno);
  **CUANDO** `WithMaxOutputTokens(false)`, **DEBERÁ** mandar 0 (campo ausente). — Verifica: tabla en `local_test.go`.
- **R4.6.c** · **EL** turno acotado **DEBERÁ** pedir 12 s (`PlazoTurno`), 128 tokens, temperatura
  0, clase `interactivo`, y esperar hasta `12 s + 7 s`. — Verifica: `llmvia_test.go` con un frame falso.
- **R4.6.d** · `MargenVeredicto` (7 s) **DEBERÁ** ser mayor que `edge/grpc.DefaultInferGrace` (5 s).
  — Verifica: aserción en `local_test.go` (importa `modulos/edge/grpc`).

## H4.7 · El binario nuevo usa lo nuevo y hacia fuera no cambia nada

> Como **Jhoan**, quiero **conmutar `inferencia` sin que la huella cambie**, para **poder relevar en
> F10 con confianza**.

- **R4.7.a** · **CUANDO** `conmutar(inferencia)` se aplica, **LA** huella del arranque nuevo
  **DEBERÁ** ser idéntica a la del viejo (rutas, rpc, métricas, variables, goroutines). — Verifica: `internal/arranque/huella_test.go`.
- **R4.7.b** · **EL** arranque nuevo **DEBERÁ** construir **un solo** `*llmvia.Selector` (nuevo) y
  pasárselo a etapas, aforo, `quotetext`, `intakeahead` y —por el adaptador— a `turnoacotado`. —
  Verifica: test de cableado en `internal/arranque` (T4.27).
- **R4.7.c** · **SI** el tenant está en vía `api` y el `turnoacotado` viejo pide un turno,
  **ENTONCES EL** adaptador **DEBERÁ** devolver un error que satisface
  `errors.Is(err, <llmvia viejo>.ErrViaSinTurnoAcotado)`. — Verifica: `puente_inferencia_test.go`.
- **R4.7.d** · **LAS** rutas F1–F4 del mapa FX **DEBERÁN** servirse desde `apipublica` con el mismo
  scope, feature, auditoría y condición de montaje. — Verifica: TX.14 y la huella.
- **R4.7.e** · **EL** nombre de la métrica `wapp_llm_degradacion_total{origen,via,reason}` y los
  valores de `origen` (`seleccion`, `pipeline`, `turno`) **NO DEBERÁN** cambiar. — Verifica: huella de métricas + test de constantes.
