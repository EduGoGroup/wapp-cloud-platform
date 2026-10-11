# FW · La receta: conversar con el Edge de UAT sin WhatsApp

> **Para qué**: mirar las filas 1 y 6 de la ventana de UAT ([`../F10-relevo/diseno.md`](../F10-relevo/diseno.md) §2.4:
> «un mensaje entra y se contesta» y «los textos no cambian») sin una sesión de WhatsApp. La usa **F10-03**.
> **Estado**: escrita y **ejecutada en local de principio a fin** por FW-04 el 2026-10-11 (§5, con sus salidas).
> 🔴 **Contra UAT no se ha ejecutado**: lo que solo se sabrá allí está en §6.
> Cumple RW.5.a y RW.5.b de [`requisitos.md`](requisitos.md). La versión para operación, sin el contexto del relevo,
> es el runbook del ecosistema `operacion/runbooks/conversar-con-el-edge-sin-whatsapp.md` (raíz de wApp, fuera de este repo).

## 1 · Qué hace, en cuatro líneas

Se echan dos palancas en el Edge y se reinicia: deja de hablar con WhatsApp y usa un transporte falso (ADR-0049, fuera
de este repo). Se empareja una sesión falsa —sin QR—, se le hacen **entrar** mensajes por el inyector (ADR-0041) y se
**lee** lo que el Edge habría enviado. Todo lo demás es el sistema real: la cola cifrada del Edge, su lease, CloudLink
con mTLS y el Cloud entero. Al terminar se desvincula la sesión falsa y se bajan las palancas.

## 2 · Antes de empezar: cinco cosas que muerden

1. 🔴 **Con la palanca echada, el Edge no entrega nada a nadie, y toda sesión `active` que ya tuviera pasa a escuchar
   por el transporte falso**, también una de WhatsApp real: sale `listening`, late hacia la nube, y no está conectada.
   Su almacén cifrado no se toca y vuelve a su transporte al bajar la palanca. (Leído en el código del Edge,
   `whatsappfalso.NuevaEscucha` no mira el JID; **no ejecutado** con una sesión real.) Por eso el paso 1 lista las
   sesiones antes de tocar nada.
2. 🔴 **Lo sintético llega a la nube y se queda**: conversación, evento, solicitud. Es deliberado (ADR-0041). Se
   distingue por tres marcas: el `wa_message_id` de cada entrante empieza por `SINTETICO-`, el número propio de la
   sesión falsa por `0000`, y el del cliente es el que tú elijas (§3). Se hace con la **empresa de prueba** de UAT.
3. **Lo capturado vive en la memoria del Edge**: un reinicio lo borra. Se guarda en un fichero antes de reiniciar.
4. **La doble llave no tiene excepción de pruebas.** La DEK de la sesión falsa nace en el Edge por el emparejamiento de
   siempre y no sale de él; el lease lo emite y lo revoca el Cloud. Si el arranque del Edge no dice `lease_gate=true`,
   la prueba no mide el kill-switch.
5. **Ni un secreto en el acta**: el token de operador y las credenciales no se pegan. Los comandos de abajo los dejan
   en variables del shell.

## 3 · Datos que necesitas (los pones tú; aquí no van)

| Variable del shell | Qué es | De dónde sale |
|---|---|---|
| `SOCK` | El socket unix del plano de control del Edge | `WAPP_AGENT_CONTROL_SOCKET_PATH` de su `.env`; si no está puesta, `wapp-edge.sock` bajo el `data_dir`. Se confirma con `ss -xl \| grep wapp-edge` |
| `EDGE_ENV` | El `EnvironmentFile` de `wapp-edge` | `/root/source/wApp/cloud/wapp-edge-agent/.env` (documentación de despliegue de UAT del ecosistema) |
| `EDGE_LOG` | El log del Edge | `/root/source/wApp/logs/edge.log` (no pasa por journald) |
| `API` | La API pública del Cloud | `127.0.0.1:8103` |
| `CLIENTE` | Un número de cliente **inventado**, solo cifras, distinto en cada corrida | El que quieras; que no sea el de nadie |
| `PALABRA` | La palabra que abre el flujo que quieres ver | Un disparo `event_start` de la empresa de prueba: `SELECT keyword, flow_id FROM public.flow_triggers WHERE tenant_id = '<empresa>' AND kind LIKE 'event_%'` |

