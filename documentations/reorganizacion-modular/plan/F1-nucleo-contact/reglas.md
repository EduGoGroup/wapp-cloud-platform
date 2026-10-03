# F1 · Reglas de la fase

> Lo común está en [`05`](../../05-metodo-contratos-y-tdd.md) y en [`00-marco/`](../00-marco/plantilla-de-fase.md).
> Aquí, solo lo que muerde en F1. `V` = `internal/flujos/contact` · `N` = `internal/nucleo/contact`.

## 1 · Lo que no se toca

- **Nada de `V`**, ni de sus consumidores (`flujos/runtime`, `flujos/admin`, `gateway/grpc`,
  `gateway/fleet`, `intakes`, `publicapi`), ni `internal/bootstrap/**`, ni `cmd/server`. Ni para
  corregir los comentarios caducados de §3: se anotan y se corrigen en `N`.
- Las 84 migraciones y `public.contacts`: ni una columna, ni un índice (`03` §1).
- `internal/platform/crypto` (`FieldCipher`, `KeyProvider`, `rekeyTargets`): `N` lo usa, no lo cambia.
- 🔴 **Homónimo**: la «DEK» de `value_dek`/`push_name_dek` es la del **envelope de PII de negocio**
  (DEK fresca por valor, envuelta por la KEK del keyring de este repo). **No** es la DEK del
  ADR-0007 —fuera de este repo, ADR-0007: «la DEK que descifra el almacén de `whatsmeow` la custodia
  el cliente y nunca cruza el contrato»—. Ninguna tarea de F1 toca la doble llave.

## 2 · Prohibiciones

- 🚫 `t.Skip`, `WAPP_TEST_DB_DSN` o un Postgres vivo en cualquier test nuevo (D-11, `05` §7.2).
- 🚫 Un cuerpo de contrato que devuelva un valor cero (E-2).
- 🚫 Un segundo `crypto.KeyProvider` o `FieldCipher` para el resolver nuevo: se pasan **los mismos**
  que construye la fase del arranque (arquitectura §4). Otro índice → `value_bidx` distinto →
  contactos duplicados **sin un solo error**.
- 🚫 Re-normalizar dentro de `Resolve`: hoy confía en refs de `NewRef`; cambiarlo alteraría el
  índice ciego de filas existentes si la normalización difiriera en un solo byte.
- 🚫 «Arreglar» el centinela `push_name_enc IS NULL` (gana el primer nombre, R-28) o hacer que la
  memoria copie esa regla: los dos comportamientos son decisiones escritas (MD-046.5, `V/repository_memory.go:80-91`).
- 🚫 Añadir un lector de `push_name` «para poder probarlo» (R-33, `V/repository_postgres.go:32-43`).
- 🚫 Portar el tipo `Contact` salvo que Jhoan rechace D-F1-4.
- 🚫 Levantar `cmd/server` y `cmd/server-modular` a la vez contra la misma BD o puertos (`04` §2.2).

## 3 · Trampas conocidas (medidas en `dev` @ `1b18932`)

