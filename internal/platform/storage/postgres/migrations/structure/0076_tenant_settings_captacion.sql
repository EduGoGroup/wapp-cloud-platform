-- ============================================================
-- 0076: tenant_settings — LOS AJUSTES DE CAPTACIÓN DE LA OLA 1.8
-- (Plan 044 · Ola 1.8. Sección A: T1.8-1 · D-044.43. Sección B: reservada
--  a T1.8-2, la bienvenida única.)
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
-- SECCIÓN B) RESERVADA — LA BIENVENIDA ÚNICA (T1.8-2)
-- ============================================================
-- Aquí van, cuando T1.8-2 se ejecute, el texto de la bienvenida por tenant y el
-- umbral de horas de silencio tras el cual se vuelve a mandar. Se dejan ESCRITAS como
-- hueco y no como columnas vacías: una columna que nadie escribe ni lee es superficie
-- muerta (D-044.23), y el plan solo autoriza a compartir el NÚMERO de migración, no a
-- adelantar el DDL de una tarea que todavía no ha decidido su forma.
--
-- Quien la escriba: añade las columnas AQUÍ ABAJO, en este mismo fichero, con el mismo
-- patrón de cuatro pasos de la sección A. El runner es full-replay por hash, así que
-- editar este fichero reejecuta el esquema entero y las columnas de la sección A
-- sobreviven intactas (sus guardas son NO-OP exactos). Lo que NO se puede hacer es
-- ampliar un CHECK cerrado de aquí con una migración POSTERIOR: bajo full-replay la
-- vieja corre antes y aborta el arranque.