Quien opera es una persona **administradora de la empresa de prueba**: su token trae los permisos del plano de control
(`edge.sessions.pair`, `edge.diag.inbound.inject`). No hay un permiso aparte que conceder.

## 4 · La receta

Todo se corre **en la máquina del Edge**, como el usuario del servicio.

### Paso 0 · El binario del Edge tiene que traer el transporte falso

El transporte falso entró en `dev` de `wapp-edge-agent` con su PR #12 (`0675d55`). Un Edge anterior ignora la palanca
**sin avisar**: arranca con WhatsApp real. Se comprueba el proceso vivo, no el fichero:

```bash
pid=$(pgrep -f 'wapp-agent serve'); go version -m /proc/$pid/exe | grep -E 'vcs\.(revision|modified)'
git -C /root/source/wApp/cloud/wapp-edge-agent merge-base --is-ancestor 0675d55 <esa revisión>; echo "rc=$?"   # 0 = lo trae
```

Si no lo trae, se despliega el Edge de `dev` como dice la documentación de despliegue de UAT (dos binarios), **antes**
de la ventana.

### Paso 1 · Foto de antes (palanca baja): entrar como operador y apuntar las sesiones que hay

```bash
ctl() { m=$1; p=$2; shift 2; curl -sS --unix-socket "$SOCK" -X "$m" -w '\nHTTP %{http_code}\n' "$@" "http://unix$p"; }
read -r  -p 'correo: ' CORREO; read -rs -p 'clave: ' CLAVE; echo
TOK=$(curl -sS --unix-socket "$SOCK" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$CORREO\",\"password\":\"$CLAVE\"}" http://unix/v1/auth/login | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])')
unset CLAVE; A="Authorization: Bearer $TOK"
ctl GET /v1/sessions -H "$A"                        # 🔴 APÚNTALAS: son las que dejarán de hablar con WhatsApp (§2.1)
ctl GET /v1/health                                  # 200, status ok
ctl POST /v1/diag/inbound/inject -d '{}'            # 404: la ruta no existe
ctl GET '/v1/diag/outbound/captured?session_id=x'   # 404: la ruta no existe
grep -E '^WAPP_AGENT_(WHATSAPP_FALSO|INYECTOR_ENTRANTES|CLOUDLINK_LEASE_SHADOW_MODE)=' "$EDGE_ENV"   # nada
```

`503 relay_offline` en el login = el stream con la nube aún no está vivo: se reintenta a los segundos. Si alguna sesión
de la lista es de WhatsApp **real y viva**, se para aquí y se decide con Jhoan: la ventana la deja muda.

### Paso 2 · Echar las dos palancas y reiniciar

```bash
cp -p "$EDGE_ENV" "$EDGE_ENV.antes-receta"
printf 'WAPP_AGENT_WHATSAPP_FALSO=true\nWAPP_AGENT_INYECTOR_ENTRANTES=true\n' >> "$EDGE_ENV"
marca=$(wc -l < "$EDGE_LOG"); systemctl restart wapp-edge
```

**Las tres puertas** (un e2e contra el proceso equivocado parece verde):

```bash
tail -n +$((marca+1)) "$EDGE_LOG" | grep -c 'PALANCA ECHADA (WAPP_AGENT_WHATSAPP_FALSO=true)'   # 1
tail -n +$((marca+1)) "$EDGE_LOG" | grep 'CloudLink habilitado'    # lease_gate=true sealed_transit=true lease_shadow_mode=false
ctl GET /v1/health                                                  # 200; apunta el uptime_s: es el proceso recién arrancado
```

