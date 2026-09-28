# FX · Requisitos — historias y criterios EARS

> Cada criterio dice con qué se verifica. «La cara nueva» = `internal/apipublica`; «la cara vieja»
> = `internal/publicapi` montada por el arranque nuevo; «el compuesto» = el handler que devuelve
> `apipublica.Componer`. Filas del mapa: [`mapa-de-rutas.md`](mapa-de-rutas.md).

## HX.1 · El estrangulador

> **HX.1** · Como **la dueña del negocio**, quiero que la consola siga funcionando igual mientras la
> API se reconstruye por partes, para **no notar la obra**.

- **RX.1.a** · **CUANDO** una petición casa con un patrón de la cara nueva, **EL** compuesto
  **DEBERÁ** servirla con la cara nueva y no invocar la vieja. — Verifica:
  `TestComponer_GanaLaNueva` (`apipublica/estrangulador_test.go`).
- **RX.1.b** · **CUANDO** una petición no casa con ningún patrón de la cara nueva y sí con uno de
  la vieja, **EL** compuesto **DEBERÁ** delegarla en la vieja con **el mismo** `*http.Request`
  (sin copia), para que `r.Pattern` llegue a la métrica. — Verifica: `TestComponer_DelegaEnLaVieja`
  y `TestComponer_MismaPeticion`.
- **RX.1.c** · **SI** ninguna cara casa método + ruta pero una de ellas conoce la ruta con otro
  método, **ENTONCES EL** compuesto **DEBERÁ** responder `405` con la cabecera `Allow` de **esa**
  cara; y `404` si ninguna la conoce. — Verifica: `TestComponer_405DeLaCaraQueConoce`,
  `TestComponer_404`.
- **RX.1.d** · **EL** compuesto **NO DEBERÁ** añadir middleware, cabeceras ni escritura propias. —
  Verifica: `TestComponer_NoEscribeNada` (un handler de cada cara que no escribe → respuesta
  idéntica a la del handler solo).

## HX.2 · Nada cambia hacia fuera

> **HX.2** · Como **el integrador CRM** y como **la operación de UAT**, quiero que cada ruta
> conserve su patrón, sus permisos, su auditoría y su métrica, para **no tener que cambiar ni un
> cliente ni un panel**.

- **RX.2.a** · **EL** arranque nuevo **DEBERÁ** exponer por `:8103` y `:8100` exactamente los 95
  patrones del arranque viejo, con el **mismo texto** (método, comodines con el mismo nombre). —
  Verifica: `internal/arranque/huella_test.go` (F0) resolviendo cada patrón por el compuesto.
- **RX.2.b** · **CUANDO** una ruta se sirve por la cara nueva, **SU** cadena **DEBERÁ** ser la
  misma que en la vieja: mismo tipo (W/R/A/—), mismo permiso, mismo recurso de auditoría, mismo gate
  de feature y en el mismo orden (Authenticate → RequirePermission → gate). — Verifica: el test de
  cada fichero de área (`401` sin token, `403` sin grant, `403` `feature_not_enabled` sin feature,
  un registro en el auditor doble solo en las W).
- **RX.2.c** · **EL** compuesto **DEBERÁ** quedar envuelto **una sola vez** por
  `mtx.InstrumentHTTP("public", …)` y `httpapi.PublicRateLimit` en el arranque nuevo, como hoy en
  `internal/bootstrap/arranque/http.go:193-195`. — Verifica: aserción de cableado en
  `internal/arranque` (una sola llamada a cada uno sobre el handler público).
- **RX.2.d** · **DONDE** una ruta tiene condición de montaje (mapa, «Monta si»), **LA** cara nueva
  **DEBERÁ** montarla con la misma condición, y **NO DEBERÁ** montarla si falta la dependencia (404
  de ruta inexistente). — Verifica: un caso «sin dependencia → 404» por grupo en el test del área.
- **RX.2.e** · **EL** `POST /api/v1/members` **DEBERÁ** montarse aunque falte el cliente M2M y
  responder `503`, nunca `404`. — Verifica: `TestRolePlane_AltaSinM2M_503` (F2).
- **RX.2.f** · **EL** `POST /api/v1/intakes/{id}/quote-suggestion` **DEBERÁ** extender su plazo de
  escritura a `pipeline.PlazoPorLlamadaSuelo + 12 s` con el envoltorio **por fuera** de la cadena. —
  Verifica: `TestQuoteSuggestion_PlazoPorFuera` (F6) y la aserción del arranque de que el plazo
  inyectado es el mismo que recibe `quotetext.ConPlazo`.
- **RX.2.g** · **EL** `POST /api/v1/integrations/callback` **NO DEBERÁ** pasar por Authenticate y
  **DEBERÁ** dejar línea de access-log. — Verifica: `TestCRMCallback_SinJWT_ConAccessLog` (F6).

## HX.3 · Las mudanzas siguen el mapa

> **HX.3** · Como **Jhoan**, quiero que cada fase mude exactamente sus rutas, para **poder auditar
> el avance con un test y no con una lectura**.

