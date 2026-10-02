# F10 · Requisitos — historias y criterios EARS

> Forma: [`plantilla-de-fase.md`](../00-marco/plantilla-de-fase.md) §2. «UAT» = el VPS de la
> operación (alias SSH `wapp-vps`, unidad `wapp-cloud.service`; datos citados de la documentación de
> operación del ecosistema, fuera de este repo, medidos allí el 2026-08-30).

---

## H10.1 · La prueba en UAT en sustitución (D-9)

> Como **la operación de UAT**, quiero correr `cmd/server-modular` en lugar del viejo durante una
> ventana acordada, con marcha atrás en minutos, para saber que el código nuevo aguanta tráfico real
> antes de borrar el viejo.

- **R10.1.a** · **CUANDO** empieza la ventana, **LA** operación **DEBERÁ** tener copia del binario vivo
  (`bin/server` → `bin/server.viejo-<sha>`) y un volcado de la base, y **DEBERÁ** comprobar que el
  `content_hash` de `public.schema_version` que espera el binario modular es el mismo que el del viejo.
  — Verifica: acta del traspaso con `md5sum`, ruta del volcado y las dos salidas de `migrate -status`.
- **R10.1.b** · **MIENTRAS** dura la ventana, **EL** proceso vivo de `wapp-cloud.service` **DEBERÁ**
  ser el modular. — Verifica: `go version -m /proc/$(systemctl show -p MainPID --value wapp-cloud)/exe`
  muestra `path …/cmd/server-modular` y `vcs.revision` = el SHA de `dev` desplegado, `vcs.modified=false`.
- **R10.1.c** · **CUANDO** arranca el modular, **LAS** dos líneas de clave del log **DEBERÁN** decir
  `key_source=config` o `file`, nunca `generated`. — Verifica: `grep -E "clave pública de(l)? (cifrado|lease)" /root/source/wApp/logs/cloud.log | tail -2`.
- **R10.1.d** · **SI** cualquier criterio de corte de [`diseno.md`](diseno.md) §2.4 falla, **ENTONCES LA**
  operación **DEBERÁ** restaurar el binario viejo y reiniciar en menos de 5 minutos. — Verifica: acta
  con la hora del fallo, la de la restauración y el §6 del despliegue repetido sobre el viejo.
- **R10.1.e** · **LA** dueña del negocio **NO DEBERÁ** notar el cambio: mismas rutas, mismos textos,
  mismos mensajes al cliente. — Verifica: el e2e con WhatsApp real de la ventana y cero quejas
  registradas; `/metrics` con los mismos nombres `wapp_*` que antes (lista capturada antes y después).

## H10.2 · Un solo arranque

> Como **la sesión web**, quiero que `cmd/server` llame a `internal/arranque` y que desaparezcan
> `cmd/server-modular` e `internal/bootstrap`, para que haya un solo cableado que mantener.

- **R10.2.a** · **EL** fichero `cmd/server/main.go` **DEBERÁ** llamar a `arranque.Ejecutar` y **NO
  DEBERÁ** existir `internal/bootstrap/` ni `cmd/server-modular/`. — Verifica:
  `ls internal/bootstrap cmd/server-modular 2>&1 | grep -c 'No such file'` → 2;
  `GOWORK=off go list -deps ./cmd/server | grep -c 'internal/arranque$'` → 1.
- **R10.2.b** · **EL** comando de despliegue **NO DEBERÁ** cambiar: `GOWORK=off go build -o bin/server ./cmd/server`.
  — Verifica: el mismo comando, rc=0, en T10.16.
- **R10.2.c** · **EL** `huella_test.go` **DEBERÁ** comparar el arranque único contra una **dorada**
  congelada del viejo, generada **antes** de borrarlo. — Verifica: `internal/arranque/testdata/huella-vieja.golden`
  existe, fue commiteado antes que el borrado (`git log --oneline -- internal/arranque/testdata/`), y
  `huella_test` falla si se quita una ruta del arranque (caso `muerde`).

## H10.3 · Lo viejo se borra, con sus tests

> Como **Jhoan**, quiero que el código viejo desaparezca entero con sus tests, para que el árbol diga la
> verdad y nadie arregle dos veces lo mismo.

- **R10.3.a** · **AL** cerrar F10, **`internal/`** **DEBERÁ** contener solo `apipublica`, `arranque`,
  `modulos`, `nucleo` y `platform`. — Verifica: `ls -d internal/*/` → esas cinco.
- **R10.3.b** · **SI** algún `.go` de `cmd/`, `internal/` o `test/` importa una ruta vieja, **ENTONCES
  LA** compilación **DEBERÁ** fallar (no existen). — Verifica: `GOWORK=off go build ./... && GOWORK=off go vet ./...` rc=0.