### Paso 3 · Mirar otra vez las sesiones

```bash
ctl GET /v1/sessions -H "$A"       # las mismas del paso 1. 🔴 Con la palanca echada NO hablan con WhatsApp (§2.1)
```

El token del paso 1 sigue valiendo tras el reinicio (medido en local, a través de tres reinicios). `403
degraded_read_only` = el Edge aún no tiene el JWKS de la nube: se reintenta.

### Paso 4 · Emparejar la sesión falsa y activarla

```bash
ctl POST /v1/sessions/pair -H "$A"                  # {"id":"<emparejamiento>","status":"success","qr":""}
ctl GET /v1/sessions/<emparejamiento>/pair -H "$A"  # status success, session_id, jid 0000…@s.whatsapp.net
SID=<ese session_id>
```

`403 degraded_read_only` = el Edge aún no tiene el JWKS de la nube: se reintenta. La sesión nace **pasiva** y la nube le
manda el aviso de sesión pasiva a su propio número: es el primer capturado. Se activa **en la nube**, con el token de
la administradora en la API pública (o desde la pantalla de sesiones de la consola del cliente):

```bash
curl -sS -X POST -H "Authorization: Bearer <token de la administradora>" -H 'Content-Type: application/json' \
  -d '{"profile":"active"}' "http://$API/api/v1/sessions/$SID/profile"      # {"session_id":"…","profile":"active"}
```

### Paso 5 · Conversar

Un mensaje por llamada. `lote` tiene que ser **distinto en cada mensaje** (es el `wa_message_id`: uno repetido se
descarta en silencio), y entre mensajes se dejan **unos 2 s**: el servidor frena las ráfagas del mismo cliente.

```bash
decir() { ctl POST /v1/diag/inbound/inject -H "$A" -H 'Content-Type: application/json' \
  -d "{\"session_id\":\"$SID\",\"texto\":\"$2\",\"chat_jid\":\"$CLIENTE\",\"lote\":\"$1\"}"; sleep 2; }
decir VENTANA-1 "$PALABRA"        # {"inyectados":1,"acusados":1,"errores":0,…}  HTTP 200
decir VENTANA-2 "1"               # …lo que pida el flujo
```

`409` = la escucha aún no está publicada: se reintenta. Cualquier otra cosa que no sea `inyectados:1, acusados:1` es un fallo.

### Paso 6 · Leer lo que el Edge «envió», y guardarlo

```bash
curl -sS --unix-socket "$SOCK" -H "$A" "http://unix/v1/diag/outbound/captured?session_id=$SID" > /root/capturados-$(date +%H%M).json
python3 -c 'import json,sys
d=json.load(open(sys.argv[1])); print("total", d["total"], "descartados", d["descartados"])
for c in d["capturados"]: print(c["to"], "|", c["text"].replace("\n"," ⏎ "))' /root/capturados-*.json
```

`descartados` tiene que ser `0` (si no, el anillo dio la vuelta y el orden ya no se puede afirmar). Cada capturado trae
su `command_id` y un `message_id` que empieza por `SINTETICO-`.

**Para la ventana de F10** (filas 1 y 6): los pasos 5 y 6 se corren **dos veces con el mismo guion** —una con el binario
viejo, antes de §2.2 del relevo, y otra con el modular, después—, cada una con su `CLIENTE` y su prefijo de `lote`, y se
comparan los textos (`diff` de las dos salidas, quitando el número del cliente). Fila 1: cada entrante tiene su
respuesta. Fila 6: ni una diferencia de texto. El Edge **no** se reinicia entre las dos: el relevo reinicia el Cloud, y
el Edge reconecta solo con su sesión falsa (medido en TW.11, hallazgo 35 del [README](README.md)).

### Paso 7 · Bajar la palanca (en este orden)

