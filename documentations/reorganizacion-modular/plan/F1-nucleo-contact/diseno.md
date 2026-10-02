# F1 · Diseño — la vista micro

> `V` = `internal/flujos/contact` (referencia, @ `1b18932`) · `N` = `internal/nucleo/contact`.
> Las reglas R-xx salen de los tests viejos (E-8); las N-xx, del código viejo sin test que las fije.

## 1 · El árbol que se crea

```
internal/nucleo/contact/
├── contact.go                 contact_test.go                package contact (interno)
├── resolver.go                resolver_test.go               package contact (interno)
├── repository_memory.go       repository_memory_test.go      package contact_test (EXTERNO: ver §6)
├── repository_postgres.go     repository_postgres_test.go    package contact (interno)
└── contacttest/
    ├── contrato.go            (sin test propio: D-F1-3)      la suite del puerto: Montaje, Estado, Contrato y la tabla de casos
    ├── resolve_contrato.go · isolation_contrato.go · merge_contrato.go · destination_contrato.go
    │   concurrency_contrato.go · pushname_contrato.go          los casos, un fichero por tema
    ├── fixtures_contrato.go · assertions_contrato.go           las ayudas compartidas por los casos
    ├── estado.go              estado_test.go                 doble de flow_state (tiene lógica)
internal/arranque/
├── puente_contact.go          puente_contact_test.go         adaptador viejo ← nuevo (arquitectura §3)
test/procesos/                                                (si D-F1-2)
└── contact_contrato_test.go                                  la suite contra PostgresResolver
```

## 2 · Los contratos, fichero a fichero

Exportados medidos con `GOWORK=off go doc -short ./internal/flujos/contact` (19 de primer nivel +
5 métodos). 🔴 En el **rojo** solo existen los exportados: un `const`, `func` o campo **no exportado**
sin uso hace fallar `unused` en el lint (verificado 2026-09-28 con una sonda y golangci-lint 2.14.0
local, **no** la v2.12.2 fijada: comprobarlo en T1.1). Los auxiliares nacen en su `verde`.

### `contact.go` — el dominio puro (porta `V/contact.go`)

| Exportado | Firma | Promete (lo que el test afirma) |
|---|---|---|
| `KindPhoneE164` · `KindWALID` · `KindWAUsername` | `const = "phone_e164"` · `"wa_lid"` · `"wa_username"` | Valores literales (viajan a BD en `contacts.kind`) |
| `ErrInvalidRef` | `var = errors.New("contact_ref inválida")` | Base de todo error de ref; texto exacto (R-10) |
| `Ref` | `struct{ Kind, Value string }` | `Value` ya normalizado; `(tenant, Kind, Value)` es la unidad de dedup |
| `ValidateKind` | `func(kind string) error` | nil para los 3 kinds; `ErrInvalidRef` envuelto para el resto, sensible a mayúsculas (R-01) |
| `Normalize` | `func(kind, value string) (string, error)` | R-02…R-08, R-11 y los textos de §5 |
| `NewRef` | `func(kind, value string) (Ref, error)` | `Normalize` + `Ref`; el único constructor recomendado (R-09) |
| ~~`Contact`~~ | `struct{ID; Refs; PushName}` | **No se porta** (D-F1-4): no se instancia en todo el repo (`grep -rn 'contact\.Contact\b\|Contact{'` → solo comentarios) |

### `resolver.go` — el puerto y lo que lo rodea (porta `V/resolver.go`)

| Exportado | Firma | Promete |
|---|---|---|
| `ErrNoRefs` · `ErrNoDestino` · `ErrContactNotFound` | `var` (textos §5) | Se inspeccionan con `errors.Is` |
| `Resolver` | `interface{ Resolve(ctx, tenantID string, refs []Ref, pushName string) (string, error); Destino(ctx, tenantID, contactID string) (Ref, error) }` | §3 (la suite) |
| `StateMigrator` | `interface{ MigrateContactID(ctx, tenantID, from, to string) error }` | Re-clava el estado del huérfano en el canónico; en conflicto de sesión **conserva el del canónico** (R-17) |
| `Ref.Sendable` | `func (r Ref) Sendable() (string, error)` | phone → el valor; lid → `valor+"@lid"`; otro → `ErrNoDestino` con `kind %q no direccionable` (R-20) |
| `RefsFrom` | `func(fromPn, fromLid, from string) []Ref` | R-22; puede devolver vacío; descarta en silencio lo que no normaliza |

