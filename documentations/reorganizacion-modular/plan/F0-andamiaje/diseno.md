# F0 · Andamiaje — diseño (la vista micro)

> Por paquete: sus ficheros y el **contrato** de cada uno. Los comentarios de contrato de aquí son
> la especificación de la que sale cada test (`05` E-4). Donde F0 no reconstruye sino **copia**
> (el arranque), se dice y se justifica (D-F0-1).

## 1 · Qué crea F0

| Ruta | Tipo | Ficheros de producción | Test | Nace |
|---|---|---|---|---|
| `internal/pendiente/` | paquete de una función | `pendiente.go` | `pendiente_test.go` | rojo→verde (T0.2–T0.3) |
| `internal/candados/` | lógica de los candados, con árboles de prueba | 6 (§4) | 6 + `testdata/` | rojo→verde (T0.5–T0.6) |
| `internal/modulos/` | raíz del árbol de módulos | `doc.go` | 3 candados (§4) | T0.7 |
| `test/procesos/` | futuro paquete de F9 | `doc.go` | `sin_bd_viva_test.go` | T0.8 |
| `cmd/cobertura-ficheros/` | herramienta del gate | `main.go` | — (fuera del alcance de los candados: `cmd/`) | T0.9 |
| `internal/arranque/` | copia del arranque viejo | 21 (`arquitectura.md` §2) | 19 copiados + `huella_test.go` | copia (T0.10) |
| `internal/arranque/huellatest/` | ayudante de la huella (patrón `fleettest`) | `huellatest.go` | `huellatest_test.go` | rojo→verde (T0.12–T0.13) |
| `internal/arranque/testdata/huella.json` | la dorada | — | — | T0.14 |
| `internal/bootstrap/arranque/huella_vieja_test.go` | test en el paquete **viejo** (D-F0-2) | — | él mismo | T0.14 |
| `internal/apipublica/` | cara HTTP nueva, vacía | `apipublica.go` | `apipublica_test.go` | T0.16 |
| `cmd/server-modular/` | segundo binario | `main.go` | — | T0.11 |

Prefijo de commit de F0: `andamiaje(f0): …`, y para los ficheros con ciclo TDD
`andamiaje(f0): rojo — …` / `andamiaje(f0): verde — …`, en commits **distintos** (E-4).

## 2 · `internal/pendiente/pendiente.go`

```go
// Package pendiente marca un contrato sin lógica (05 E-2). Es un paquete de UNA función a
// propósito: todo cuerpo de contrato es `panic(pendiente.Implementar("paquete.Símbolo"))`, y
// `make test-pendiente` cuenta esas llamadas para decir cuánto falta.
package pendiente

// Implementar devuelve el error con el que entra en pánico un cuerpo de contrato.
// El mensaje contiene `simbolo` literal y el prefijo "pendiente: ", para que un test en rojo
// diga QUÉ falta sin leer la traza. Un `simbolo` vacío produce "pendiente: (símbolo sin
// nombre)", nunca un mensaje vacío. Dos llamadas con el mismo símbolo dan mensajes iguales.
// No tiene estado ni efectos: no loguea, no cuenta, no mira el entorno.
func Implementar(simbolo string) error
```

Test (una aserción por promesa): el prefijo; el símbolo literal; el caso vacío; la igualdad de
mensajes; y un `recover()` sobre `panic(Implementar("x.Y"))` que recupera un `error` cuyo texto
contiene `x.Y`. 🔴 El rojo (T0.2) **no** puede usar `pendiente.Implementar` en su propio cuerpo:
lleva `panic("pendiente: sin implementar")`.

## 3 · Los `make` (T0.4, T0.9)