```bash
ctl DELETE "/v1/sessions/$SID" -H "$A"              # {"session_id":"…","unlinked":true}  — cada sesión falsa (JID 0000…)
cp -p "$EDGE_ENV.antes-receta" "$EDGE_ENV"          # fuera las dos variables
marca=$(wc -l < "$EDGE_LOG"); systemctl restart wapp-edge
```

**La comprobación que va al acta (RW.5.b):**

```bash
tail -n +$((marca+1)) "$EDGE_LOG" | grep -c 'PALANCA'               # 0
ctl POST /v1/diag/inbound/inject -d '{}'                             # 404
ctl GET '/v1/diag/outbound/captured?session_id=x'                    # 404
ctl GET /v1/sessions -H "$A"                                         # las del paso 3, ninguna con JID 0000…
grep 'latencia del handler de entrantes' "$EDGE_LOG" | tail -1       # … inyector=no whatsapp=real …  (periódica, 60 s por defecto, y al parar)
diff "$EDGE_ENV" "$EDGE_ENV.antes-receta" && rm "$EDGE_ENV.antes-receta"
```

Si se bajó **sin** desvincular: la sesión falsa queda `active` / `degraded` y el log repite `listener caído … no hay
device pareado para el JID de la sesión`, con *backoff*. No rompe nada más; el `DELETE` funciona igual con la palanca baja.

**Lo que queda en la nube** y no se borra: la fila de la sesión falsa en `fleet_sessions` (`state` `offline`, `self_pn`
`0000…`), y la conversación, el evento y la solicitud sintéticos de la empresa de prueba.

## 5 · La ejecución en local (2026-10-11)

**Con qué**: Cloud **nuevo** (`cmd/server-modular`) y Postgres del arnés de `test/procesos`, con su doble de identidad;
Edge real `wapp-edge-agent` @ `0675d55` compilado para `linux/arm64`, en un contenedor, **enrolado y parado con las
palancas bajas**; empresa con el flujo de carrito del arnés. La escena la montó un test temporal que no se commiteó; los
pasos 1–7 se corrieron **a mano**, con los comandos de §4. ✎ En esa corrida el login y la lista de sesiones se hicieron **después** de echar la palanca (el Edge no tenía ninguna); la receta los adelanta al paso 1, y `GET /v1/sessions` con la palanca baja y ese mismo token se midió después (`HTTP 200`, en «el olvido»). Tres diferencias con UAT: el reinicio fue parar y arrancar
`agent serve` leyendo un `.env` (no hay systemd ni `wapp-ctl`), no había cajero, y cada `curl` iba dentro de un `docker exec`.

