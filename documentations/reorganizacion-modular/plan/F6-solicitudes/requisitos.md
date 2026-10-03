# F6 · Requisitos — historias y criterios EARS

> Formato: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2. `S` =
> `internal/modulos/solicitudes`. Cada criterio nombra el comando o el test que lo verifica.

## H6.1 · El dominio de la solicitud nace por contrato

> Como **la sesión web**, quiero reconstruir `intakes` (la solicitud, su máquina de 11 estados y la
> bandeja) desde contratos que ya digan todas sus reglas, para que la lógica llegue con su test hecho.

- **R6.1.a** · **EL** paquete `S/intakes` **DEBERÁ** tener los 24 ficheros de producción de
  [`diseno.md`](diseno.md) §2.1 (los 23 de `internal/intakes` + `note.go`), cada uno con su
  `x_test.go` al lado. — Verifica: `un_fichero_un_test_test.go` verde.
- **R6.1.b** · **EL** contrato de `status.go` **DEBERÁ** fijar los 11 estados literales
  (`open`, `pending_approval`, `confirmed`, `deposit_requested`, `deposit_paid`, `settled`,
  `cancelled`, `expired`, `abandoned`, `rejected`, `needs_info`) y el alias legado `closed` →
  `confirmed` resuelto en un único punto. — Verifica: `TestStatus_*` en `S/intakes/status_test.go`.
- **R6.1.c** · **SI** un texto observable del paquete viejo (error que llega por HTTP, plantilla que
  ve el cliente por WhatsApp, nombre de evento) cambia un solo byte, **ENTONCES EL** test del fichero
  **DEBERÁ** fallar. — Verifica: aserciones de texto de [`diseno.md`](diseno.md) §5 en cada test.
- **R6.1.d** · **EL** contrato de `note.go` **DEBERÁ** conservar `MaxNoteRunes = 280`, el saneo
  (saltos → espacio, controles e invisibles fuera, emojis dentro, medir **después** de sanear, nunca
  truncar) y el texto `cart: la indicación mide %d runas y el máximo es %d` (D-F6-4). — Verifica:
  `S/intakes/note_test.go`.
- **R6.1.e** · **MIENTRAS** un fichero esté en rojo, **SU** test **DEBERÁ** llevar
  `//go:build pendiente` y el contrato solo símbolos **exportados** con cuerpo
  `panic(pendiente.Implementar(…))` (patrón F1: el linter `unused` rompe el gate con no exportados
  sin uso). — Verifica: `go vet -tags pendiente ./internal/modulos/solicitudes/...` rc=0 y
  `make ci-local` rc=0.

## H6.2 · Los invariantes de la bandeja siguen vigilados

> Como **Jhoan**, quiero que las reglas de seguridad de la bandeja (una sola puerta para aprobar y
> para preguntar, el plazo que no mata, la poda que se sella) sigan con candado en el paquete nuevo,
> para que la reconstrucción no borre la memoria de sus incidentes.

- **R6.2.a** · **EL** test `S/intakes/inv1_aprobar_test.go` **DEBERÁ** exigir que la **única**
  llamada a `.Approve(` del código de producción esté en `internal/apipublica` (control positivo,
  exactamente 1) y que **ninguno** de los directorios automáticos vigentes en la fase la contenga, con
  la guarda anti-hueco (directorio inexistente o sin ficheros → `t.Fatalf`). — Verifica:
  `TestINV1_SoloElPOSTDelDueñoAprueba` verde con la lista de [`diseno.md`](diseno.md) §6.
- **R6.2.b** · **EL** mismo barrido **DEBERÁ** exigir lo mismo para `.RequestInfo(`. — Verifica:
  `TestINV1_SoloElPOSTDelDueñoPregunta`.
- **R6.2.c** · **EL** repo entero **NO DEBERÁ** contener el nombre del evento de vencimiento
  (compuesto por concatenación en el test, nunca escrito entero), con el control positivo
  `deposit_reminded_at`; **Y EL** paquete `S/intakes` **NO DEBERÁ** leer `order_ttl`/`OrderTTL`, con
  el control positivo `QuoteDeadline` (= 24 h). — Verifica: los dos tests de
  `S/intakes/vencimiento_test.go` §candados.