| Target | Qué hace | `rc` |
|---|---|---|
| `vet-pendiente` | `$(GO) vet -tags pendiente ./...` | el de `vet`: un rojo que no compila rompe el gate (`05` §5) |
| `test-pendiente` | (1) `PENDIENTES=` nº de llamadas `pendiente.Implementar(` en `.go` **no** de test, fuera de `internal/pendiente/`, sin contar líneas cuyo primer texto sea `//`; (2) `ROJOS=` nº de `_test.go` cuya primera línea es `//go:build pendiente`; (3) corre `go test -tags pendiente` sobre `internal/modulos/... internal/nucleo/... internal/arranque/...` **sin** que su fallo rompa el target: el rojo es esperado y un `panic` aborta el binario de test del paquete entero, así que la cifra que manda es la estática, no los FAIL (`05` E-5; `../00-marco/tecnologia.md`); (4) `vet-pendiente` | `0` salvo que falle el `vet`. Informa, no juzga |
| `cobertura-ficheros` | `$(GO) test -covermode=set -coverprofile=$(TMP)/cobertura.out` sobre `./internal/modulos/... ./internal/nucleo/... ./internal/apipublica/... ./internal/pendiente/... ./internal/candados/... ./internal/arranque/huellatest/...` y `go run ./cmd/cobertura-ficheros -perfil … -umbral 80` | `1` si algún fichero **en verde** queda por debajo |
| `ci-local` | `fmt-check vet vet-pendiente lint test cobertura-ficheros build` | el de su primer fallo |

Los paquetes del alcance que aún no existen se omiten con `go list` (el target no falla por un
módulo que todavía no nació). `lint` **no** mira los ficheros con etiqueta `pendiente`
(`.golangci.yml` no declara `build-tags`): son contratos; `vet-pendiente` los compila y basta.

## 4 · `internal/candados/` — la lógica de los candados, probada contra árboles que muerden

Los tests de los candados que corren sobre el árbol real (`internal/modulos/*_test.go`,
`test/procesos/sin_bd_viva_test.go`, `make cobertura-ficheros`) son **finos**: llaman a una
función de `candados` sobre el árbol real y exigen cero violaciones. La lógica vive aquí para
poder demostrar que **muerde** con árboles de prueba en `testdata/<candado>/muerde/` (debe dar
≥ 1 violación nombrando fichero y motivo) y `…/pasa/` (debe dar 0). Leer código como texto está
permitido **solo** aquí y en la huella: son candados (`05` E-7).

| Fichero | Exportados (contrato) |
|---|---|
| `candados.go` | `type Violacion struct{ Fichero, Motivo string }` (con `String()`), `func Recorrer(raiz string, dirs []string, incluirTests bool) ([]Fuente, error)` — devuelve los `.go` parseados (`go/parser`, con comentarios) **ignorando `testdata/`** y, si `dirs` no existe, cero ficheros sin error; `type Fuente` (ruta, `*ast.File`, `EsTest`) |
| `fronteras.go` | `type Reglas`, `type Puente`, `func Fronteras(modulo string, fuentes []Fuente, r Reglas) []Violacion` (§4.1) |
| `unfichero.go` | `func UnFicheroUnTest(fuentes []Fuente) []Violacion` (§4.2) |
| `exportados.go` | `func ExportadosCubiertos(fuentes []Fuente) []Violacion` (§4.3) |
| `sinbdviva.go` | `func SinBDViva(fuentes []Fuente) []Violacion` (§4.4) |
| `cobertura.go` | `type Fichero struct{ Ruta string; Sentencias, Cubiertas int }`, `func Agregar(perfil io.Reader) (map[string]Fichero, error)`, `func Cobertura(fs map[string]Fichero, fuentes []Fuente, umbral float64) []Violacion` (§4.5) |

**Alcance** (qué directorios recorre cada candado del árbol real): `internal/modulos`,
`internal/nucleo`, `internal/apipublica`, `internal/pendiente`, `internal/candados` (sin
`testdata`), `internal/arranque/huellatest`. **`internal/arranque` queda fuera** de
`un_fichero_un_test` y de la cobertura por fichero (D-F0-1: su test es el paquete), y **dentro**
de `exportados_cubiertos` (dos exportados: `Ejecutar` debe aparecer en `orquestador_test.go` y
`EnrollServerCreds` en `pki_test.go`, que ya lo hace) y de `fronteras`.

### 4.1 · `fronteras` — el formato de datos

La tabla vive **en Go**, en `internal/modulos/fronteras_test.go` (tipada, diffable, sin parser
propio):

