# F1 · Requisitos — historias y criterios EARS

> Cada criterio se verifica con el comando o el test que nombra. `N` = `internal/nucleo/contact`.
> Los tests nuevos no llevan `t.Skip` (D-11) y se leen sin pipe (regla 3 del ecosistema).

## H1.1 · Como **Jhoan**, quiero que `nucleo/contact` nazca por contrato → rojo → verde, para medir el método en el módulo más pequeño antes de comprometer los otros siete.

- **R1.1.a** · **EL** árbol **DEBERÁ** contener en `N/` exactamente `contact.go`, `resolver.go`,
  `repository_memory.go`, `repository_postgres.go` y un `x_test.go` por cada uno. — Verifica:
  `ls N/*.go` · candado `un_fichero_un_test_test.go` en `make ci-local`.
- **R1.1.b** · **CUANDO** se commitee el contrato de un fichero, **EL** commit `rojo(nucleo): …`
  **DEBERÁ** incluir su test con `//go:build pendiente`, y ese test **DEBERÁ** fallar corrido solo.
  — Verifica: `GOWORK=off go test -tags pendiente -run '^TestX$' ./internal/nucleo/contact/; echo rc=$?` → rc≠0.
- **R1.1.c** · **SI** un cuerpo de contrato devuelve un valor cero en vez de
  `panic(pendiente.Implementar(…))`, **ENTONCES EL** revisor **DEBERÁ** rechazar el commit. —
  Verifica: `grep -n 'return' <fichero>` en el commit `rojo` solo muestra firmas sin cuerpo decisorio.
- **R1.1.d** · **EL** commit `verde(nucleo): <fichero>` **DEBERÁ** tocar un solo fichero de
  producción, quitar su etiqueta `pendiente` y dejar su cobertura ≥ 80 % (salvo
  `repository_postgres.go`). — Verifica: `git show --stat <sha>` · `make cobertura-ficheros`.
- **R1.1.e** · **SI** un test de `N` llama a `t.Skip`, **ENTONCES EL** gate **DEBERÁ** fallar. —
  Verifica: `grep -rn 't.Skip' internal/nucleo` vacío · conteo de `--- SKIP` con `-v` = 0.

## H1.2 · Como **la sesión web**, quiero las reglas de los tests viejos escritas en los contratos, para que el test salga del comentario y no del código viejo.

- **R1.2.a** · **EL** comentario de cada exportado de `N` **DEBERÁ** llevar las reglas R-01…R-33 y N-01…N-05 de
  [`diseno.md`](diseno.md) §4 que le tocan, y **CUANDO** una regla no se porte, **EL** commit
  **DEBERÁ** decirlo con su motivo. — Verifica: revisión del diff contra la tabla §4.
- **R1.2.b** · **EL** test de cada fichero **DEBERÁ** mencionar todos sus exportados. — Verifica:
  candado `exportados_cubiertos_test.go`.
- **R1.2.c** · **EL** fichero nuevo **DEBERÁ** decir en su cabecera `// Porta internal/flujos/contact/<f>.go @ <sha>` (E-10). — Verifica: `grep -L '^// Porta internal/flujos/contact/' N/*.go` vacío (sin contar tests).

## H1.3 · Como **la sesión web**, quiero una suite de contrato del puerto `Resolver` que ejecuten memoria y Postgres, para escribir el comportamiento una vez y probarlo en las dos.

- **R1.3.a** · **EL** paquete `N/contacttest` **DEBERÁ** exportar `Contrato(t *testing.T, nuevo func(t *testing.T) Montaje)`. — Verifica: `go doc ./internal/nucleo/contact/contacttest Contrato`.
- **R1.3.b** · **EL** test de `repository_memory.go` **DEBERÁ** ejecutar `contacttest.Contrato`
  contra `NewMemoryResolver` sin BD y con `-race`. — Verifica:
  `GOWORK=off go test -race -run Contrato -v ./internal/nucleo/contact/; echo rc=$?` → rc=0, 0 SKIP.
- **R1.3.c** · **DONDE** Jhoan acepte D-F1-2, **EL** paquete `test/procesos` **DEBERÁ** ejecutar la
  misma suite contra `NewPostgresResolver` con testcontainers (`postgres:17-alpine`), sin
  `WAPP_TEST_DB_DSN`. — Verifica: `make test-procesos` (o `go test -tags integracion -run Contact ./test/procesos/`) rc=0 en local · candado `sin_bd_viva_test.go`.
- **R1.3.d** · **SI** las dos implementaciones divergen en un caso de la suite, **ENTONCES EL**
  informe del piloto **DEBERÁ** listarlo, y la suite no se relaja para ocultarlo. — Verifica: §Informe.

