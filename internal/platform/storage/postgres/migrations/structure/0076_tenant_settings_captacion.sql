-- ============================================================
-- 0076: tenant_settings — LOS AJUSTES DE CAPTACIÓN DE LA OLA 1.8
-- (Plan 044 · Ola 1.8. Sección A: T1.8-1 · D-044.43. Secciones B y C: T1.8-2,
--  la bienvenida única · D6 — la B son sus dos columnas de config en esta misma
--  tabla, la C es la tabla NUEVA `conversation_welcomes` con su estado.)
--
-- POR QUÉ EL FICHERO SE LLAMA «captacion» Y NO «ventana_hibrida»
-- ------------------------------------------------------------
-- Porque no es suyo del todo. La Ola 1.8 gasta UN número de migración para DOS
-- tareas que tocan la MISMA tabla (`tenant_settings`) y el mismo asunto —cómo se
-- comporta la captación de pedidos frente al cliente—: T1.8-1 mete el TECHO de la
-- ventana de agregación y T1.8-2 meterá el texto y el umbral de la bienvenida.
-- Cerrar el nombre en «la ventana» obligaría a T1.8-2 a pedir otro número para
-- añadir dos columnas a la misma tabla, o —peor— a esconderlas bajo un nombre que
-- no las nombra. El plan lo dice literalmente: la `0076` «la gastan, por orden,
-- T1.8-1 (ventana híbrida + bienvenida)».
--
-- 🔴 Y ESO SE CUMPLIÓ, CON UNA COSA MÁS DE LA PREVISTA (T1.8-2, el 2026-08-25): además
-- de sus dos columnas, la bienvenida necesitaba una TABLA propia para su estado, porque
-- el concepto «primer mensaje de una conversación» no existía en ningún sitio del
-- esquema. Va en la sección C, en este mismo fichero, y el porqué está escrito allí: es
-- la misma unidad de despliegue que la B —sin la tabla el emisor saludaría en cada
-- mensaje, sin las columnas no sabría qué decir— y partirlas permitiría desplegar media
-- funcionalidad.
--
-- 🔴 EL NÚMERO SE VERIFICÓ CON `ls`, NO SE HEREDÓ DE LA DOC. El 2026-08-25, con el
-- directorio delante, la última migración REAL era la
-- `0075_owner_degradation_notices.sql` (la ampliación a ocho motivos de D-044.36 se
-- hizo EDITANDO la 0075, no con una 0076), así que la `0076` estaba LIBRE. «Conocida»
-- no es «verificada»: quien añada la siguiente repite el `ls`.
--
-- ⚠️ **NO sube `SchemaVersion`**: el Plan 044 hace UN SOLO bump al cierre, en T6.2
-- (INV-8). Se comprobó en `migrations/version.go:297` — hoy `0.44.0`, puesta por el
-- Plan 046 · Ola 5— y ninguna de las migraciones de este plan (`0071`–`0075`) la ha
-- movido. El runner reaplica por HASH de contenido, así que este fichero se ejecuta
-- igual sin tocar la constante.
--
-- ============================================================
-- SECCIÓN A) EL TECHO DE LA VENTANA DE AGREGACIÓN (T1.8-1, D-044.43)
-- ============================================================
--
-- QUÉ CAMBIA EN EL COMPORTAMIENTO, DICHO ANTES QUE EL DDL
-- ------------------------------------------------------------
-- Hasta hoy la ventana de captación se medía SIEMPRE contra el PRIMER mensaje:
-- `COALESCE(message_ts, created_at) + aggregation_window_seconds`
-- (`internal/intake/postgres.go`, `listAggregatingSQL`). Eso tiene un defecto y una
-- virtud, y las dos importan para entender por qué esta columna existe:
--
--   * EL DEFECTO: una ráfaga que dura más que la ventana SE PARTE EN DOS JOBS. El
--     cliente escribe a t=0, t=30 y t=60 —una sola petición, tecleada despacio— y a
--     los 45 s el barrido cierra lo que hay, dejando el tercer mensaje fuera. El
--     presupuesto sale incompleto y NADIE ve un error.
--   * LA VIRTUD: nunca se queda abierta. Es, literalmente, un techo.
--
-- La cura obvia —anclar en el silencio, `now() - updated_at`— arregla el defecto y
-- TIRA la virtud: una conversación que gotea cada 40 s no alcanza nunca 45 s de
-- silencio y su ventana NO CIERRA JAMÁS. Un job en `aggregating` que nadie recoge es
-- un pedido perdido sin traza de error, que es el modo de fallo que esta casa peor
-- tolera.
--
-- ⇒ VENTANA HÍBRIDA: cierra por lo que llegue ANTES.
--
--     silencio : now() - updated_at  >= aggregation_window_seconds   (45 s, 0072)
--     techo    : now() - created_at  >= aggregation_max_seconds      (120 s, ESTA)
--
-- 🔴 LOS DOS PLAZOS SE MIDEN CONTRA EL RELOJ DE POSTGRES, y por eso las anclas son
-- `updated_at` y `created_at` y NO `message_ts`. `message_ts` lo pone el Edge (es el
-- `ts_unix` del cliente): compararlo con `now()` es comparar DOS RELOJES, que en esta
-- casa ya tiene ficha propia de incidente. `message_ts` NO cambia de significado —
-- sigue siendo el instante del PRIMER mensaje y la base de fechas del presupuesto
-- (D-044.9)— y esta migración no lo toca.
--
-- ------------------------------------------------------------
-- POR QUÉ 120, Y QUÉ SIGNIFICA EL 0
-- ------------------------------------------------------------
-- 120 = el doble largo de los 45 s de silencio, con el número dicho al revés: es
-- CUÁNTO tiempo como mucho puede el cliente seguir añadiendo mensajes a un mismo
-- pedido antes de que el sistema decida que ya tiene bastante y empiece a trabajar.
-- Dos minutos de tecleo son una petición larga; el presupuesto del plan es «primer
-- borrador en < 5 min» (T6.1) y este techo se come 2 de esos 5 en el peor caso.
-- Subirlo es gastar ese presupuesto y hay que decirlo con el número delante, igual
-- que se dice de la ventana de silencio.
--
-- 🔴 `0` SIGNIFICA «VENCIDO SIEMPRE» (cierre en el primer barrido), EXACTAMENTE LO
-- MISMO que el 0 de `aggregation_window_seconds`. Se decide así —y se escribe aquí—
-- porque la alternativa tentadora, «0 = sin techo», pondría a DOS columnas vecinas de
-- la misma tabla a significar cosas OPUESTAS con el mismo número (una «ya» y la otra
-- «nunca»), que es una trampa que alguien pisa tarde o temprano. Y además:
--
--   * NO EXISTE «SIN TECHO», Y ES DELIBERADO. La ausencia de techo es precisamente
--     el defecto que T1.8-1 viene a cerrar; ofrecerla como configuración sería
--     construir el interruptor del bug.
--   * el 0 aquí es REDUNDANTE con poner la ventana en 0 (las dos formas de decir
--     «sin agregación»), y la redundancia es inofensiva: las dos cierran en el primer
--     barrido y producen el mismo job.
--
-- POR QUÉ EL CHECK ES `>= 0` Y NO `> 0`: por lo anterior —el 0 tiene lectura— y por
-- simetría exacta con `tenant_settings_aggregation_window_check` de la 0072. Lo único
-- que se descarta es el negativo, que no significa «esperar poco» sino nada.
-- POR QUÉ NO HAY TECHO SUPERIOR: mismo argumento que la 0072 —ningún requisito lo
-- discutió y un número inventado no se puede defender—. Un techo absurdamente alto no
-- rompe la base: retrasa el presupuesto de ese tenant y solo de ese.
--
-- 🔴 NO SE AÑADE UN CHECK CRUZADO `aggregation_max_seconds >= aggregation_window_seconds`,
-- y no por olvido. Un CHECK entre dos columnas obliga a que TODA escritura las mueva
-- juntas: un endpoint de administración que bajase primero el techo y luego la
-- ventana reventaría en la primera sentencia aunque el estado final fuera legal. La
-- relación entre los dos números no es una invariante del esquema, es una elección
-- del tenant: con techo < ventana, el silencio deja de mandar y el tenant tiene un
-- plazo fijo desde el primer mensaje — que es EXACTAMENTE el comportamiento que el
-- sistema tenía antes de esta ola, y por tanto una configuración legítima.
--
-- ------------------------------------------------------------
-- LAS CUATRO REGLAS DEL PATRÓN FULL-REPLAY (0063:33-57 · 0073), APLICADAS AQUÍ
-- ------------------------------------------------------------
-- (1) COLUMNA SIN DEFAULT PRIMERO, DEFAULT DESPUÉS — se aplica, y aquí está el nervio
--     de la migración. Ver el bloque «EL BACKFILL» de abajo.
-- (2) BACKFILL CON GUARDA `WHERE … IS NULL` — se aplica entera.
-- (3) `SET NOT NULL` DENTRO DE UN `DO $$ … IF is_nullable = 'YES'` — se aplica: se
--     añade una columna a una tabla CON FILAS, que es justo el caso para el que esa
--     guarda existe (0068). Va DESPUÉS del backfill: promover antes reventaría con
--     las filas a medio rellenar.
-- (4) CHECK CON NOMBRE EXPLÍCITO, FUERA DEL `ADD COLUMN` Y RECREADO EN CADA REPLAY —
--     se aplica. Un CHECK inline en un `ADD COLUMN IF NOT EXISTS` se salta CON él del
--     segundo arranque en adelante y deja de recrearse para siempre (lo pagó la 0071
--     con tiempo real el 2026-08-22).
--
-- ============================================================
-- 🔴 EL BACKFILL: POR QUÉ NO BASTA `ADD COLUMN … DEFAULT 120`
-- ============================================================
-- Desde PostgreSQL 11 un `ADD COLUMN … DEFAULT x` SÍ rellena las filas que ya
-- existen, así que la tentación es escribir una línea y llamarlo backfill —es lo que
-- hizo la 0072 sección E para `aggregation_window_seconds`, y allí era correcto
-- porque no había ninguna columna anterior de la que derivar un valor mejor—.
--
-- AQUÍ SÍ LA HAY, y por eso una sola línea sería un cambio de conducta silencioso:
--
--   Un tenant con `aggregation_window_seconds = 300` cierra HOY sus ventanas a los
--   300 s del PRIMER mensaje (el ancla vieja es el primero). Si esta migración le
--   pusiera el techo en 120, mañana cerrarían a los 120 s: su configuración explícita
--   quedaría NEUTRALIZADA por un default de plataforma, sin error y sin aviso. Es el
--   mismo accidente que la 0073 documentó para `via` y evitó por este mismo camino.
--
-- ⇒ el backfill preserva la intención que ya está escrita en la fila:
--
--     aggregation_max_seconds := GREATEST(120, aggregation_window_seconds)
--
--   * ventana <= 120 (todos los tenants de hoy, cuyo default es 45) ⇒ techo 120, que
--     es el valor nuevo de plataforma;
--   * ventana  > 120 ⇒ techo = la ventana, que reproduce EXACTAMENTE el
--     comportamiento de hoy (cierre a `created_at + ventana`) y no cambia nada para
--     ese tenant. Ni una latencia se mueve por culpa de la migración.
--
-- ⚠️ Y NO SE PONE UNA RATIO INVENTADA (`ventana * 2`, `ventana + 60`…): esos números
-- no salen de ningún requisito y nadie sabría defenderlos dentro de tres meses.
-- `GREATEST` dice la única cosa que se puede defender: «el techo nunca queda por
-- debajo del plazo que el tenant ya había elegido».
--
-- 🔴 EL BACKFILL NO SE PRUEBA CONTRA UNA BASE RECIÉN MIGRADA. En una base virgen
-- `tenant_settings` está vacía y CUALQUIER barrido sale verde por cero filas — el
-- modo favorito de este repo de mentir en verde. Su test FABRICA el estado anterior
-- (fila con la ventana en 300, columna ausente, migrar encima): vive en
-- `internal/flujos/store/aggregation_window_integration_test.go`.
--
-- ⚠️ ADITIVA E IDEMPOTENTE. Del segundo arranque en adelante: el `ADD COLUMN IF NOT
-- EXISTS` es NO-OP, el backfill afecta 0 filas (su guarda `IS NULL` no casa con nada
-- porque la columna ya es NOT NULL), el `SET DEFAULT` reescribe el mismo default y el
-- `DO $$` no promueve nada. 🔴 Consecuencia deseada: un tenant que baje su techo a 60
-- NO lo ve volver a 120 en el próximo reinicio — que es la mitad del argumento por la
-- que el backfill lleva guarda en vez de ser un `UPDATE` a secas.
-- ------------------------------------------------------------