```go
var reglas = candados.Reglas{
	// Módulo nuevo → módulos nuevos que PUEDE importar (además de platform, nucleo y pendiente,
	// que todo el árbol nuevo puede importar). Lista blanca de HOY congelada (04 §3): las aristas
	// módulo→módulo medidas con el script de 02 §5 y el MAP de D-5 sobre dev @ <sha>, <fecha>.
	Capas: map[string][]string{
		"acceso":       {},
		"edge":         {"acceso"},
		"inferencia":   {"edge"},                        // llmvia → gateway (02 §2)
		"catalogo":     {"conversacion"},                // → model (04 §5.2)
		"solicitudes":  {"conversacion"},                // telemetria → store (ciclo 2)
		"captacion":    {"conversacion", "solicitudes", "catalogo", "inferencia"},
		"conversacion": {"captacion", "solicitudes", "edge", "acceso", "inferencia"},
		// … la cifra buena la pone T0.7 midiendo; esto es la forma, no el dato.
	},
	// Viejo → módulo (04 §4, gana el prefijo más largo). Sirve para dos cosas: saber a qué
	// módulo pertenece el destino de un puente, y prohibir que internal/arranque cablee lo
	// viejo de un módulo YA conmutado.
	Mapa: map[string]string{"internal/iam": "acceso", "internal/platformadmin": "acceso", /* … */},
	// Módulos conmutados (el arranque nuevo ya los cablea): lo marca el commit conmutar(<m>).
	Conmutados: []string{},
	// Puentes: import de un paquete NUEVO a uno VIEJO, declarado (05 §4.1). Vacía en F0.
	Puentes: []candados.Puente{
		// {Desde: "internal/modulos/captacion/pipeline", Hacia: "internal/flujos/store",
		//  Motivo: "FlowEvent/SaveStage hasta que F8 reconstruya el almacén", Nace: "F7", Muere: "F8"},
	},
}
```

Qué hace fallar el gate (cada línea tiene su caso en `testdata/fronteras/muerde/`):

1. un paquete de `internal/modulos/<m>` que importa `internal/modulos/<n>` con `n` fuera de
   `Capas[m]`;
2. un paquete del árbol nuevo que importa un paquete **viejo** de `internal/` (ni `platform`, ni
   `nucleo`, ni `pendiente`, ni el propio árbol) sin un `Puente` que lo declare — **con
   `internal/arranque` como única excepción**: es el composition root y en F0 cablea lo viejo;
3. `internal/arranque` que importa un paquete viejo cuyo módulo (`Mapa`) está en `Conmutados`;
4. `internal/apipublica` que importa **cualquier** paquete viejo (la cara nueva solo habla con
   lo nuevo; el `publicapi` viejo lo monta el arranque, no ella). Con las recomendaciones de
   `DECISIONES.md` (D-FX-1/D-F7-4: intenciones en F7; D-F3-2: centinela vía `platform`) **no hace
   falta ningún puente** desde `apipublica`, así que la regla se queda así. **Vía de excepción**
   (`05` §4.1 sí admite puentes declarados): si Jhoan eligiera una alternativa que lo exija, la
   regla 4 pasa a admitir un `Puente` con `Desde: "internal/apipublica/…"`, con las mismas
   obligaciones que los demás (regla 6: `Muere` y uso real), en el mismo commit que lo declara;
5. un fichero de producción **viejo** que importa algo del árbol nuevo; en tests viejos, solo
   `internal/bootstrap/arranque/huella_vieja_test.go` → `internal/arranque/huellatest`;
6. un `Puente` declarado que ya ningún fichero usa (un puente muerto se borra) o cuyo `Muere`
   es una fase ya cerrada.

Se recorren producción **y** tests del árbol nuevo (un test que importa lo viejo sería portar a
escondidas, E-8).

### 4.2 · `un_fichero_un_test`

Por cada `x.go` no de test del alcance, debe existir `x_test.go` en el mismo directorio. Las
excepciones de E-3 se **verifican**, no se listan:

| Excepción | Condición que el candado comprueba |
|---|---|
| `doc.go` | cero declaraciones, solo `package` y su comentario |
| fichero de `//go:embed` | todas sus declaraciones son `var` precedidas de `//go:embed` |
| fichero solo de interfaces (puerto) | todas sus declaraciones son `type X interface{…}`; y existe un paquete hermano `<paquete>helpertest`, en `<dir>/<paquete>helpertest`, con una `func Contrato(t *testing.T, …)` (D-F1-10, 2026-10-02; antes `<paquete>test`) |
| doble en un paquete `…helpertest` | su paquete termina en `helpertest` (D-F1-10; antes `test`) y no tiene ninguna `func` con cuerpo; si tiene lógica, necesita test |
| paquete `…helpertest` entero (**D-F1-3**, condicionada; estrechada por **D-F1-10**, 2026-10-02) | si D-F1-3 = sí (lo es desde el 2026-09-30): todo fichero de un paquete cuyo nombre termina en `helpertest` **con al menos un carácter delante** (suite `Contrato` y dobles) queda fuera de este candado **y** del de §4.3 (y, desde D-F1-6, de la cobertura por fichero de §4.5); los dobles con lógica llevan igualmente su test (lo exige la fase: F1 T1.4, F2 T2.2…). Si D-F1-3 = no, solo vale la fila anterior. `Recorrer` expone el nombre de paquete para que la exención sea **una** condición en `candados` (`candados.go`, `isHelperTestPackage`), no una lista. Hasta D-F1-10 la condición era «termina en `test`», que eximía también un paquete de producción `latest` o `contest` (README, contradicción 16): hoy un paquete que acaba en `test` sin acabar en `helpertest` (`huellatest`) y uno llamado `helpertest` a secas **no** están exentos |

### 4.3 · `exportados_cubiertos` — cómo se detecta «mencionado en su test»

Por cada `x.go` con `x_test.go`: el conjunto de exportados de `x.go` (funciones, tipos,
variables y constantes de primer nivel; y los métodos exportados de sus tipos) debe estar
contenido en el conjunto de **identificadores** de `x_test.go` (`*ast.Ident` sueltos en el mismo
paquete, o `Sel` de un `*ast.SelectorExpr` en el paquete externo `…_test`). `go/parser` ignora
las etiquetas de compilación: un test en rojo **cuenta** (`05` E-9, «se cumple ya en rojo»). No
se exige mencionar campos de struct. Un comentario **no** es una mención. Con **D-F1-3** = sí, los
paquetes `…helpertest` quedan fuera (la misma condición que §4.2, D-F1-10).

### 4.4 · `sin_bd_viva`

Sobre **todos** los `.go` de `test/procesos/` (con o sin etiqueta `integracion`), salvo él mismo,
falla si aparece: el literal `WAPP_TEST_DB_DSN`; un literal que contenga `:5432`,
`localhost:5432` o `127.0.0.1:5432`; el identificador `WithReuseByName`; un literal que empiece
por `postgres://` o `postgresql://` (la única cadena válida es la de
`ctr.ConnectionString(ctx, "sslmode=disable")`, `05` §7.2).

### 4.5 · `cobertura-ficheros` — agregación, umbral y exentos

- **Agregación**: cada línea del perfil `-coverprofile` es
  `fichero:l1.c1,l2.c2 sentencias cuenta`; por fichero, `Sentencias` = Σ sentencias y
  `Cubiertas` = Σ sentencias de los bloques con `cuenta > 0`. Un bloque repetido (dos paquetes
  de test sobre el mismo fichero) se cuenta **una vez**, con la cuenta máxima.
- **Umbral**: `100·Cubiertas/Sentencias ≥ 80` (D-12), solo para ficheros **en verde**: un
  fichero con alguna llamada `pendiente.Implementar` sigue en rojo y no se evalúa.
- **Exentos: los adaptadores Postgres** (`05` E-6), marcados **en el propio fichero** con la
  línea `// cobertura: adaptador postgres (05 E-6)` en la cabecera. El candado **verifica** la
  marca: un fichero marcado que no importa `database/sql` ni `github.com/jackc/pgx/…` es una
  violación (nadie se exime por decreto). Sin marca, un adaptador Postgres se evalúa como
  cualquiera. La tabla que imprime el comando cuenta aparte `EXENTOS_POSTGRES=N`.

## 5 · `internal/arranque/` — la copia (D-F0-1)