Comentario del puerto, corregido frente a `V` (E-8, se dice en el commit): **(a)** `ErrNoRefs` solo
sale con la lista vacía tras deduplicar; `Resolve` **no** filtra `Ref{}` vacías (`V` promete «todas
vacías o no normalizables», `resolver.go:15-16`, y no lo hace, `repository_memory.go:52-55`):
precondición «refs construidas con `NewRef`». **(b)** «Si `pushName` no es vacío, lo registra; qué
nombre sobrevive si llegan varios **no** es parte del contrato» (Postgres conserva el primero,
memoria el último: `repository_memory.go:80-91`). **(c)** `tenantID` y `contactID` son UUID: con uno
mal formado Postgres da un error de parseo, **no** `ErrContactNotFound` (memoria sí da este): la
suite usa siempre UUID bien formados.

### `repository_memory.go` (porta `V/repository_memory.go`)

`MemoryResolver` (struct **sin campos en el rojo**) · `NewMemoryResolver(migrator StateMigrator) *MemoryResolver`
(migrator puede ser nil) · métodos `Resolve`, `Destino`. Promete: todo lo de la suite; seguro para
concurrencia; en la fusión llama **una vez por huérfano** a `migrator.MigrateContactID(huérfano,
canónico)` y, si falla, `Resolve` devuelve `contact: migrar flow_state en fusión: %w`. Conserva el
⚠️ de «aquí gana el último nombre» (`V:80-91`).

### `repository_postgres.go` (porta `V/repository_postgres.go`)

`PostgresResolver` · `NewPostgresResolver(db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider) *PostgresResolver`
(sin validación, como hoy) · `Resolve` (una transacción `postgres.WithTx`, reintento ante
`40P01`/`40001` de `platform`) · `Destino`. El SQL se copia **literal** (R-1.4.a). Funciones puras
extraídas para probar sin BD (nacen en su `verde`, no exportadas):

| Función | Sale de | Qué se prueba sin BD |
|---|---|---|
| `codificarRef(kp, cipher, tenantID, ref)` | `encodeRef` `V:184-191` | bidx = `kp.BlindIndex(tenant, value)`; otro tenant → otro bidx; `enc` no contiene el valor; ida y vuelta |
| `sobrePushName(cipher, nombre)` | `pushNameEnvelope` `V:221-230` | `""` → tres piezas vacías **sin cifrar** (R-26); `"Ana"` → tres pobladas, `kekID` `"1"` en modo compat; error de cifrado sin el nombre |
| `elegirCanonico([]candidato{id, creado})` | `pickCanonicalDB` `V:361-381` | el `created_at` menor; empate → el `id` menor |
| `abrirFilas(cipher, filas, contactID)` sobre `interface{Next() bool; Scan(...any) error; Err() error}` | bucle de `Destino` `V:432-459` | cada fila con **su** `kek_id` (R-24); `kek_id` ausente del keyring → `contact: descifrar value:`; error de `Scan`/`Err` con su prefijo; cero filas → `ErrContactNotFound` con `%q` del id |
| `nullStr` | `V:329-334` | `""` → NULL |

`Resolve(ctx, t, nil, "")` sobre `NewPostgresResolver(nil, …)` devuelve `ErrNoRefs` **sin tocar la
BD**: es el único camino de `Resolve` probable en unitario. El resto del SQL lo cubre §3 contra
Postgres (T1.13/T1.18) y los procesos de F9.

## 3 · La suite de contrato `contacttest.Contrato`

```go
// Package contacttest es la suite de contrato del puerto contact.Resolver y sus dobles.
// Ningún código de producción lo importa (mismo criterio que internal/gateway/fleet/fleettest).
package contacttest

// Montaje es lo que cada implementación entrega a la suite.
type Montaje struct {
	Resolver         contact.Resolver
	TenantA, TenantB string // dos tenants válidos y distintos (en Postgres: filas de public.tenants)
	Estado           Estado // el flow_state que la fusión migra; obligatorio
}

// Estado siembra y observa el estado conversacional por (tenant, sesión).
type Estado interface {
	Sembrar(t *testing.T, tenantID, sessionID, contactID string)
	Dueno(t *testing.T, tenantID, sessionID string) (contactID string, ok bool)
}

// Contrato ejecuta todas las promesas de contact.Resolver contra la implementación que
// devuelve nuevo, con un Montaje limpio por caso. No salta nada: un caso que no aplica
// a una implementación es un defecto del puerto, no de la suite.
func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje)
```