-- (a) LA COLUMNA, SIN DEFAULT Y NULLABLE. Nace así a propósito: el NULL es lo que el
--     backfill de (b) usa como marca de «esta fila todavía no tiene techo». Con un
--     default puesto aquí, (b) no encontraría NI UNA fila que tocar y el caso del
--     tenant con ventana > 120 se perdería en silencio.
ALTER TABLE public.tenant_settings
    ADD COLUMN IF NOT EXISTS aggregation_max_seconds INTEGER;

-- (b) EL BACKFILL. La guarda `IS NULL` es lo que lo hace idempotente bajo full-replay:
--     en el segundo arranque no hay ninguna fila sin techo y esto afecta 0 filas.
UPDATE public.tenant_settings
   SET aggregation_max_seconds = GREATEST(120, aggregation_window_seconds)
 WHERE aggregation_max_seconds IS NULL;

-- (c) EL DEFAULT, para las filas FUTURAS. A las viejas las gobierna (b), y solo (b).
ALTER TABLE public.tenant_settings
    ALTER COLUMN aggregation_max_seconds SET DEFAULT 120;

-- (d) EL `NOT NULL`, guardado. Sin la guarda, un replay sobre una base donde la
--     columna ya es NOT NULL vuelve a ejecutar un ALTER que reescanea la tabla entera
--     por nada; con ella, la promoción ocurre UNA vez.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM information_schema.columns
         WHERE table_schema = 'public'
           AND table_name   = 'tenant_settings'
           AND column_name  = 'aggregation_max_seconds'
           AND is_nullable  = 'YES'
    ) THEN
        ALTER TABLE public.tenant_settings
            ALTER COLUMN aggregation_max_seconds SET NOT NULL;
    END IF;