| Paso | Salida (literal, recortada) |
|---|---|
| 1 · antes | `/v1/health` → `{"status":"ok","version":"0675d5523b7e…"}` `HTTP 200` · inject → `{"error":{"code":"not_found","message":"ruta no encontrada: /v1/diag/inbound/inject"}}` `HTTP 404` · captured → `HTTP 404` · `PALANCA ECHADA` en el log: `0` · `CloudLink habilitado … lease_gate=true sealed_transit=true lease_shadow_mode=false` |
| 2 · echar | el daemon que se paró: `… emision=final despachador=activo inyector=no whatsapp=real …` · el nuevo: `level=WARN msg="transporte de WhatsApp: PALANCA ECHADA (WAPP_AGENT_WHATSAPP_FALSO=true): este daemon NO HABLA CON WHATSAPP. …"` y `level=WARN msg="plano de control: PALANCA DE DIAGNÓSTICO ECHADA (WAPP_AGENT_INYECTOR_ENTRANTES=true): …"` · `/v1/health` `HTTP 200` |
| 3 · operador | login `HTTP 200` con token · `/v1/sessions` → `{"sessions":[]}` |
| 4 · emparejar | `{"id":"006f1574-…","status":"success","qr":""}` · `{"status":"success","qr":"","session_id":"5f456133-…","jid":"000024073023@s.whatsapp.net"}` · `/v1/sessions` → `"state":"active","health":"listening"` · primer capturado: el aviso de sesión pasiva, a `000024073023`, `message_id` `SINTETICO-0c58…` · perfil → `{"session_id":"5f456133-…","profile":"active"}` `HTTP 200` |
| 5 · conversar | siete mensajes (`carrito`, `1`, `1`, `2`, `2`, `2`, `1`), cada uno `{"lote":"RECETA-n","solicitados":1,"inyectados":1,"acusados":1,"errores":0,…}` `HTTP 200` |
| 6 · leer | `total 9, descartados 0`; en orden, tras el aviso: la bienvenida · `🛒 Elige una categoría: ⏎ 1) Bebidas ⏎ 2) Postres` · `Bebidas: ⏎ 1) Café · $2.50 ⏎ 2) Té · $2.00 ⏎ 0) ← Volver` · `Café · $2.50 ⏎ 1) Ver descripción ⏎ 2) Agregar al pedido ⏎ 0) ← Volver` · `¿Cuántos "Café"? Escribe la cantidad (0 ← volver)` · `Añadido al pedido ✅ …` · `🧾 Resumen del pedido: ⏎ Café x2  $5.00 ⏎ TOTAL  $5.00 …` · `✅ ¡Pedido confirmado! Total $5.00.` — todos a ese cliente, todos con `SINTETICO-` |
| 7 · bajar | `{"session_id":"5f456133-…","unlinked":true}` `HTTP 200` · tras reiniciar: `/v1/health` `HTTP 200`, `/v1/sessions` → `{"sessions":[]}`, inject `HTTP 404`, líneas `PALANCA ECHADA` o `listener caído` tras el arranque: `0`, el `.env` vacío, el directorio `sessions/` de la custodia vacío · `level=ERROR` en el log del servidor en toda la corrida: `0` |

**El olvido, medido aparte** (bajar sin desvincular): `/v1/sessions` → `"state":"active","health":"degraded"`; captured
`HTTP 404`; y en el log, `level=WARN msg="listener caído; sesión degradada, reintentando con backoff" … error="listen:
whatsapp: no hay device pareado para el JID de la sesión" reason=listener_down intento=1 siguiente_delay=1s` (2 s, 4 s…).
Después el `DELETE`, con la palanca ya baja, devolvió `unlinked:true`.

**En la nube quedó**: `GET /api/v1/sessions` → la sesión falsa con `"state":"offline","self_pn":"000024073023","profile":"active"`.

Lo que esta corrida **no** repite, porque ya lo afirma `make test-procesos-edge-real` sin nadie delante: el lease
revocado (cero capturados), las marcas en Postgres y la reanudación del carrito.

## 6 · Lo que solo se sabrá en UAT (lo mide F10-03)

| Qué | Por qué no se midió aquí |
|---|---|
| La ruta real del socket, y que `curl --unix-socket` llega con el usuario del servicio | En local el socket lo fijó el arnés |
| Que `agent serve` hereda las dos variables a través de `wapp-ctl -autostart` | Leído (`cmd/wapp-ctl` no arma un entorno propio para el hijo); en local no hubo `wapp-ctl` |
| Qué hace el transporte falso con las sesiones que el Edge de UAT ya tiene | §2.1: leído, no ejecutado. Las de UAT estaban caducadas el 2026-10-10 |
| La custodia de la DEK de la sesión falsa en el VPS | En el contenedor cayó en su último recurso (el archivo `0600`); en el VPS será lo que ese Linux tenga. No se toca (D-FW-7) |
| El cajero y Ollama vivos: una frase que el flujo mande a inferir **sí** se infiere | En local no había cajero y el Edge contestaba `ollama_down` |
| El login del operador contra la identidad real | En local, el doble de identidad del arnés |
| De dónde sale el token de la administradora para activar el perfil | En local lo emitió el arnés; en UAT, la consola del cliente o el canje de siempre |
| Con qué flujo y qué palabra se conversa | Depende de lo que tenga publicado la empresa de prueba de UAT |