## H1.4 · Como **la dueña del negocio** y **el Edge**, no quiero notar nada: mismos contactos, mismo cifrado, mismos textos.

- **R1.4.a** · **EL** resolver nuevo **DEBERÁ** escribir y leer `public.contacts` con las mismas
  columnas (`value_bidx`, `value_enc`, `value_dek`, `value_kek_id`, `push_name_enc`,
  `push_name_dek`, `push_name_kek_id`) y el mismo SQL de fusión sobre `public.flow_state`, sin
  migración nueva. — Verifica: `ls internal/platform/storage/postgres/migrations/structure | wc -l` → 84 · diff de SQL (T1.11).
- **R1.4.b** · **EL** índice ciego **DEBERÁ** calcularse con el **mismo** `crypto.KeyProvider` que
  usa el resto del arranque. — Verifica: test de cableado (T1.16).
- **R1.4.c** · **EL** paquete nuevo **DEBERÁ** producir, byte a byte, los textos de error de la
  tabla de [`diseno.md`](diseno.md) §5 (los de `ErrInvalidRef` llegan al cliente HTTP en un 400:
  `publicapi/flows.go:159` y `flujos/admin/handlers.go:231`). — Verifica: tabla dorada en `contact_test.go` y `resolver_test.go`.
- **R1.4.d** · **SI** un error del paquete se produce con un `value` o un `push_name` en la mano,
  **ENTONCES EL** texto del error **NO DEBERÁ** contenerlo. — Verifica: casos «no filtra PII» en
  `contact_test.go` y `repository_postgres_test.go`.
- **R1.4.e** · **MIENTRAS** dure la transición, `Normalize`, `NewRef`, `RefsFrom` y `Ref.Sendable`
  nuevos **DEBERÁN** dar la misma salida que los viejos para el corpus del adaptador (el índice ciego
  de `contacts`, de `fleet_sessions.self_pn_bidx` y del anti-self-loop se calcula sobre esa salida).
  — Verifica: test de equivalencia en `internal/arranque/puente_contact_test.go` (T1.14).

## H1.5 · Como **la operación de UAT**, quiero que el binario desplegado no cambie y que el nuevo cablee el paquete nuevo con la misma huella.

- **R1.5.a** · **EL** binario `cmd/server` **NO DEBERÁ** enlazar `internal/nucleo`. — Verifica:
  `GOWORK=off go list -deps ./cmd/server | grep -c 'internal/nucleo/'` → 0.
- **R1.5.b** · **CUANDO** se conmute, `cmd/server-modular` **DEBERÁ** enlazar
  `internal/nucleo/contact` y construir su resolver solo con `contact.NewPostgresResolver` **nuevo**.
  — Verifica: `go list -deps ./cmd/server-modular | grep -c 'internal/nucleo/contact$'` → 1 ·
  test de cableado · `grep -rn 'viejo.NewPostgresResolver' internal/arranque` → 0.
- **R1.5.c** · **EL** candado `internal/arranque/huella_test.go` **DEBERÁ** dar diferencia vacía tras
  la conmutación. — Verifica: `make ci-local` rc=0.
- **R1.5.d** · **EL** código viejo **NO DEBERÁ** cambiar. — Verifica:
  `git diff --stat <sha-inicio-F1>..HEAD -- internal/flujos internal/bootstrap internal/gateway internal/intakes internal/publicapi cmd/server` vacío.

## H1.6 · Como **la sesión local**, quiero cerrar lo que la web no puede, con los gates fijados.

- **R1.6.a** · **CUANDO** la web entregue, **LA** sesión local **DEBERÁ** repetir `make ci-local`
  con golangci-lint v2.12.2, correr la suite contra Postgres (si D-F1-2) e integrar en `dev` sin
  squash. — Verifica: traspaso `traspasos/TRASPASO-F1-nucleo-contact.md` con sección `CERRADO`.

## H1.7 · Como **Jhoan**, quiero un informe con números y una parada, para decidir el rumbo con datos.

- **R1.7.a** · **CUANDO** F1 cierre, **EL** fichero `plan/F1-nucleo-contact/informe-piloto.md`
  **DEBERÁ** existir con todas las secciones de la plantilla de [`tareas.md`](tareas.md) §Informe,
  cada número con su comando. — Verifica: `grep -c '^## ' informe-piloto.md` ≥ 8.
- **R1.7.b** · **MIENTRAS** la parada no tenga respuesta escrita de Jhoan, **NINGUNA** sesión
  **DEBERÁ** empezar F2. — Verifica: la sección «Decisión de Jhoan» del informe, con fecha.