END
$$;

-- (e) EL CHECK, con nombre y recreado en cada replay (regla 4).
ALTER TABLE public.tenant_settings
    DROP CONSTRAINT IF EXISTS tenant_settings_aggregation_max_check;
ALTER TABLE public.tenant_settings
    ADD CONSTRAINT tenant_settings_aggregation_max_check
    CHECK (aggregation_max_seconds >= 0);

COMMENT ON COLUMN public.tenant_settings.aggregation_max_seconds IS
'Plan 044 T1.8-1: TECHO de la ventana de captación, en segundos desde created_at del job. La ventana cierra por lo que llegue ANTES: silencio (now()-updated_at >= aggregation_window_seconds) o techo (now()-created_at >= este valor). Ambos plazos contra el reloj de Postgres, NUNCA contra message_ts (que viene del Edge). 0 = vencido siempre (cierre en el primer barrido), igual que el 0 de aggregation_window_seconds; NO existe "sin techo" y es deliberado.';

-- ============================================================
-- SECCIÓN B) LA BIENVENIDA ÚNICA — SU TEXTO Y SU UMBRAL (T1.8-2, D6)
-- ============================================================
--
-- QUÉ CAMBIA EN EL COMPORTAMIENTO, DICHO ANTES QUE EL DDL
-- ------------------------------------------------------------
-- Hoy, entre que el cliente escribe y que le llega el borrador del presupuesto, el
-- Cloud NO LE DICE NADA. Los plazos de la sección A son 45 s de silencio y 120 s de
-- techo, y encima de eso va el pipeline entero: el cliente pasa minutos mirando una
-- conversación muda sin saber si su mensaje llegó. La bienvenida es UNA frase fija
-- —«estamos procesando»— que se manda al PRIMER mensaje de una conversación y otra
-- vez si el contacto vuelve tras un silencio largo. Nunca por interacción.
--
-- 🔴 NO ES CONTENIDO DEL ANÁLISIS Y ESO GOBIERNA TODO SU DISEÑO. La bienvenida no
-- entra en `source_refs`, no entra en `source_text` (ni rotulada) y no mueve el
-- `updated_at` del job. La razón no es de presupuesto: una `evidence` del borrador que
-- apuntara a ella sería una `evidence` que apunta a un texto QUE ESCRIBIMOS NOSOTROS,
-- o sea, el sistema citándose a sí mismo como si fuera el cliente. Por eso el emisor
-- vive fuera del camino del agregador y del hilo, y por eso aquí NO hay ninguna
-- columna que la ligue a `intake_jobs` ni a `conversation_event_messages`.
--
-- ⚠️ NO CONFUNDIR CON LOS OTROS DOS AUTOMENSAJES DE LA PLATAFORMA. La notificación de
-- degradación (0075, `owner_degradation_notices`) va AL DUEÑO, y el aviso de sesión
-- pasiva (0066, `fleet_sessions.greeted_at`) va al NÚMERO DE LA PROPIA SESIÓN. Esta va
-- AL CLIENTE, y es el único texto que el Plan 044 le manda antes del borrador. Los tres
-- son textos FIJOS del sistema: INV-1/INV-2 siguen intactas, el LLM no escribe ninguno.
--
-- ------------------------------------------------------------
-- POR QUÉ DOS COLUMNAS Y NO UNA, Y POR QUÉ AQUÍ
-- ------------------------------------------------------------
-- Son las dos únicas cosas que un dueño puede querer cambiar: QUÉ dice y CADA CUÁNTO
-- se repite. Van en `tenant_settings` porque son configuración por tenant del mismo
-- asunto que la sección A —cómo se comporta la captación frente al cliente— y porque
-- esta tabla es donde vive ya el resto de esa configuración (page_size, los TTL, la
-- ventana). El ESTADO —«a este contacto ya le saludé», «cuándo habló por última vez»—
-- NO va aquí: es por conversación, no por tenant, y tiene tabla propia en la sección C.
--
-- ⚠️ NO HAY INTERRUPTOR DE «APAGAR LA BIENVENIDA», y no es un olvido: el interruptor
-- ES la feature `llm_intake` del tenant (ADR-0022). Un tenant sin ella no recibe
-- bienvenida NUNCA —el gate es fail-closed, igual que el del hilo literal—, así que una
-- columna `welcome_enabled` sería un segundo interruptor para lo mismo. Y un segundo
-- interruptor es exactamente lo que el Plan 044 · T1.6 tuvo que RETIRAR de `thread.go`
-- después de que dejara al plan sin materia prima durante doce días.