- **RX.3.a** · **MIENTRAS** la fase actual del arranque nuevo sea Fn, **LA** cara nueva **DEBERÁ**
  servir exactamente las filas del mapa con «Muda en» ≤ Fn, y la vieja el resto. — Verifica:
  `internal/arranque/mudanzas_test.go` (TX.4) contra `internal/arranque/testdata/mapa.tsv` y la constante
  `arranque.FaseActual`.
- **RX.3.b** · **SI** una fase muda un patrón con comodín mientras un literal que solapa con él
  sigue en la vieja, **ENTONCES EL** candado de mudanzas **DEBERÁ** fallar. — Verifica: caso
  `TestMudanzas_SolapeRoto` con un mapa de prueba.
- **RX.3.c** · **SI** una familia (mismo camino, varios métodos) queda partida entre las dos caras,
  **ENTONCES EL** candado **DEBERÁ** fallar. — Verifica: `TestMudanzas_FamiliaPartida`.
- **RX.3.d** · **CUANDO** cierre F8, **EL** arranque nuevo **NO DEBERÁ** construir la cara vieja. —
  Verifica: `grep -rn 'publicapi\.' internal/arranque` vacío.

## HX.4 · Un singleton, una instancia

> **HX.4** · Como **el Edge**, quiero que mi conexión viva sea la que usa cualquier ruta que me
> hable, para **que un envío no se pierda en un gateway sin conexiones**.

- **RX.4.a** · **EL** arranque nuevo **DEBERÁ** construir **un** gateway gRPC y **un** runtime del
  motor, y **toda** ruta de cualquiera de las dos caras que los use (directa o transitivamente)
  **DEBERÁ** recibir esa misma instancia. — Verifica: aserción de identidad de puntero en
  `internal/arranque` (TX.12), p. ej. el `Sender` del notificador viejo `==` el gw de la fase edge.
- **RX.4.b** · **SI** un puerto viejo exige un tipo del paquete viejo del singleton, **ENTONCES LA**
  ruta **DEBERÁ** mudarse con el singleton o el arranque **DEBERÁ** interponer un adaptador de
  tipos declarado; nunca una segunda instancia. — Verifica: revisión de TX.12 + `go vet`.
- **RX.4.c** · **MIENTRAS** una ruta vieja compare un error centinela del gateway, **EL** paquete
  nuevo **DEBERÁ** conservar la identidad del centinela (D-FX-3). — Verifica:
  `errors.Is(nuevo.ErrSessionOffline, viejo.ErrSessionOffline)` en un test de `internal/arranque`
  hasta F8.

## HX.5 · La cara nueva nace cubierta

> **HX.5** · Como **la sesión web**, quiero que cada fichero de `apipublica` nazca con su test y sin
> base de datos, para **poder cerrarlo entero en la nube**.

- **RX.5.a** · **EL** fichero `apipublica/x.go` **DEBERÁ** tener `x_test.go` en el mismo
  directorio y cubrir su contrato (E-3, E-9). — Verifica: `un_fichero_un_test_test.go` y
  `exportados_cubiertos_test.go` con alcance sobre `internal/apipublica`.
- **RX.5.b** · **EL** test de un handler **NO DEBERÁ** tocar Postgres: usa la `Cara`, el
  middleware real con tokens firmados y dobles en memoria de los puertos. — Verifica:
  `grep -rn 'WAPP_TEST_DB_DSN\|sql.Open' internal/apipublica --include='*_test.go'` vacío.
- **RX.5.c** · **SI** un test de `apipublica` llama a `t.Skip`, **ENTONCES EL** gate **DEBERÁ**
  fallar. — Verifica: `grep -rn 't.Skip' internal/apipublica` vacío.
- **RX.5.d** · **EL** fichero nuevo **DEBERÁ** decir en su cabecera de qué fichero viejo porta
  (`// Porta internal/publicapi/x.go @ <sha>`, E-10). — Verifica: `grep -L 'Porta internal/'
  internal/apipublica/*.go | grep -v _test` vacío (salvo `apipublica.go` y `estrangulador.go`, que
  no portan).

## HX.6 · Las fronteras de la cara

> **HX.6** · Como **Jhoan**, quiero que la cara nueva no dependa de código viejo sin decirlo, para
> **que el relevo no descubra imports olvidados**.

- **RX.6.a** · **EL** paquete `apipublica` **DEBERÁ** importar solo `platform`, `nucleo`, módulos
  nuevos y los puentes declarados en `internal/modulos/fronteras_test.go`. — Verifica: ese candado
  con `internal/apipublica` en su alcance.
- **RX.6.b** · **CUANDO** cierre F8, **LA** lista de puentes de `apipublica` **DEBERÁ** estar vacía.
  — Verifica: `fronteras_test.go`.
- **RX.6.c** · **CUANDO** cierre F10, **NO DEBERÁ** existir `internal/publicapi` ni
  `apipublica/estrangulador.go`. — Verifica: `ls` de los dos.