### 5.1 · Por qué la copia no pasa por contrato → rojo → verde

El arranque **no tiene lógica de dominio que especificar**: es cableado, y su verdad es
**la huella** y los candados AST que ya lo vigilan. Escribir 21 contratos con `panic` para una
copia que debe salir **idéntica** produciría un rojo que no especifica nada nuevo y un verde que
es `cp`. Por eso F0 lo **copia** (commit `andamiaje(f0)`) con sus guardas, y la reconstrucción
del arranque ocurre **por módulo**, en cada `conmutar(<m>)`, donde sí cambia algo. No lo prevé
`05`: es la decisión **D-F0-1**.

### 5.2 · Los tests que se copian, y por qué

| Tests viejos (`internal/bootstrap/arranque/`) | Qué guardan | En F0 |
|---|---|---|
| `astpaquete_test.go` (ayudante: recorre el **paquete**, no un fichero) | — | se copia |
| `calentamiento_cableado_test.go` (3) · `flow_options_cableadas_test.go` (1) · `invitaciones_cableado_test.go` (2) · `pipeline_captacion_cableado_test.go` (1, I-CP-4: W=1) · `quotetext_cableado_test.go` (1) · `reanalisis_cableado_test.go` (1) · `roleplane_cableado_test.go` (2) · `send_budget_cableado_test.go` (2) · `turno_acotado_cableado_test.go` (1) | los **9** candados de cableado (`05` §3.2: «Contrato del arranque nuevo») | se copian; dos rutas relativas cambian (`../../publicapi/` → `../publicapi/`) |
| `platform_permissions_test.go` (2) | 🔴 I-CP-5: todo permiso de ruta de plataforma acaba en `.any`; detecta por el texto `"platformadmin."` en el argumento de `adminHandler(…)` (llamadas inline en `rutas_admin.go:76-91`; el porqué, en el comentario de `:19-28` sobre `type adminRouteDeps` `:29`) | se copia; sigue funcionando tras F2 porque el paquete nuevo conserva el nombre `platformadmin` (D-4) |
| `orquestador_test.go` (4) · `mux_registration_test.go` (1) · `es256_key_test.go` (11) · `lease_test.go` (4) · `pki_test.go` (3) · `delegated_auth_test.go` (4) · `identity_verifier_test.go` (2) · `filters_config_test.go` (8) | las funciones puras del arranque | se copian; `orquestador_test.go` añade una mención de `Ejecutar` (exportados cubiertos) |
| `pool_metrics_integration_test.go` (1) | el pool hasta `/metrics`, contra Postgres | **no** se copia: `t.Skipf` sin `WAPP_TEST_DB_DSN` (E-5). Su regla («las seis `wapp_db_*` salen del pool configurado») pasa a un proceso de F9 |

Cifras: `grep -c '^func Test'` por fichero (2026-09-28). ⚠️ `05` §3.2 dice «los **11**
`*_cableado_test.go`»: son **9** ficheros con `cablead` en el nombre; **11** es el número de
ficheros de test que leen AST (los 9 + `platform_permissions_test.go` + `astpaquete_test.go`).

### 5.3 · `cmd/server-modular`

Solo `main.go`. `04` §2.1 proponía copiar también `cmd/server/integration_test.go`; **no se
copia**: ese test (468 l, `TestInProcessEnrollConnectLeaseSendRecv`) compone **paquetes** en
proceso y solo usa del arranque `bootstrap.EnrollServerCreds`; no ejerce el arranque nuevo y
portarlo contradice E-8. El oráculo del binario nuevo contra el viejo es F9.

## 6 · La huella (`internal/arranque/huella_test.go` y su gemelo viejo)

### 6.1 · Componentes