-- (a) EL TEXTO. `''` NO significa «sin bienvenida»: significa EL TEXTO DE PLATAFORMA
--     (store.DefaultWelcomeText), que es lo mismo que recibe un tenant SIN fila en esta
--     tabla. Los dos caminos —fila con '' y sin fila— tienen que dar el mismo mensaje, o
--     el comportamiento dependería de si alguien tocó alguna vez el page_size.
--
--     🔴 EL `DEFAULT ''` ALCANZA A LAS FILAS QUE YA EXISTEN, y aquí eso es lo CORRECTO
--     (al revés que en la sección A): no hay ninguna columna anterior de la que derivar
--     un texto mejor, así que el default ES el backfill —el mismo argumento, palabra por
--     palabra, que la 0072 sección E escribió para `aggregation_window_seconds`—. Un
--     backfill aparte no tendría de dónde sacar nada distinto de '' y solo añadiría una
--     sentencia que afectaría a las mismas filas con el mismo valor.
--
--     Dato de NEGOCIO EN CLARO (ADR-0009): es una frase que el dueño escribe para que la
--     lean sus clientes, no PII. ⚠️ Y por eso mismo lleva aviso: quien la configure NO
--     debe meter aquí datos de nadie —esta columna está en claro y se manda por WhatsApp
--     a CUALQUIERA que escriba—. Es el mismo aviso que lleva `intake_jobs.error`.
ALTER TABLE public.tenant_settings
    ADD COLUMN IF NOT EXISTS welcome_text TEXT NOT NULL DEFAULT '';