- **R6.2.d** · **CUANDO** la lectura de revisiones pode el literal, **EL** instante sellado
  **DEBERÁ** publicarse (no descartarse). — Verifica: test puro de `sellarPodada` y el candado AST
  `revisionsOf → ejecutarPoda → sellarPodada` en `S/intakes/postgres_test.go` (llega con el verde, T6.18).
- **R6.2.e** · **SI** una de estas reglas solo se puede comprobar con BD, **ENTONCES** **DEBERÁ**
  estar como aserción de un proceso de F9 (P5). — Verifica: `R9.6.c` de F9 (`TestP5_AprobarDosVecesUnSoloEfecto`).

## H6.3 · Los puertos nacen cubiertos, en memoria y en Postgres

> Como **la sesión web**, quiero que cada puerto con adaptador Postgres tenga su suite de contrato
> `Contrato(t, func(t) Montaje)` corrida por un doble en memoria y, con el arnés, contra Postgres (P4),
> para saber que los dos se comportan igual.

- **R6.3.a** · **EL** puerto `intakes.Store` **DEBERÁ** tener `intakeshelpertest.Contrato(t, …)` y
  **EL** `MemoryStore` nuevo **DEBERÁ** pasarla en unitario. — Verifica:
  `go test -race ./internal/modulos/solicitudes/intakes/ -run Contrato`.
- **R6.3.b** · **EL** puerto `integrations.Store` **DEBERÁ** tener `integrationshelpertest.Contrato` **Y**
  un doble nuevo `integrationshelpertest.Memoria` (el paquete no tiene gemelo, `05` E-6). — Verifica:
  `go test ./internal/modulos/solicitudes/integrations/integrationshelpertest/`.
- **R6.3.c** · **EL** puerto `tenantvars.Store` **DEBERÁ** tener `tenantvarshelpertest.Contrato`, que
  pasan `MemoryStore` (unitario) y `Postgres` (arnés, T6.27). — Verifica: ídem.
- **R6.3.d** · **DONDE** un fichero sea adaptador Postgres (`*postgres*.go`, D-F6-6), **SU** test
  unitario **DEBERÁ** cubrir constructor, validación, mapeo de filas y de errores con funciones
  puras, sin BD. — Verifica: `grep -rn 'sql.Open\|WAPP_TEST_DB_DSN' internal/modulos/solicitudes` vacío.
- **R6.3.e** · **LA** marca de estado de cada suite **DEBERÁ** vigilar todas las columnas que la operación
  puede tocar, no una sola (hallazgo 35 de F1). — Verifica: revisión del `contrato.go` de cada `…helpertest`.

## H6.4 · El contrato CRM no se mueve ni un campo

> Como **el integrador CRM**, quiero que `intake.push` y el callback `intake.status` sigan idénticos,
> para no tener que tocar el puente.

- **R6.4.a** · **EL** payload de `crmpush.Build` **DEBERÁ** validar contra
  `docs/contracts/wapp-crm-v1/intake.push.schema.json`, y los ejemplos de los tres verbos contra su
  esquema draft 2020-12 (D-F6-3). — Verifica: `crmpush/push_test.go` y
  `integrations/contrato_wapp_crm_v1_test.go`.
- **R6.4.b** · **EL** candado del contrato **DEBERÁ** exigir que `RevisionNo` y `LifecycleStatus`
  nunca se asignen con una constante, barriendo `S/integrations/crmpush` **y** el `runtime` de la
  conversación vigente (el viejo hasta F8). — Verifica: `crmpush/contrato_test.go`.
- **R6.4.c** · **CUANDO** llegue un callback, **LA** cara nueva **DEBERÁ** aceptarlo solo con firma
  `X-Wapp-Signature: v1=<hex>` HMAC-SHA256 del cuerpo crudo, `X-Wapp-Timestamp` dentro de ±300 s,
  comparación en tiempo constante, cuerpo ≤ 64 KiB y **sin JWT**. — Verifica: `sigv1_test.go` y
  `apipublica/crmcallback_test.go` (TX.16).