- **R10.3.c** · **LOS** binarios de herramienta (`cmd/casebank`, `cmd/prompts`, `cmd/migrate`,
  `cmd/debug_inferencia`) **DEBERÁN** conservar sus flags y su conducta tras re-apuntar sus imports. —
  Verifica: `go run ./cmd/prompts -comprobar <dir>` y `go run ./cmd/casebank` sin `-consentido`
  («se niega») dan la misma salida antes y después (capturada en T10.1).

## H10.4 · Cero puentes, cero pendientes

> Como **Jhoan**, quiero que el gate pruebe que no queda ni un puente al código viejo ni un contrato
> sin lógica, para que el relevo no deje deuda escondida.

- **R10.4.a** · **EL** candado `internal/modulos/fronteras_test.go` **DEBERÁ** tener la lista de puentes
  **vacía** y fallar si se añade uno. — Verifica: la lista en el fichero; caso `muerde`.
- **R10.4.b** · **EL** candado `no_pending_test.go` (D-F1-12, 2026-10-02; antes `sin_pendientes_test.go`) **DEBERÁ** estar activo y fallar ante cualquier
  `pendiente.Implementar` (y, si D-F10-3, ante la etiqueta `//go:build pendiente`). — Verifica: caso
  `muerde` y `grep -rn 'pendiente.Implementar\|go:build pendiente' --include='*.go' . | wc -l` → 0.
- **R10.4.c** · **EN** el código nuevo, `--- SKIP` **DEBERÁ** ser 0. — Verifica:
  `GOWORK=off go test -v ./... 2>&1 | grep -c -- '--- SKIP'` → 0 (con D-F10-5 = borrar) o solo los de
  `internal/platform` contados y nombrados (si no).

## H10.5 · La integración nueva sustituye a la vieja

> Como **la sesión local**, quiero que `make test-procesos` corra contra el único binario y que la
> batería vieja se retire, para que no queden dos formas de probar contra Postgres.

- **R10.5.a** · **EL** arnés **DEBERÁ** compilar solo `cmd/server` y **NO DEBERÁ** leer
  `WAPP_PROCESOS_BINARIO`. — Verifica: `grep -rn WAPP_PROCESOS_BINARIO . | wc -l` → 0 (salvo historia
  en `documentations/reorganizacion-modular/`).
- **R10.5.b** · **DONDE** D-F10-5 = borrar, **EL** repo **NO DEBERÁ** contener `WAPP_TEST_DB_DSN`,
  `WAPP_TEST_REQUIRE_DB` ni el target `test-integration`. — Verifica:
  `grep -rn 'WAPP_TEST_DB_DSN\|WAPP_TEST_REQUIRE_DB\|test-integration' --include='*.go' --include=Makefile --include='*.yml' . | wc -l` → 0.
- **R10.5.c** · **EL** job `integration` de `.github/workflows/ci.yml` **DEBERÁ** correr los procesos
  (`go test -tags integracion ./test/procesos/...`) en vez de `postgres:16` + `WAPP_TEST_DB_DSN`. —
  Verifica: `grep -n 'tags integracion\|postgres:16' .github/workflows/ci.yml`.

## H10.6 · UAT con el binario de siempre

> Como **la operación de UAT**, quiero desplegar el commit del relevo con el procedimiento de siempre,
> para que el relevo no cambie nada de la operación.

- **R10.6.a** · **CUANDO** el relevo está en `dev`, **LA** operación **DEBERÁ** desplegarlo con
  `GOWORK=off go build -o bin/server ./cmd/server` y `systemctl restart wapp-cloud`, más el reinicio
  explícito de las dos consolas. — Verifica: acta con §6 (`/proc/$pid/exe`) y §9 (líneas de clave) del
  despliegue de UAT, y `path …/cmd/server` en `go version -m`.

## H10.7 · La documentación dice las rutas nuevas

> Como **la sesión local**, quiero actualizar la documentación del repo y la del ecosistema con la
> tabla ruta vieja → nueva, para que nadie siga una ruta que ya no existe.

- **R10.7.a** · **LA** documentación del repo (`CLAUDE.md`, `README.md`, `documentations/*.md`, las
  cinco skills) **NO DEBERÁ** citar rutas de paquetes borrados salvo como historia. — Verifica: el
  comando de [`diseno.md`](diseno.md) §4 → 0 fuera de `reorganizacion-modular/` y de citas marcadas «(histórico)».
- **R10.7.b** · **LA** sesión local **DEBERÁ** reescribir las menciones del ecosistema con la tabla de
  `04` §4 y proponer a Jhoan la regla nueva de ADR-0010. — Verifica: recuento de [`diseno.md`](diseno.md)
  §3.1 → 0 menciones de rutas borradas en `documentations/` de la raíz fuera de lo histórico.
- **R10.7.c** · **LOS** 12 comentarios de repos hermanos **DEBERÁN** citar la ruta nueva, cada uno en
  un commit a `dev` de **su** repo. — Verifica: el grep de [`diseno.md`](diseno.md) §3.3 → 0 rutas viejas.