-- (b) EL UMBRAL DE SILENCIO, EN SEGUNDOS. El enunciado habla de «N horas» y la columna
--     se llama `_seconds` a propósito: TODOS los relojes de esta tabla se miden en
--     segundos (`order_ttl_seconds`, `conversation_ttl_seconds`,
--     `event_inactivity_ttl_seconds`, `event_history_ttl_seconds`,
--     `aggregation_window_seconds`, `aggregation_max_seconds`). Una columna en horas
--     sería la única que no, y quien escribiera un UPDATE a mano sobre dos de ellas
--     acertaría en una y fallaría en la otra sin que nada se quejara.
--
-- 🔴 POR QUÉ 86400 (24 h) Y NO UN NÚMERO CUALQUIERA. El argumento NO es «un día suena
--     bien», es que 24 h está MUY POR ENCIMA de todos los relojes conversacionales de
--     esta misma tabla (`conversation_ttl_seconds` y `event_inactivity_ttl_seconds`,
--     los dos en 7200 = 2 h). Esa distancia es lo que convierte la promesa «nunca por
--     interacción» en algo que sostiene el NÚMERO y no solo la guarda del código: para
--     que el umbral venciera durante una conversación viva, esa conversación tendría
--     que llevar 24 h de silencio, y a las 2 h el reloj que manda ya la habría soltado.
--     Quien baje este valor por debajo de 7200 se queda SOLO con la guarda del runtime
--     (que no saluda sobre conversación viva), y eso hay que saberlo antes de bajarlo.
--
-- 🔴 QUÉ SIGNIFICA EL 0, dicho porque en esta tabla el 0 ya significa tres cosas
--     distintas y hay que decir cuál es la de aquí: 0 = VENCIDO SIEMPRE, la MISMA
--     lectura que el 0 de `aggregation_window_seconds` y `aggregation_max_seconds` (y no
--     la de `conversation_ttl_seconds`, donde 0 es «sin vencimiento»). Con 0, todo
--     mensaje que no avance una conversación viva vuelve a recibir la bienvenida. Es
--     una configuración legítima —un tenant que quiera acuse SIEMPRE— y es la que se
--     desaconseja: repite la frase, y «nunca por interacción» deja de cumplirse en la
--     práctica aunque la guarda del runtime siga en su sitio.
--
--     POR QUÉ EL CHECK ES `>= 0` Y NO `> 0`: por lo anterior —el 0 tiene lectura— y por
--     simetría exacta con sus dos vecinas. Lo único que descarta es el negativo, que no
--     significa «poco silencio» sino nada.
--     POR QUÉ NO HAY TECHO SUPERIOR: mismo argumento que la 0072 y que la sección A.
--     Ningún requisito lo discutió y un número inventado no se podría defender. Un
--     umbral absurdamente alto no rompe nada: ese tenant saluda UNA vez y no repite.
ALTER TABLE public.tenant_settings
    ADD COLUMN IF NOT EXISTS welcome_silence_seconds INTEGER NOT NULL DEFAULT 86400;