- **R6.4.d** · **SI** el worker del outbox agota `WAPP_WEBHOOK_MAX_ATTEMPTS`, **ENTONCES** la fila
  **DEBERÁ** quedar `dead` y la métrica `wapp_webhook_deliveries_total{status}` contar uno de
  `delivered|failed|dead|claim_lost`. — Verifica: `integrations/worker_test.go` con reloj y métrica dobles.

## H6.5 · La dueña no nota nada

> Como **la dueña del negocio**, quiero que la bandeja (listar, editar, aprobar, pedir información,
> descartar, exportar, P5, integraciones y variables) responda igual, para no notar la reconstrucción.

- **R6.5.a** · **CUANDO** F6 conmute, **LA** cara nueva **DEBERÁ** servir exactamente G1–G18 del
  mapa de FX, con el mismo patrón byte a byte, permiso, recurso de auditoría, gate y condición de
  montaje; G2 · G9 · G10 en el **mismo** commit. — Verifica: `huella_test.go` y el candado de mudanzas (TX.18).
- **R6.5.b** · **EL** plazo de escritura de G7 **DEBERÁ** seguir siendo
  `PlazoPorLlamadaSuelo (48 s) + 12 s`, **derivado** del mismo valor que recibe `quotetext.ConPlazo`,
  no copiado. — Verifica: aserción de cableado en `internal/arranque` (TX.18).
- **R6.5.c** · **SI** la ruta de un dominio ya conmutado quedara en la cara vieja, **ENTONCES EL**
  candado de mudanzas **DEBERÁ** fallar. — Verifica: FX TX.7.

## H6.6 · La conversación vieja sigue funcionando contra lo nuevo

> Como **la operación de UAT**, quiero que el carrito y el motor viejos (hasta F8) sigan cerrando
> pedidos, avisando y empujando al CRM con los objetos nuevos de solicitudes, para que la prueba en
> sustitución no pierda ni una comanda.

- **R6.6.a** · **EL** arranque nuevo **DEBERÁ** construir **un solo** `intakes.Service` (nuevo),
  **un solo** notificador y **un solo** par de recordatorios, y dárselos al motor viejo por sus
  puertos estructurales (`IntakeAbandoner`, `DepositReminder`). — Verifica: test de cableado
  `internal/arranque/solicitudes_cableado_test.go` (T6.24), que además afirma por grep de import que
  ninguna fase importa los paquetes viejos de solicitudes fuera del sitio declarado para el carrito.
- **R6.6.b** · **EL** carrito viejo **DEBERÁ** recibir el escritor de revisiones y el garante del
  envío por la vía que decida D-F6-1 (segunda instancia vieja o adaptador `bridge_intakes.go`), con
  fecha de muerte F8 anotada. — Verifica: ídem + tabla de
  [`arquitectura.md`](arquitectura.md) §4.
- **R6.6.c** · **EL** `WebhookSink` viejo **DEBERÁ** encolar por el `integrations.Postgres` nuevo y
  consultar el `EntitlementsGate` nuevo (puertos estructurales `crmpush.Queuer`/`Gate`). — Verifica: ídem.
- **R6.6.d** · **SI** `internal/modulos/solicitudes/**` importa un paquete viejo fuera del puente (import)
  declarado (`telemetria → internal/flujos/store`), **ENTONCES EL** gate **DEBERÁ** fallar. —
  Verifica: `fronteras_test.go`.

## H6.7 · El binario viejo es el oráculo

> Como **la sesión local**, quiero cerrar la fase comprobando los procesos de la bandeja y del CRM
> contra los dos binarios, para no confiar solo en tests unitarios.

- **R6.7.a** · **DONDE** D-F9-1 (F9 adelantado) esté aceptada, **LA** sesión local **DEBERÁ** correr
  las suites de contrato de `intakes`, `integrations` y `tenantvars` contra Postgres y P5/P6 contra
  `viejo` y `nuevo` (T9.27). — Verifica: el bloque de F6-06 en `ESTADO.md` y, mientras existan los dos
  entornos, la sección `CERRADO` del traspaso `TRASPASO-F6-solicitudes.md`.
- **R6.7.b** · **EL** binario `cmd/server` **NO DEBERÁ** cambiar. — Verifica:
  `git diff <sha-inicio>..HEAD -- cmd/server internal/bootstrap internal/publicapi internal/intakes internal/integrations internal/tenantvars` vacío.