| Caso (`t.Run`) | Regla |
|---|---|
| `SinRefs_ErrNoRefs` (nil y `[]Ref{}`) | R-18 |
| `RefNueva_CreaID` · `MismaRef_MismoID` · `RefRepetidaEnLaEntrada_UnID` | R-12, R-13, código `V/resolver.go:136-151` |
| `DosRefsJuntas_UnID` · `RefNuevaJuntoAExistente_SeAta` | R-14, R-15 |
| `MismoValorOtroKind_OtroContacto` · `MismaRefOtroTenant_OtroContacto` | N-02, N-01 |
| `Fusion_CanonicoElMasAntiguo_ElHuerfanoDesaparece` (`Destino(huérfano)` → `ErrContactNotFound`) | R-16, N-03 |
| `Fusion_MigraElEstadoDelHuerfano` · `Fusion_ConflictoConservaElCanonico` | R-16, R-17 |
| `Destino_PrefiereTelefono` · `Destino_SoloLID` · `Destino_SoloUsername_ErrNoDestino` | R-19, R-20 |
| `Destino_Inexistente` (UUID bien formado) · `Destino_OtroTenant` → `ErrContactNotFound` | R-21, N-01 |
| `Destino_DevuelveElValorNormalizado` | R-23 (ida y vuelta) |
| `Concurrente_MismaRef_UnSoloID` (16 goroutines, `-race`) | R-32 |
| `PushName_NoCambiaLaIdentidad` | N-04 |

La suite **no** afirma qué `push_name` sobrevive (R-28: divergencia aceptada) ni la ausencia de
`40P01` (R-29, proceso de F9). Con memoria, los dos casos de estado prueban que `MemoryResolver`
llama bien al migrador; la política de conflicto que se ve es la del doble `contacttest.Estado…`
(así lo dice su comentario). Con Postgres prueban el SQL de `fuseDB`.

## 4 · Reglas de los tests viejos (E-8) — dónde aterriza cada una

Tests viejos: `contact_test.go` 11 `Test*` · `resolver_test.go` 11 · `repository_postgres_test.go` 7 ·
`deadlock_integration_test.go` 1 · `push_name_cifrado_integration_test.go` 2 ·
`rekey_integration_test.go` 2 = **34** (`grep -c '^func Test'`); los **12** de los cuatro últimos son de
integración (`openTestDB`, `WAPP_TEST_DB_DSN`, `repository_postgres_test.go:48-75`). Sin BD: 22 PASS y
**12 SKIP** con `rc=0` (`go test -v`, 2026-09-28).