-- Su CHECK, con nombre y FUERA del `ADD COLUMN`, por la regla (4) de la cabecera: un
-- CHECK inline en un `ADD COLUMN IF NOT EXISTS` se salta CON él del segundo arranque en
-- adelante y deja de recrearse para siempre (lo pagó la 0071 el 2026-08-22).
ALTER TABLE public.tenant_settings
    DROP CONSTRAINT IF EXISTS tenant_settings_welcome_silence_check;
ALTER TABLE public.tenant_settings
    ADD CONSTRAINT tenant_settings_welcome_silence_check
    CHECK (welcome_silence_seconds >= 0);

COMMENT ON COLUMN public.tenant_settings.welcome_text IS
'Plan 044 T1.8-2 (D6): TEXTO FIJO de la bienvenida unica que el Cloud manda AL CLIENTE al primer mensaje de una conversacion, y otra vez tras welcome_silence_seconds de silencio. Cadena vacia (el DEFAULT) = usar el texto de PLATAFORMA (store.DefaultWelcomeText), que es lo mismo que recibe un tenant SIN fila en esta tabla; NO significa "sin bienvenida". Apagar la bienvenida se hace quitandole al tenant la feature llm_intake, que es el unico interruptor y es fail-closed. NO es contenido del analisis: no entra en intake_jobs.source_refs ni en source_text, ni rotulada, y no mueve el updated_at del job -- una evidence que apuntara a ella seria el sistema citandose a si mismo. Lo escribe el DUENO y lo lee CUALQUIERA que escriba al numero: dato de negocio EN CLARO (ADR-0009), aqui NO van datos de personas.';

COMMENT ON COLUMN public.tenant_settings.welcome_silence_seconds IS
'Plan 044 T1.8-2 (D6): segundos de silencio del contacto tras los cuales la bienvenida VUELVE a mandarse. Se mide contra conversation_welcomes.last_incoming_at, que es el instante del ultimo mensaje del contacto. DEFAULT 86400 (24 h), elegido MUY por encima de conversation_ttl_seconds y event_inactivity_ttl_seconds (7200 los dos) para que el umbral no pueda vencer durante una conversacion viva: bajarlo por debajo de 7200 deja la promesa "nunca por interaccion" sostenida SOLO por la guarda del runtime. 0 = VENCIDO SIEMPRE (bienvenida en cada mensaje que no avance conversacion viva), la misma lectura que el 0 de aggregation_window_seconds y aggregation_max_seconds, y NO la de conversation_ttl_seconds. CHECK >= 0: el negativo no significa nada. Sin techo superior a proposito.';