| Componente | Fuente | Se calcula con |
|---|---|---|
| `rutas[listener]` | `c.httpSrv.Handler` (`:8100`) y `c.publicSrv.Handler` (`:8103`) tras la fase 8 real | sonda por patrón candidato: `httptest` con el método y la ruta del patrón (`{x}` → `x`), `RemoteAddr` fijo; **montada** si la respuesta no es el 404 del mux (`404` + cuerpo `404 page not found\n`) ni un `405` con cabecera `Allow`; un `panic` del handler cuenta como montada |
| `rpc[listener]` | `c.enrollGS`, `c.connectGS` | `GetServiceInfo()` → `servicio/método` |
| `metricas` | `GET /metrics` por `c.httpSrv.Handler` **después** de las sondas | nombres de `# TYPE wapp_…` |
| `perfil` | dos configuraciones: **mínima** (sin identity: `signup` y `exchange` a 503) y **con M2M** (`authStk.m2mClient` = doble) | la huella lleva las dos |
| `goroutines` (estática) | ficheros de producción de cada arranque | cada sentencia `go`, normalizada a `<paquete>.<Tipo>.<Método>` del receptor (`go/types`, importador `source`) o al nombre de la función; multiconjunto |
| `hooks` (estática) | ídem | multiconjunto de selectores `X.M` con `M` en el conjunto de métodos de `*metrics.Metrics` (obtenido por `reflect` en el test, no escrito a mano) |
| `entorno` (estática) | todo el árbol nuevo | llamadas a `os.Getenv`/`os.LookupEnv`/`os.Environ` fuera de `internal/platform/config` → debe ser **∅** |

Candidatos de ruta: los literales de `Handle`/`HandleFunc` de **los dos** árboles (viejo:
`internal/` entero; nuevo: `internal/arranque`, `internal/apipublica`, `internal/modulos`) más
los de la dorada. Una ruta nueva de más aparece como candidata y se sondea en los dos lados:
así se detecta **también lo que sobra**, no solo lo que falta.

### 6.2 · `huellatest` (contrato)

`type Huella struct{ Perfil string; Rutas, RPC map[string][]string; Metricas, Goroutines, Hooks, Entorno []string }` ·
`Candidatos(raiz string, dirs ...string) ([]string, error)` · `Rutas(h http.Handler, candidatos []string) []string` ·
`RPC(gs *grpc.Server) []string` · `Metricas(admin http.Handler) ([]string, error)` ·
`Goroutines(dir string) ([]string, error)` · `Hooks(dir string, metodos []string) ([]string, error)` ·
`Entorno(raiz string, dirs ...string) ([]string, error)` · `Leer(ruta string) ([]Huella, error)` ·
`Escribir(ruta string, hs []Huella) error` · `Diferencia(quiere, tiene Huella) []string` — una
línea legible por diferencia (`:8100 falta GET /admin/tenants`, `goroutines sobra
pipeline.Worker.Run`), vacía si son iguales. Todo determinista (listas ordenadas).

### 6.3 · El contenedor de huella (idéntico en los dos lados)

| Fase | En la huella | Por qué |
|---|---|---|
| prólogo | `config.Load()` con el entorno del test + listeners en `127.0.0.1:0` y `RateLimit` altísimo | sin puertos fijos; sin 429 en las sondas |
| 1 | **simulada**: `*sql.DB` perezoso (`sql.Open("pgx", …127.0.0.1:1…)`, nunca conecta), `metrics.New()` + `RegisterDBStats`, CA de desarrollo en memoria (`enroll.NewDevCA` + `IssueServerCert`), `buildLeaseManager` y `buildEnrollServer` **reales** | `setupDatabase` exige Postgres |
| 2 | **real** | sin red con identity apagado |
| 3 | **simulado** solo el grupo `flowDeps` (keyring de prueba `crypto.NewEnvKeyProvider`, presign nulo, resolver real); los 16 objetos de salida con sus constructores reales sobre la BD perezosa — medir en T0.14 si basta con dejarlos a `nil` tipado | `flows.go:75` hace `HeadBucket` (red) |
| 4–8 | **reales** (`f.ejecutar`), comprobando `requiere()` | es lo que se compara |
| 9 | **no se ejecuta**: sus `go` los cubre la parte estática | sus `Run` tocarían la BD |

Medido en un prototipo (2026-09-28, copia de `dev` en un directorio temporal, no commiteado):
todo el contenedor se arma y se sondea en **0,09 s**, sin red. Los dos lados escriben **el mismo**
constructor de contenedor (el tipo es privado a cada paquete, no se puede compartir): una
diferencia entre las dos copias del constructor es un defecto de la huella, y la §7 del
traspaso lo pide revisar.

