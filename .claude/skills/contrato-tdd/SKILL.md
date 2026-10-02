---
name: contrato-tdd
description: Use when creating or finishing ONE file of the modular reconstruction of wapp-cloud-platform (internal/modulos/**, internal/nucleo/**, internal/arranque/**) — writing its contract without logic, its x_test.go derived from that contract (red, behind //go:build pendiente), or later porting the logic to turn it green. Enforces the rules of documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md. Triggers — "contrato de <fichero>", "rojo de X", "pasa a verde X", "escribe el test del contrato", "porta la lógica de X", "crea el fichero X del módulo Y".
---

# Un fichero, de contrato a verde

> **La norma vive en [`documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md`](../../../documentations/reorganizacion-modular/05-metodo-contratos-y-tdd.md).**
> Esta skill es el **procedimiento** para cumplirla con un fichero. Si algo de aquí choca con
> `05`, manda `05`, y esta skill está desactualizada: corrígela en el mismo commit.

La reorganización **no mueve ficheros: los crea**. Cada fichero nuevo nace con su contrato y un
test que lo cubre, **antes** que la lógica. El objetivo: que cuando llegue la lógica de verdad, ya
exista lo que la valida.

## Antes de escribir nada

1. **Localiza la referencia.** La tabla de `04-estructura-final.md` §4 dice qué paquete viejo
   corresponde al nuevo. El fichero viejo es la **implementación de referencia**: se lee, **no se
   edita** (E-1).
2. **Lee los tests viejos de ese paquete** (E-8). No se portan: se leen para no olvidar reglas,
   casos borde e incidentes. Toda regla que el código nuevo deba seguir cumpliendo **va al
   comentario del contrato**. Si decides no mantener una, dilo en el commit con el motivo.
3. **¿Toca un candado de invariante?** Mira `05` §3.2. Si el fichero es dueño de una de esas
   reglas (INV-1, I-CP-5, el literal del aviso pasivo…), su contrato la escribe y su test la prueba.

## Paso 1 · El contrato (sin lógica)

El fichero nuevo lleva **solo**:

- comentario de paquete (si es el fichero principal) y cabecera de origen:
  `// Porta internal/<ruta vieja>/<fichero>.go @ <sha corto de dev>` (E-10);
- tipos, interfaces, constantes, errores centinela, firmas;
- un comentario por símbolo exportado que diga **qué promete**: entradas, salidas, errores y en
  qué casos. **Ese comentario es la especificación**; si es vago, el test será vago;
- cuerpos: `panic(pendiente.Implementar("paquete.Func"))`.

🔴 **Nunca un valor cero como cuerpo** (`return nil, nil`, `return ""`, `return false`): puede
hacer pasar un test por accidente. Solo `panic`.

🔴 **Nombres en inglés, comentarios en español (E-11).** Ficheros, paquetes, tipos, funciones, métodos,
campos, variables, constantes, centinelas y nombres de test van en **inglés**; solo los comentarios van en
español. Portando un símbolo viejo con nombre en español, el nuevo lleva uno en inglés y el comentario dice
cuál era. Lo ya escrito y lo ya decidido (módulos de D-5, `pendiente`, `Contrato`, `Montaje`) no se renombra.

🔴 **Los textos observables no cambian.** Un mensaje de error que hoy ve un humano (el BFF y
`wapp-ctl` muestran en texto plano los de `/api/v1/signup`) se copia **literal** del fichero viejo.

## Paso 2 · El test (rojo)

`x_test.go`, en el mismo directorio, primera línea `//go:build pendiente`:

- **Una aserción por promesa del comentario.** Se escribe leyendo el contrato, no el código viejo.
- **Todo símbolo exportado de `x.go` aparece en `x_test.go`**: lo exige el candado
  `exportados_cubiertos_test.go` (E-9), y se cumple ya en rojo.
- **Tabla de casos** cuando hay varias entradas; nombres de caso **en inglés** (E-11) y que digan la regla.
- **Sin BD, sin red, sin reloj real, sin `t.Skip`.** Postgres → ver «Puertos» abajo. Reloj →
  inyectado.
- **Sin leer código como texto** (AST), salvo que sea un candado de invariante de `05` §3.2.

Compruébalo **antes de commitear**:

```bash
GOWORK=off go vet -tags pendiente ./internal/modulos/<modulo>/<paquete>/          # compila
GOWORK=off go test -tags pendiente -run '^TestX$' ./internal/modulos/<modulo>/<paquete>/ ; echo "rc=$?"
```

El segundo **debe fallar** (rc≠0) por el `panic`. ⚠️ Un `panic` **aborta el binario entero** del
paquete: para ver un test concreto en rojo, córrelo solo con `-run`.

Commit: `rojo(<modulo>): contrato de <fichero>` — con el contrato **y** su test.

### Puertos (ficheros solo de interfaces) y adaptadores Postgres

- **El puerto** lleva una **suite de contrato** exportada en un paquete `<paquete>test`
  (patrón `internal/gateway/fleet/fleettest` de hoy). Dos formas, según el puerto:
  - **puerto con BD** (el que tiene adaptador Postgres):
    `func Contrato(t *testing.T, nuevo func(t *testing.T) Montaje)` — **D-F1-1**, cerrada. El `Montaje` trae el puerto
    **y** lo que el puerto no deja ver: los tenants sembrados (la FK los exige) y, si hace falta, un observador de estado.
    Referencia: `internal/nucleo/contact/contacttest/contrato.go`;
  - **puerto sin BD**: `func Contrato(t *testing.T, nuevo func() Puerto)`, la forma de `05` E-3.
- **Toda implementación** ejecuta esa suite desde su propio test. La implementación **en memoria**
  la ejecuta ya en unitario. Si el paquete no tiene gemelo en memoria (`05` E-6 lista los 12 que
  no lo tienen), **créalo** en `<paquete>test` en esta misma pasada.
- **El adaptador Postgres** prueba en unitario solo lo que no necesita BD: constructor,
  validación de argumentos, mapeo de filas y errores (extrae funciones puras para eso). Su SQL lo
  cubren los procesos de F9. Queda fuera del umbral de cobertura.

## Paso 3 · Verde

1. Sustituye cada `panic` por la lógica del fichero viejo, **con sus comentarios del porqué**
   (el estilo *comentario-como-ADR* de la casa no se pierde).
2. Quita `//go:build pendiente` del test.
3. Comprueba:

```bash
GOWORK=off go test -race ./internal/modulos/<modulo>/<paquete>/ ; echo "rc=$?"
make cobertura-ficheros   # ≥ 80 % por fichero (D-12); fuera los adaptadores Postgres
```

Commit: `verde(<modulo>): <fichero>`. Un fichero por commit. Si luego limpias con los tests en
verde: `refactor(<modulo>): …`.

> Si `make cobertura-ficheros` o `make test-pendiente` no existen, estás antes de F0 o F0 no está
> completo: esos targets nacen allí (`05` §6). No los sustituyas por un comando improvisado sin
> dejarlo escrito.

## Antipatrones — si te ves haciendo esto, para

- Copiar el fichero viejo entero y «ya luego» escribir el test. Eso es mover, no reconstruir.
- Portar un test viejo tal cual. Se consultan; el nuevo sale del contrato.
- Un test que solo comprueba que «no hace panic» o que la función existe.
- Un nombre en español en un fichero, tipo, función, variable o test **nuevo** (E-11).
- `t.Skip` por cualquier motivo. Es la deuda DT-52: un SKIP bajo `rc=0` parece verde.
- Editar el paquete viejo «de paso». El viejo es lo que corre en UAT y el oráculo.
- Declarar verde con `| tail` delante del `go test`: el `rc` sería el de `tail`.

## Relacionadas

- `reconstruir-modulo` — orquesta todos los ficheros de un módulo.
- `validar-antes-de-cerrar` — los gates antes de decir «listo».
- `procesos-testcontainers` — la integración, que **no** va por fichero.