-- ============================================================
-- SECCIÓN C) EL ESTADO DE LA BIENVENIDA: `conversation_welcomes` (T1.8-2)
-- ============================================================
--
-- 🔴 POR QUÉ ESTA SECCIÓN VIVE EN UN FICHERO QUE SE LLAMA «tenant_settings»
-- ------------------------------------------------------------
-- Porque el nombre completo es `0076_tenant_settings_captacion` y lo que lo gobierna es
-- la segunda mitad: la CAPTACIÓN de la Ola 1.8. La cabecera dice, literalmente, que este
-- fichero se llama «captacion» para que T1.8-2 pueda meter aquí lo suyo sin pedir otro
-- número. La bienvenida necesitaba DOS cosas —config por tenant (sección B) y estado por
-- conversación (esta)—, y las dos son la misma unidad de despliegue: sin la tabla, el
-- emisor no sabe a quién ya saludó y saludaría en cada mensaje; sin las columnas, no
-- sabe qué decir ni cada cuánto. Partirlas en dos migraciones permitiría desplegar media
-- funcionalidad, que es peor que un nombre de fichero que se queda corto.
--
-- ⚠️ Y esta migración NO ESTÁ DESPLEGADA: ampliarla editándola es lo correcto (regla de
-- la casa para migraciones no aplicadas, la misma por la que la 0072 sección F entró
-- tarde). No hay ninguna base con la 0076 aplicada a la que esto llegue tarde.
--
-- ------------------------------------------------------------
-- QUÉ GUARDA, Y POR QUÉ NO CABÍA EN NINGUNA TABLA QUE YA EXISTA
-- ------------------------------------------------------------
-- Dos hechos por conversación, y el concepto «primer mensaje de una conversación» NO
-- EXISTÍA en el esquema. Se buscaron los tres candidatos obvios y ninguno sirve:
--
--   * `contacts` (0005) — NO TIENE `last_seen`, y sobre todo su PK es
--     (tenant_id, kind, value): UN contacto tiene VARIAS filas, una por referencia
--     (número + LID + username, todas con el mismo contact_id). Una marca de «ya le
--     saludé» ahí se duplicaría en N filas y el centinela `WHERE welcomed_at IS NULL`
--     dejaría de ser una pregunta con UNA respuesta. Su `updated_at`, además, solo se
--     mueve al re-resolver el push_name: no es un reloj de actividad.
--   * `conversation_events.last_activity_at` (0051) — es POR EVENTO ABIERTO. Sin evento
--     vivo (el LIMBO: un saludo, la cháchara) NO HAY FILA, y el LIMBO es justo donde la
--     bienvenida tiene que decidir.
--   * `flow_state.updated_at` (0005) — SE BORRA. `releaseForNewConversation` destruye la
--     fila al vencer el TTL, y con ella se iría la única prueba de que ya saludamos: el
--     contacto volvería a recibir la bienvenida por el camino equivocado.
--
-- ⇒ Estado propio, con la MISMA clave que el motor usa para todo lo demás:
--   (tenant_id, session_id, contact_id) — `store.Key`, idéntica a la PK de `flow_state`.
--
-- 🔴 POR QUÉ LA CLAVE LLEVA `session_id` Y NO ES SOLO (tenant, contacto). El enunciado
-- dice «un contacto en un tenant», y esa frase se lee como el SCOPING de tenant
-- (INV-8), no como una declaración de que la sesión sobra. Un tenant con dos sesiones
-- son DOS números de WhatsApp distintos: el cliente que escribe al segundo está
-- empezando una conversación con un número que nunca le ha contestado nada, y con la
-- clave por (tenant, contacto) se quedaría MUDO. Entre saludar de más a quien escribe a
-- dos números de la misma empresa y dejar en silencio una conversación entera, esta casa
-- elige lo primero. Y es además la clave con la que el runtime ya serializa (keyedMutex),
-- carga estado y cuenta rachas: cualquier otra obligaría a traducir.
--
-- ------------------------------------------------------------
-- LAS DOS COLUMNAS, Y POR QUÉ HACEN FALTA LAS DOS
-- ------------------------------------------------------------
--   * `welcomed_at` — cuándo se entregó la última bienvenida. NULL = nunca. Es la
--     idempotencia de «una por conversación» y es el MISMO patrón exacto que
--     `fleet_sessions.greeted_at` (0066): centinela en el UPDATE, y se marca SOLO si el
--     Ack del Edge vuelve `ok=true`. Si el envío falla, NO se marca y el siguiente
--     mensaje reintenta — un aviso que no llegó no se puede dar por dado.
--   * `last_incoming_at` — cuándo habló el contacto por última vez. Es lo que ancla el
--     umbral de silencio, y NO se puede sustituir por `welcomed_at`: la regla «vuelve a
--     saludar tras N h de SILENCIO» anclada en el saludo sería «vuelve a saludar cada N
--     h», que dispararía en mitad de una conversación larga. Es exactamente lo que el
--     enunciado prohíbe («nunca por interacción»).
--
-- ⚠️ ESTA TABLA NO ES UN REGISTRO DE ACTIVIDAD DE PROPÓSITO GENERAL, y quien venga
-- buscando uno debe irse: `last_incoming_at` está aquí porque la regla de la bienvenida
-- lo necesita, y solo se escribe para los tenants que pueden recibirla (los que tienen
-- `llm_intake`). Colgar de ella un «último visto» de producto sería construir sobre una
-- columna que miente para media flota. Superficie muerta = D-044.23.
--
-- CERO PII (ADR-0009/ADR-0010): tres identificadores OPACOS y dos marcas de tiempo. Ni
-- el número, ni el push_name, ni una sola letra de lo que el cliente escribió.
--
-- RETENCIÓN: no hay poda, y es la MISMA decisión que `intakes`, `flow_events`,
-- `webhook_outbox` e `intake_jobs` tras el ADR-0043 (Plan 046). Lo que sí la recorta es
-- el `ON DELETE CASCADE` del tenant: borrar la empresa se lleva sus marcas.
--
-- ADITIVA e IDEMPOTENTE: `CREATE TABLE IF NOT EXISTS` es NO-OP EXACTO del segundo
-- arranque en adelante, así que las marcas reales SOBREVIVEN a cada replay. NO sube
-- SchemaVersion (un solo bump del 044, en T6.2).
--
-- ⚠️ AVISO PARA QUIEN AÑADA UNA COLUMNA AQUÍ MAÑANA: `CREATE TABLE IF NOT EXISTS` no
-- toca una tabla que ya existe. Una columna nueva se añade con `ALTER TABLE ... ADD
-- COLUMN IF NOT EXISTS`, nunca editando este CREATE — y un `COMMENT ON COLUMN` sobre una
-- columna que el CREATE no repuso mata el arranque siguiente (0051:86-90, y el caso real
-- que el Plan 046 · Ola 5 pagó con la columna borrada de `contacts`).
CREATE TABLE IF NOT EXISTS public.conversation_welcomes (
    tenant_id        UUID        NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    session_id       TEXT        NOT NULL,
    contact_id       UUID        NOT NULL,          -- OPACO: contacts.contact_id (ADR-0017)
    last_incoming_at TIMESTAMPTZ NOT NULL,          -- el ancla del silencio. SIN default: ver abajo.
    welcomed_at      TIMESTAMPTZ,                   -- NULL = nunca se le dio la bienvenida
    PRIMARY KEY (tenant_id, session_id, contact_id)
);