### 6.4 · La dorada

`internal/arranque/testdata/huella.json`: una `Huella` por perfil, listas ordenadas, sin
fechas ni SHA (para que el diff sea el cambio). **Solo** `huella_vieja_test.go -args
-actualizar` la escribe. En cada commit que la cambie se dice por qué.

### 6.5 · Lo que la huella NO compara, y quién lo cubre

| No compara | Por qué | Lo cubre |
|---|---|---|
| **Qué** handler sirve una ruta (nuevo o viejo) | la sonda ve el patrón, no el código | el diseño de `FX-cara-http` |
| El comportamiento de cada handler (códigos más allá de «montada») | no es superficie | los contratos de cada módulo y los procesos de F9 |
| Las fases 1 y 3 **reales** (migraciones, pool, KMS, R2) | exigen red | la parte estática (`hooks` ve `RegisterDBStats`), T0.23 en local y F9 |
| Los `CounterVec` sin muestras | no aparecen en frío en ningún lado | se declaran en `platform/metrics`, **compartido**: iguales por construcción; su cableado, `hooks` |
| Las goroutines que lanza el código de dominio en caliente | nacen por petición | los contratos de `edge`, `conversacion`, `captacion` |
| Parámetros de `grpc.Server` (keepalive, credenciales) y `http.Server` (plazos) | no están en `GetServiceInfo` ni en la sonda | la copia (F0) y los procesos de F9 |
| El orden de la lista `fases` y los `requiere()` | no es superficie | `orquestador_test.go` copiado |

## 7 · Los tres ✎ de `platform`

Regla común: el tipo o el centinela pasa a **declararse** en `platform` con su comentario
entero, y el paquete de dominio viejo lo **re-exporta** con una línea (alias de tipo o
`var X = platform.X`). Resultado: el mismo tipo para lo viejo y lo nuevo, ni una línea del
arranque viejo cambia, y ningún texto observable cambia (`"sesión offline"`, los seis campos de
auditoría, las cinco series `wapp_edge_*`). El nombre exacto del paquete de `platform` que los
recibe lo elige la tarea con dos condiciones: que `go build ./...` no forme ciclo, y que no sea
un paquete HTTP si el consumidor no es HTTP (el `Agregado` va en `platform/metrics`, que es su
consumidor). Cada ✎ se verifica con `go list -f '{{join .Imports "\n"}}' ./internal/platform/...`.

## 8 · `internal/apipublica` en F0

Contrato mínimo (el API exacto lo fija `../FX-cara-http/diseno.md`): el comentario de paquete
dice que es la cara `/api/v1` **nueva**, que crece por olas (D-10) y que lo que no registra cae
al `publicapi` viejo; el símbolo de montaje **no registra nada** en F0. Su test afirma que, con
la cara vacía, toda petición llega al handler de *fallback* sin tocar (código, cuerpo y
cabeceras del *fallback*).

## 9 · Tests viejos que hay que leer (E-8) y candados de invariante que aterrizan en F0

| Leer | Para |
|---|---|
| `internal/bootstrap/arranque/*_test.go` (20) | T0.10: qué se copia (§5.2) |
| `internal/platform/httpapi/*_test.go` (9 según `04` §3) | T0.17–T0.18: qué fija hoy el 502 de sesión offline y la auditoría |
| `internal/platform/metrics/inferstats_test.go`, `metrics_test.go` (`:48-49`: `wapp_auth_logins_total` no debe volver) | T0.19 |
| `internal/gateway/session/*_test.go`, `internal/inferstats/*_test.go`, tests de `internal/iam/usecase` que usan `AuditInput` | T0.17–T0.19: que el alias no cambia nada |

Candados de invariante (`05` §3.2) que F0 deja expresados: **I-CP-5** (`platform_permissions_test`
copiado, con su caso negativo `TestINV056_1_DetectsUnguardedPlatformRoute`), **I-CP-4**
(`pipeline_captacion_cableado_test` copiado) y los otros **8** de cableado. El resto de filas de
`05` §3.2 aterriza en los contratos de sus módulos (F2–F8).