| # | Trampa | Dónde | Qué hacer |
|---|---|---|---|
| T-1 | **`unused` rompe el rojo**: un `const`, `func` o campo no exportado sin uso hace fallar el lint | Sonda del 2026-09-28 (golangci-lint 2.14.0 local; **reconfirmar con v2.12.2**) | En el rojo, solo exportados y structs sin campos. Tipo no exportado que implementa un puerto (el adaptador): `var _ viejo.Resolver = (*contactBridge)(nil)` lo mantiene «usado» (sonda: 0 issues)· ✎ **P6 (Jhoan, 2026-10-03)**: el test de un auxiliar no exportado nace en el `verde`, solo si lleva regla de negocio (`05` E-4) |
| T-2 | **Ciclo de imports**: un test interno de `N` no puede importar `contacthelpertest` | Sonda: `import cycle not allowed in test` | `repository_memory_test.go` en `package contact_test` |
| T-3 | Postgres exige tenants **UUID existentes** (FK) y `contactID` UUID: con `"no-existe"` da error de parseo, no `ErrContactNotFound` | `0006_contacts_cifrado.sql:45-46` · `V/resolver_test.go:263` usa `"no-existe"` | La suite usa `uuid.NewString()` y los tenants del `Montaje` |
| T-4 | El comentario de `ErrNoRefs` promete filtrar refs vacías y no lo hace | `V/resolver.go:15-16` vs `V/repository_memory.go:52-55` | El contrato nuevo dice lo que hace el código (diseño §2) |
| T-5 | El comentario de `Resolver.Resolve` dice «actualiza el push_name»: en Postgres solo el primero | `V/resolver.go:39` vs `V/repository_postgres.go:316` | Contrato: qué nombre sobrevive no es parte del puerto |
| T-6 | Referencias `fichero:línea` caducadas en comentarios que se van a portar: «línea 437» (es 447), «353-371» (361-381), «línea 381» (391), «115-137» (116-138), `repository_postgres.go:323` (es 316) | `V/repository_postgres.go:38,240,248-249` · `V/deadlock_integration_test.go:56,93,101` (el `:323` está en `:56` y `:101`; «115-137» en `:93`) | En `N`, citar por **nombre de función**, nunca por línea (E-10 pide el porqué, no el número) |
| T-7 | Comentarios que nombran lo que ya no existe: `BackfillPushName` (murió en `58e92a2`, T5.4) y `backfill_push_name_integration_test.go` (hoy `push_name_cifrado_integration_test.go`) | `V/repository_postgres.go:197,218` · `V/repository_memory.go:91` | No se portan; se dice en el commit `verde` |
| T-8 | Campo muerto en el arranque: `contactsPG` se escribe y no se lee | `bootstrap/arranque/flows.go:35,91` | Se elimina **en la copia** de `internal/arranque` al conmutar (no en el viejo) |
| T-9 | La huella no ve `contact` (sin rutas, rpc, métricas ni goroutines) | `grep` de arquitectura §5 | La prueba de la conmutación es el test de cableado + `go list -deps` |
| T-10 | Un `Normalize` nuevo que difiera en un byte rompe **en silencio** tres índices ciegos: `contacts.value_bidx`, `fleet_sessions.self_pn_bidx` y el anti-self-loop | `internal/flujos/runtime/incoming.go:1266-1300` · `gateway/fleet/fleet.go:40-53` | Corpus de equivalencia viejo ↔ nuevo en `bridge_contact_test.go` (R1.4.e) |
| T-11 | El respaldo de `RefsFrom` con un JID de dispositivo mete el dígito del dispositivo en el número | `V/resolver.go:100-107` + `V/contact.go:112-131` | Se **conserva** (N-05); no se arregla en F1 |
| T-12 | Los tests de `publicapi` y `runtime` usan `contact.NewMemoryResolver` **viejo** (88 líneas en 56 ficheros, todos fuera del paquete) | `grep -rn 'contact\.NewMemoryResolver' --include='*.go' internal \| wc -l` → 88 · con `-rln` → 56 (2026-09-29) | No se tocan: prueban código viejo con tipos viejos. El `MemoryResolver` nuevo servirá a F6/F8 |
| T-13 | El sobre de `push_name` del test viejo del deadlock solo reproduce el ciclo sembrando **sin** nombre; y el reintento de `WithTx` no lo cubre nadie (medido 2026-08-21; lo registra MP-12 de la documentación del ecosistema, abierto y sin fecha: *ningún test se pone rojo al quitar el reintento acotado ante `40P01` de `postgres.WithTx` —`maxTxAttempts = 8`, `internal/platform/storage/postgres/tx.go:63`, backoff exponencial ≤ 50 ms + jitter, respetando `ctx`—*; el arreglo del Plan 026 fue el guard idempotente del `UPDATE` **más** ese reintento) | `V/deadlock_integration_test.go:52-100` | Dato para el proceso de F9; no es trabajo de F1 |
| T-14 | `Rekey` escanea `contacts` **globalmente** (sin tenant): una fila ajena con KEK desconocida aborta la pasada | `V/rekey_integration_test.go:15-21` | Solo afecta a F9 (base aislada por proceso ya lo resuelve, `05` §7.2) |

## 4 · Registro del piloto (obligatorio, lo alimenta el informe)

Cada commit de F1 lleva en su cuerpo una línea:

```
Piloto: <minutos> min · sesión <S0x> · candados-fallidos: <nombre:motivo|ninguno> · reglas-no-portadas: <R-xx:motivo|ninguna>
```

Sin esa línea el informe no se puede escribir con números (y se diría «sin medir»).

## 5 · Definición de hecho de F1

1. `make ci-local` rc=0 con golangci-lint **v2.12.2**, leído del log (skill `validar-antes-de-cerrar`).
2. `GOWORK=off go vet -tags pendiente ./...` rc=0 · `grep -rn 'pendiente.Implementar' internal/nucleo internal/arranque | wc -l` → 0.
3. `GOWORK=off go test -race -v ./internal/nucleo/... ./internal/arranque/... 2>&1 | grep -c -- '--- SKIP'` → 0.
4. `make cobertura-ficheros` ≥ 80 % en `contact.go`, `resolver.go`, `repository_memory.go`.
5. `go list -deps ./cmd/server-modular | grep -c 'internal/nucleo/contact$'` → 1 y `go list -deps ./cmd/server | grep -c 'internal/nucleo/'` → 0.
6. Huella sin diferencias (`internal/arranque/huella_test.go` dentro de `ci-local`).
7. `git diff --stat <inicio-F1>..HEAD -- internal/flujos internal/bootstrap cmd/server` vacío.
8. Suite contra Postgres corrida en local con testcontainers (D-F1-2) o declarada «no corrida» con motivo.
9. `informe-piloto.md` escrito, traspaso con `CERRADO`, todo en `dev` (sin squash).
10. **La parada**: nadie empieza F2 hasta que Jhoan escriba su decisión en el informe.