COMMENT ON TABLE public.conversation_welcomes IS
'Plan 044 T1.8-2 (D6): estado de la BIENVENIDA UNICA por conversacion (tenant, sesion, contacto) -- la misma clave que flow_state, que es la que el motor usa para todo. Existe porque el concepto "primer mensaje de una conversacion" no vivia en ningun sitio: contacts no tiene last_seen y ademas tiene N filas por contacto (una por referencia), conversation_events.last_activity_at es por evento abierto y no existe en el LIMBO, y flow_state.updated_at SE BORRA al vencer el TTL. CERO PII (ADR-0009/ADR-0010): identificadores opacos y marcas de tiempo. Solo se escribe para tenants con la feature llm_intake. NO es un registro de actividad de proposito general: last_incoming_at esta aqui porque la regla de la bienvenida lo necesita. Sin poda por tiempo, igual que intakes/flow_events/webhook_outbox/intake_jobs tras el ADR-0043; lo unico que la recorta es el CASCADE del tenant.';

COMMENT ON COLUMN public.conversation_welcomes.tenant_id IS
'Tenant dueno de la conversacion (FK a tenants, CASCADE). Scoping INV-8.';
COMMENT ON COLUMN public.conversation_welcomes.session_id IS
'Sesion CloudLink por la que entra la conversacion. VA EN LA CLAVE A PROPOSITO: dos sesiones del mismo tenant son dos numeros de WhatsApp distintos, y el cliente que escribe al segundo empieza una conversacion que nunca le ha contestado nada. Sin session_id en la clave, esa segunda conversacion se quedaria MUDA.';
COMMENT ON COLUMN public.conversation_welcomes.contact_id IS
'Identidad OPACA del contacto (contacts.contact_id, ADR-0017). NO es un telefono.';
COMMENT ON COLUMN public.conversation_welcomes.last_incoming_at IS
'Instante del ULTIMO mensaje del contacto. Es el ancla del umbral tenant_settings.welcome_silence_seconds. SIN DEFAULT now() a proposito: lo escribe SIEMPRE el runtime con SU reloj inyectable (rt.now, WithClock), de modo que el valor guardado y el "ahora" con el que se compara salen del MISMO reloj. Un DEFAULT now() aqui metaria el reloj de Postgres en una comparacion que hace Go, que es un modo de fallo permanente y silencioso ya conocido en esta casa. NO se puede sustituir por welcomed_at: anclar el umbral en el saludo convertiria "vuelve a saludar tras N h de silencio" en "vuelve a saludar cada N h", que dispara en mitad de una conversacion larga -- justo lo que el enunciado prohibe.';
COMMENT ON COLUMN public.conversation_welcomes.welcomed_at IS
'Cuando se ENTREGO la ultima bienvenida a esta conversacion. NULL = nunca, y es el estado CORRECTO de una conversacion a la que nadie saludo: por eso la columna es NULLABLE, SIN default y SIN backfill (mismo argumento exacto que fleet_sessions.greeted_at de la 0066 -- un backfill aqui AFIRMARIA algo falso). Solo la escribe MarkWelcomed, con centinela sobre el valor leido (compare-and-set) y SOLO cuando el Ack del Edge vuelve ok=true: si el envio falla no se marca y el siguiente mensaje reintenta. Un aviso que no llego no se da por dado.';
