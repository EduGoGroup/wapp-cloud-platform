# F2 · `acceso` — IAM, derechos comerciales y operador de plataforma

> **Estado: por empezar** (spec escrita el 2026-09-28 sobre `dev` @ `1b18932`, releída en `bad573a`).
> Norma: [`05`](../../05-metodo-contratos-y-tdd.md). Forma: [`00-marco/plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md).
> Marco común (no se repite aquí): [`00-marco/`](../00-marco/README.md). Rutas: **autoridad**
> [`FX-cara-http/mapa-de-rutas.md`](../FX-cara-http/mapa-de-rutas.md). Patrones heredados de
> [`F1`](../F1-nucleo-contact/README.md): rojo **solo con exportados** (T-1 de F1: `unused` rompe el
> lint) y adaptador de tipos viejo ← nuevo en `internal/arranque/puente_<x>.go`.

## Objetivo, en tres líneas

1. Reconstruir `internal/iam/**` (la única zona hexagonal), `internal/entitlements` e
   `internal/platformadmin` en `internal/modulos/acceso/…` por contrato → rojo → verde, con **suites
   de contrato** para los 10 puertos de salida del IAM y dobles nuevos donde faltan.
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

## Salidas (es cierto al cerrar)

- `internal/modulos/acceso/{entitlements,iam/**,platformadmin}` con **51 + 6 ✚** ficheros de producción en
  verde, cada uno con su `x_test.go`, y los paquetes `…test` de [`diseno.md`](diseno.md) §2.
- `grep -rn 'pendiente.Implementar' internal/modulos/acceso | wc -l` → **0**; SKIP en `acceso` → **0**.
- `make cobertura-ficheros` ≥ 80 % en todo fichero no-Postgres de `acceso`.
- `cmd/server-modular`: una sola instancia de `acceso/entitlements.Postgres`, las 23 rutas públicas
  servidas por `apipublica`, las 8 de plataforma con los handlers nuevos; `huella_test` igual;
  `cmd/server` sin un byte cambiado.
- `internal/arranque/puente_iam.go` en verde (nace en F2, muere en F3).
- Traspaso escrito para la sesión local (procesos de acceso en F9, candado viejo tocado).

## Orden de lectura

1. [`requisitos.md`](requisitos.md) — historias y criterios EARS.
2. [`arquitectura.md`](arquitectura.md) — paquetes, imports, estado, puente, cableado, rutas.
3. [`diseno.md`](diseno.md) — contratos por paquete, suites, dobles, reglas E-8, candados.
4. [`reglas.md`](reglas.md) — trampas con `fichero:línea` y definición de hecho.
5. [`tareas.md`](tareas.md) — tareas y bloques de sesión.

## Bloques de sesión

| Bloque | Entorno | Tareas | Punto de parada |
|---|---|---|---|
| **A** · inventario verificado | 🌐 | T2.1 | números de [`arquitectura.md`](arquitectura.md) §1 re-medidos · decisiones D-F2-* contestadas |
| **B** · rojo: hojas (`entitlements`, `iam/domain`, `ports`) | 🌐 | T2.2–T2.8 | `make test-pendiente` cuenta lo de esos paquetes · `ci-local` rc=0 · PR |
| **C** · rojo: `usecase`, `infra/*`, `transport/http`, `platformadmin` | 🌐 | T2.9–T2.16 | pendientes de `acceso` contados · `vet -tags pendiente` rc=0 · PR |
| **D** · verde: hojas y dobles | 🌐 | T2.17–T2.21 | `entitlements`, `domain`, `ports`, `infra/memory` a 0 pendientes · cobertura ≥ 80 % · PR |
| **E** · verde: `usecase` + `identity` | 🌐 | T2.22–T2.23 | 0 pendientes en esos paquetes · PR |
| **F** · verde: `infra/postgres`, `transport/http`, `platformadmin` | 🌐 | T2.24–T2.27 | 0 pendientes en `acceso` · candados AST verdes · PR |
| **G** · puente y conmutación + rutas | 🌐 | T2.28–T2.31 | huella igual · 23+8 rutas nuevas · `go list -deps` · PR · traspaso |
| **H** · cierre local | 💻 (🌐→💻) | T2.32–T2.33 | procesos de acceso compilan (y corren si F9 adelantado) · `dev` integrado |

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
   **rojo** en `ci-local`. Hace falta tocar una línea del test viejo (D-F2-2).
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
   (`internal/gateway/grpc/auth.go:200-206`). Lo resuelve `puente_iam.go` (arquitectura §4).

## Decisiones que necesita (de Jhoan, con recomendación)

| # | Pregunta | Recomendación |
|---|---|---|
| D-F2-1 | Los 3 candados AST del canje: ¿se quedan como candados AST del paquete nuevo (y no como proceso)? | **Sí**. Y el de «cuatro columnas NULLables» pasa a test **de conducta** sobre la función pura de mapeo extraída (E-6), que ya no necesita AST |
| D-F2-2 | ¿Se permite tocar **una línea** de `internal/iam/infra/postgres/membresia_unica_ast_test.go` (añadir el escritor nuevo a `escritoresEsperados`) como excepción a E-1? | **Sí**, en el mismo commit que el verde de `memberships.go`, con el motivo en el mensaje. Alternativa peor: que el candado nuevo y el viejo se ignoren mutuamente por ruta |
| D-F2-3 | `platformadmin` sin puerto: ¿se crea `platformadmin/puertos.go` ✚ (solo interfaces) y se separa el SQL de `access_requests.go` en `access_requests_postgres.go` ✚? | **Sí**: es la única forma de que su lógica (aprobación, reintento, unión de systems) nazca cubierta sin BD (E-6). Cambia el árbol de `04` §3 en dos ficheros |
| D-F2-4 | El `Fake` de `entitlements` ¿se queda en `entitlements.go` o se muda a `entitlementstest`? | **A `entitlementstest`** (patrón E-6); `entitlements.go` queda con constantes y el puerto |
| D-F2-5 | ¿Suites de contrato también para los puertos **de entrada** (`ports/in`), que implementa un solo usecase? | **No**: excepción escrita — los cubre el test del usecase que los implementa. Suites **sí** para los 10 de salida |
| D-F2-6 | Reloj inyectable (`WithReloj(func() time.Time)`) en `entitlements.Postgres` y `iamidentity.M2MClient`, exportado nuevo sin equivalente viejo | **Sí**: `contrato-tdd` prohíbe reloj real; no cambia nada observable |
| D-F2-7 | Si F9 se adelanta (`plan/F9-procesos/`), ¿cierra F2 la pasada de los procesos de acceso contra el binario nuevo? | **Sí**, como tarea 🌐→💻 condicionada (T2.33) |