| # | Regla | Origen | Aterriza en |
|---|---|---|---|
| R-01 | Solo 3 kinds; `""`, `"phone"`, `"PHONE_E164"` inválidos | `contact_test.go:9-23` | `contact_test` |
| R-02 | phone: solo dígitos; fuera `+`, espacios, guiones, paréntesis, puntos | `:25-46` | `contact_test` |
| R-03 | Dos formatos del mismo número → mismo valor (base del dedup y del índice ciego) | `:48-62` | `contact_test` |
| R-04 | phone inválido: vacío, sin dígitos, solo separadores, > 15 dígitos | `:64-78`, `contact.go:36` | `contact_test` |
| R-05 | LID: quita `@servidor`, `_agente`, `:dispositivo` y blancos de borde | `:80-101` | `contact_test` |
| R-06 | LID inválido: `""`, `"@lid"`, `"abc@lid"`, `"12ab34@lid"` | `:103-111` | `contact_test` |
| R-07 | username: minúsculas + recorte; solo blancos → inválido | `:113-124` | `contact_test` |
| R-08 · R-09 | kind desconocido → `ErrInvalidRef`; `NewRef` normaliza o falla | `:126-144` | `contact_test` |
| R-10 | Texto de `ErrInvalidRef` (el viejo solo mira `contains "contact_ref"`: el nuevo fija el literal) | `:146-151` | `contact_test` |
| R-11 | Ningún error de `Normalize` contiene el valor (sube a logs) | `:153-177`, `contact.go:121-123` | `contact_test` |
| R-12…R-18 | Resolve: nueva, reusa, juntas, ata, fusión + migración, conflicto, sin refs | `resolver_test.go:63-203` · `repository_postgres_test.go:127-223` | suite |
| R-19…R-21 | Destino: preferencia, solo LID, solo username, inexistente | `resolver_test.go:205-266` | suite + `resolver_test` (`Sendable`) |
| R-22 | `RefsFrom`: pn+lid → 2; JID crudo con kind inferido por `@lid`; nada → vacío | `resolver_test.go:268-288` | `resolver_test` |
| R-23 | Valor cifrado en reposo; no hay columna `value` | `repository_postgres_test.go:253-297` | `codificarRef` + F9 |
| R-24 | `value_kek_id` = KEK current; `Destino` abre cada fila con su `kek_id`; KEK ausente → error | `:304-365`, `rekey_integration_test.go:111-200` | `abrirFilas` + F9 |
| R-25 | Migración 0007 re-aplicable | `repository_postgres_test.go:370-410` | **No se porta**: es del runner de `platform` |
| R-26 · R-27 | `push_name` vacío → sobre NULL; el nombre tardío se sella | `push_name_cifrado_integration_test.go:138-195` | `sobrePushName` + F9 |
| R-28 | Gana el primer nombre (centinela `push_name_enc IS NULL`, MD-046.5) | `:197-228`, `repository_postgres.go:283-320` | comentario + SQL literal + F9 |
| R-29 | Ráfaga sin `40P01`; la siembra **sin nombre** es contrato del test | `deadlock_integration_test.go:29-150` | Proceso «entrante a respuesta» (F9) |
| R-30 | Rotación: todo legible; reanudable | `rekey_integration_test.go:111-262` | F9 (el `Rekey` es de `platform`) |
| R-31 · R-32 | Una transacción con reintento; get-or-create con `ON CONFLICT DO UPDATE … RETURNING` | `repository_postgres.go:83-110,140-178` | SQL literal + suite (R-32) |
| R-33 | No hay lector de `push_name`, a propósito | `repository_postgres.go:32-43` | comentario |
| N-01 | Aislamiento por tenant en `Resolve` y `Destino` | `repository_memory.go:46-48,179` · PK | suite |
| N-02 | El kind es parte de la clave (`88887777` phone ≠ lid) | PK `(tenant_id, kind, value_bidx)` | suite |
| N-03 | Tras la fusión el huérfano no existe | `repository_memory.go:165` · `repository_postgres.go:408-412` | suite |
| N-04 | `pushName` no cambia el `contact_id` | código | suite |
| N-05 | JID de dispositivo `57300…:5@s.whatsapp.net` en el respaldo de `RefsFrom` normaliza **con** el dígito del dispositivo (`contact.go:112-131` guarda todo dígito) | lectura del código | `resolver_test` fija el comportamiento **actual**; si es un defecto, se arregla en `V` y en `N` a la vez (fuera de F1) |

## 5 · Textos observables (se copian byte a byte)

`contact_ref inválida` · `…: kind desconocido %q` · `…: phone_e164 sin dígitos` ·
`…: phone_e164 con %d dígitos excede el máximo %d` · `…: wa_lid vacío` ·
`…: wa_lid con parte de usuario no numérica (longitud %d)` · `…: wa_username vacío: %q` ·
`contact: se requiere al menos una contact_ref` · `contact: sin destino enviable para el contact_id`
(+ `: kind %q no direccionable`) · `contact: contact_id no encontrado` (+ `: %q`) · y los envoltorios
del adaptador: `contact: buscar ref:` · `insertar contacto:` · `cifrar value:` · `cifrar push_name:` ·
`actualizar push_name:` · `adjuntar ref:` · `created_at del contacto:` · `podar flow_state huérfano:` ·
`migrar flow_state en fusión:` · `re-apuntar refs en fusión:` · `leer refs del contacto:` ·
`cerrar filas:` · `escanear ref:` · `descifrar value:` · `iterar refs:` (todos con prefijo `contact: ` y `%w`).

## 6 · Reparto de los tests (E-3, E-6, E-9) y una trampa de Go

- `repository_memory_test.go` es **`package contact_test`**: un test interno (`package contact`) que
  importe `contacttest` —que importa `contact`— da `import cycle not allowed in test` (verificado con
  una sonda, 2026-09-28). Consecuencia: prueba solo por exportados (E-7 lo pide igual).
- `contact_test.go`, `resolver_test.go`, `repository_postgres_test.go`: internos (prueban auxiliares
  que usan sus hermanos, E-7), **sin** importar `contacttest`.
- Candado de exportados: `resolver_test.go` menciona `Resolver` y `StateMigrator` con aserciones de
  compilación (`var _ Resolver = (*MemoryResolver)(nil)`, idem `PostgresResolver`); si F0 contó
  también métodos, `Destino` de Postgres solo es mencionable así (sin BD no se puede llamar).
- Umbral 80 % (D-12): `contact.go`, `resolver.go`, `repository_memory.go`. Línea base de los tests
  viejos: **98,0 % · 95,2 % · 91,1 %**; `repository_postgres.go` **0 %** (sin BD), medido con
  `go test -coverprofile` y agregado por fichero (49, 42, 79 y 125 sentencias).
- Candados de invariante de `05` §3.2: **ninguno** es de `contact`.

## 7 · Ejemplo concreto: el rojo de `contact.go` (T1.2)

```go
// Package contact es la identidad de contacto del núcleo: una o más referencias
// {kind, value} que resuelven a un contact_id opaco (UUID). Dedup por (tenant, kind, value).
//
// Porta internal/flujos/contact/contact.go @ <sha de dev>.
package contact

import (
	"errors"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
)

// ErrInvalidRef es la base de todo error de referencia inválida. Su texto es observable:
// las rutas de arranque de flujo lo devuelven en un 400 ("contact_ref inválida: " + err).
var ErrInvalidRef = errors.New("contact_ref inválida")

// Normalize valida el kind y devuelve el valor normalizado:
//   - phone_e164: solo los dígitos; vacío o más de 15 dígitos es inválido.
//   - wa_lid: la parte de usuario sin "@servidor", "_agente" ni ":dispositivo"; numérica.
//   - wa_username: minúsculas y sin blancos de borde; vacío es inválido.
//
// Error: envuelve ErrInvalidRef y NUNCA contiene el valor recibido (va a los logs).
func Normalize(kind, value string) (string, error) {
	panic(pendiente.Implementar("contact.Normalize"))
}
```

```go
//go:build pendiente

package contact

func TestNormalize_Telefono(t *testing.T) {
	for _, c := range []struct{ entrada, quiere string }{
		{"+1 (415) 555-2671", "14155552671"}, {"44.20.7946.0018", "442079460018"},
	} {
		if got, err := Normalize(KindPhoneE164, c.entrada); err != nil || got != c.quiere {
			t.Errorf("Normalize(phone, %q) = %q, %v; quiere %q", c.entrada, got, err, c.quiere)
		}
	}
}

func TestNormalize_ErrorNoContieneElValor(t *testing.T) {
	_, err := Normalize(KindPhoneE164, "12345678901234567890")
	if !errors.Is(err, ErrInvalidRef) || strings.Contains(err.Error(), "12345678901234567890") {
		t.Fatalf("quiere ErrInvalidRef sin el valor crudo; dio %v", err)
	}
	if want := "contact_ref inválida: phone_e164 con 20 dígitos excede el máximo 15"; err.Error() != want {
		t.Fatalf("texto observable: %q, quiere %q", err.Error(), want)
	}
}
```

(Imports omitidos: `errors`, `strings`, `testing`.) `GOWORK=off go test -tags pendiente -run '^TestNormalize_Telefono$' ./internal/nucleo/contact/; echo rc=$?`
→ `rc=1` por el `panic`. Y el de la suite, en `repository_memory_test.go` (rojo):

```go
//go:build pendiente

package contact_test

func TestMemoryResolver_Contrato(t *testing.T) {
	contacttest.Contrato(t, func(t *testing.T) contacttest.Montaje {
		est := contacttest.NuevoEstado()
		return contacttest.Montaje{Resolver: contact.NewMemoryResolver(est),
			TenantA: uuid.NewString(), TenantB: uuid.NewString(), Estado: est}
	})
}
```
