// welcome_integration_test.go — Plan 044 · Ola 1.8 · T1.8-2 (D6), contra Postgres
// REAL: las dos columnas de configuración de la bienvenida (`tenant_settings`,
// migración 0076 sección B) y la tabla de su estado (`conversation_welcomes`,
// sección C) con los dos métodos que la manejan.
//
// ⏳ ESTOS TESTS SE ESCRIBIERON SIN POSTGRES DELANTE y se ejecutaron después con
// `make test-integration`; el resultado real está en el informe de la tarea. Cada
// aserción lleva escrita su SALIDA ESPERADA, que es lo que permite leerlos aunque no
// se puedan correr.
//
// # QUÉ NO SE PUEDE PROBAR CONTRA EL GEMELO EN MEMORIA (y por eso este fichero existe)
//
//  1. Los DEFAULT (” y 86400) y que ALCANCEN a las filas que YA existían: un mapa de
//     Go no tiene defaults, y el modo de fallo que esto vigila —un `ADD COLUMN` sin
//     default, con Postgres rellenando el cero del tipo— solo ocurre en el esquema. Un
//     0 en `welcome_silence_seconds` significa VENCIDO SIEMPRE, así que ese descuido
//     mandaría la bienvenida en CADA mensaje de todo tenant preexistente.
//  2. El CHECK con nombre: que MUERDA ante un negativo.
//  3. 🔴 Que `TouchContact` devuelva la fila ANTERIOR. En memoria eso es copiar una
//     variable; en SQL depende de que el CTE `previo` lea el snapshot de antes del
//     `INSERT … ON CONFLICT`. Si alguien lo «simplificara» a un `RETURNING`, el gemelo
//     en memoria seguiría verde y la bienvenida dejaría de volver tras el silencio —el
//     silencio saldría 0 siempre— sin un solo error.
//  4. Que el centinela de `MarkWelcomed` sea `IS NOT DISTINCT FROM` y no `=`: con `=`,
//     comparar contra NULL no casa NINGUNA fila y la PRIMERA bienvenida no se marcaría
//     jamás, así que el contacto la recibiría en cada mensaje.
//
// Se corre igual que el resto: sin build tag, por el nombre del fichero, con
// `WAPP_TEST_DB_DSN`. Sin ella se salta solo. Reusa `openTestDB` y `seedTenant` de
// integration_test.go y `invalidaElHashDelEsquema` de
// aggregation_window_integration_test.go (mismo paquete `store_test`): no se define
// ningún mecanismo nuevo.
package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/flujos/store"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres/migrations"
)

// ---------------------------------------------------------------------------
// (B) LAS DOS COLUMNAS DE CONFIGURACIÓN
// ---------------------------------------------------------------------------

// TestIntegration_Bienvenida_LasColumnasTienenLaFormaDeLa0076 es el test aburrido que
// se pone rojo primero si alguien toca la sección B: la forma que el SELECT de NUEVE
// columnas de GetTenantSettings da por hecha.
//
// SALIDAS ESPERADAS (information_schema.columns, tabla tenant_settings):
//
//	column_name             | data_type         | is_nullable | column_default
//	welcome_text            | text              | NO          | ''::text
//	welcome_silence_seconds | integer           | NO          | 86400
//
// Y además: el CHECK con nombre propio existe y MUERDE ante un negativo.
func TestIntegration_Bienvenida_LasColumnasTienenLaFormaDeLa0076(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	casos := []struct {
		columna       string
		tipo          string
		porDefecto    string
		porQueImporta string
	}{
		{
			columna:    "welcome_text",
			tipo:       "text",
			porDefecto: "''::text",
			porQueImporta: "sin el DEFAULT '' una fila preexistente traería NULL, el Scan a string " +
				"reventaría y GetTenantSettings fallaría para todo tenant con config anterior a esta ola",
		},
		{
			columna:    "welcome_silence_seconds",
			tipo:       "integer",
			porDefecto: "86400",
			porQueImporta: "un 0 aquí significa VENCIDO SIEMPRE: si el ADD COLUMN se aplicara sin default, " +
				"Postgres rellenaría con el cero del tipo y la bienvenida saldría en CADA mensaje de todo " +
				"tenant preexistente, que es exactamente lo que el enunciado prohíbe",
		},
	}
	for _, c := range casos {
		var tipo, nullable string
		var porDefecto sql.NullString
		err := db.QueryRowContext(ctx, `
			SELECT data_type, is_nullable, column_default
			  FROM information_schema.columns
			 WHERE table_schema = 'public'
			   AND table_name   = 'tenant_settings'
			   AND column_name  = $1
		`, c.columna).Scan(&tipo, &nullable, &porDefecto)
		if err != nil {
			t.Fatalf("la columna %s no aparece en information_schema: %v. ESPERADO una fila "+
				"(%s | NO | %s): sin ella la 0076 sección B no se aplicó y el SELECT de "+
				"GetTenantSettings no puede funcionar", c.columna, err, c.tipo, c.porDefecto)
		}
		if tipo != c.tipo {
			t.Fatalf("%s: data_type = %q; ESPERADO %q", c.columna, tipo, c.tipo)
		}
		if nullable != "NO" {
			t.Fatalf("%s: is_nullable = %q; ESPERADO \"NO\" — la columna es NOT NULL y el Scan de "+
				"GetTenantSettings lee a un tipo no-puntero", c.columna, nullable)
		}
		if !porDefecto.Valid || porDefecto.String != c.porDefecto {
			t.Fatalf("%s: column_default = %q (valid=%v); ESPERADO %q. %s",
				c.columna, porDefecto.String, porDefecto.Valid, c.porDefecto, c.porQueImporta)
		}
	}

	// El CHECK, comprobado por EJECUCIÓN y no leyendo el catálogo: lo que importa no es
	// que la restricción exista, es que RECHACE. Un CHECK inline dentro de un
	// `ADD COLUMN IF NOT EXISTS` deja de recrearse del segundo arranque en adelante
	// (regla 4 del patrón full-replay) y este INSERT pasaría en verde.
	//
	// SALIDA ESPERADA: error que menciona `tenant_settings_welcome_silence_check`.
	huerfano := uuid.NewString()
	_, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size, welcome_silence_seconds)
		VALUES ($1, 5, -1)
	`, huerfano)
	if err == nil {
		if _, derr := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, huerfano); derr != nil {
			t.Logf("limpiando la fila que NO debió entrar: %v", derr)
		}
		t.Fatal("un welcome_silence_seconds = -1 ENTRÓ en la tabla; ESPERADO que el CHECK " +
			"tenant_settings_welcome_silence_check lo rechazara. Un negativo no significa «poco " +
			"silencio»: no significa nada")
	}
	if !strings.Contains(err.Error(), "tenant_settings_welcome_silence_check") {
		t.Fatalf("el INSERT con -1 falló, pero por otra cosa: %v. ESPERADO que el error nombre "+
			"tenant_settings_welcome_silence_check", err)
	}
}

// TestIntegration_Bienvenida_FilaQueNoLasNombraHeredaLosDefaults es el camino del
// BACKFILL, y aquí el backfill ES el default (no hay UPDATE aparte: no existe ninguna
// columna anterior de la que derivar un valor mejor, exactamente el caso de la 0072
// sección E y lo contrario del techo de la sección A).
//
// 🔴 SE FABRICA EL ESTADO ANTERIOR y no se prueba contra una base recién migrada: en
// una base virgen `tenant_settings` puede estar vacía y el barrido saldría verde por
// CERO filas. Se dropean las columnas, se siembra una fila «vieja» y se reejecuta la
// migración encima, invalidando el hash para que el runner no conteste `Skipped`.
//
// SALIDAS ESPERADAS, tras el replay:
//   - welcome_text            de la fila vieja = ”    (cadena vacía, NO NULL)
//   - welcome_silence_seconds de la fila vieja = 86400 (NO 0)
//   - filas con welcome_silence_seconds = 0 en toda la tabla: 0
func TestIntegration_Bienvenida_FilaQueNoLasNombraHeredaLosDefaults(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// (1) EL ESTADO ANTERIOR: las columnas no existen. El DROP se lleva por delante
	// también el CHECK; el replay lo recrea (regla 4).
	for _, col := range []string{"welcome_text", "welcome_silence_seconds"} {
		if _, err := db.ExecContext(ctx,
			`ALTER TABLE public.tenant_settings DROP COLUMN IF EXISTS `+col); err != nil {
			t.Fatalf("fabricando el estado anterior (drop de %s): %v", col, err)
		}
	}
	viejo := uuid.NewString()
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, viejo); err != nil {
			t.Logf("limpiando tenant_settings de %s: %v", viejo, err)
		}
	})
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size) VALUES ($1, 5)
	`, viejo); err != nil {
		t.Fatalf("sembrando la fila del estado anterior: %v", err)
	}

	// (2) EL ARRANQUE QUE APLICA LA 0076 ENCIMA. Sin invalidar el hash, Migrate diría
	// Skipped y este test saldría HUECO.
	invalidaElHashDelEsquema(t, db)
	res, err := migrations.Migrate(ctx, db)
	if err != nil {
		t.Fatalf("reejecutando la migración sobre el estado anterior: %v", err)
	}
	if res.Skipped {
		t.Fatal("Migrate devolvió Skipped=true: el full-replay NO corrió y este test no probó nada")
	}

	// (3) LAS COLUMNAS CRUDAS de la fila vieja. Se leen por SQL directo y no por
	// GetTenantSettings porque solo el SELECT crudo distingue «la BD puso el valor» de
	// «Go rellenó el hueco».
	var texto string
	var silencio int
	if err := db.QueryRowContext(ctx, `
		SELECT welcome_text, welcome_silence_seconds
		  FROM public.tenant_settings WHERE tenant_id = $1
	`, viejo).Scan(&texto, &silencio); err != nil {
		t.Fatalf("leyendo las columnas crudas de la fila vieja: %v", err)
	}
	if texto != "" {
		t.Fatalf("welcome_text (cruda) = %q; ESPERADO \"\" — el DEFAULT de la columna, que significa "+
			"«el texto de plataforma»", texto)
	}
	if silencio != 86400 {
		t.Fatalf("welcome_silence_seconds (cruda) = %d; ESPERADO 86400. Un 0 aquí significa que el "+
			"ADD COLUMN se aplicó SIN default y Postgres rellenó con el cero del tipo: la bienvenida "+
			"saldría en CADA mensaje de todo tenant preexistente", silencio)
	}

	// (4) EL BARRIDO: ni un solo tenant en 0 que nadie haya puesto ahí. Es la misma
	// aserción que la (V4) de la 0072, y por el mismo motivo: 0 satisface el CHECK, así
	// que un descuido no daría error, solo cambiaría la conducta de todo el parque.
	var enCero int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM public.tenant_settings WHERE welcome_silence_seconds = 0`).Scan(&enCero); err != nil {
		t.Fatalf("contando tenants con el umbral en 0: %v", err)
	}
	if enCero != 0 {
		t.Fatalf("hay %d tenants con welcome_silence_seconds = 0 y nadie los puso ahí; "+
			"ESPERADO 0 (el 0 es legítimo pero tiene que ser una ELECCIÓN)", enCero)
	}
}

// TestIntegration_Bienvenida_GetTenantSettings_LeeLasDosColumnas cierra el camino del
// repositorio, con sus DOS mitades —que son dos caminos distintos del método— y con la
// asimetría del ” escrita como aserción.
//
// SALIDAS ESPERADAS:
//   - SIN fila ......... WelcomeText == store.DefaultWelcomeText, WelcomeSilence == 24h
//   - CON fila y ” .... WelcomeText == ""   ← TAL CUAL: el repositorio NO inventa
//   - CON fila y texto . WelcomeText == ese texto
//   - CON fila y 0 ..... WelcomeSilence == 0 ← el override explícito, NO 24h
//
// 🔴 LA TERCERA LÍNEA ES LA QUE SORPRENDE Y ESTÁ BIEN ASÍ. `GetTenantSettings` promete
// devolver lo que diga la fila sin sustituir nada, y a esta columna no se le hace una
// excepción: quien traduce el ” al texto de plataforma es el runtime, en un solo sitio
// (runtime/welcome.go · textoDeBienvenida). Si algún día este test empieza a esperar
// DefaultWelcomeText aquí, es que la traducción se duplicó.
func TestIntegration_Bienvenida_GetTenantSettings_LeeLasDosColumnas(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)

	// (a) SIN FILA: mandan los defaults de Go (DefaultTenantSettings), que es el camino
	// de la mayoría de los tenants (el comentario de store.go dice 2 de 3 en UAT).
	sinFila, err := repo.GetTenantSettings(ctx, uuid.NewString())
	if err != nil {
		t.Fatalf("GetTenantSettings (sin fila) = error %v; ESPERADO nil", err)
	}
	if sinFila.WelcomeText != store.DefaultWelcomeText {
		t.Fatalf("WelcomeText sin fila = %q; ESPERADO store.DefaultWelcomeText (%q)",
			sinFila.WelcomeText, store.DefaultWelcomeText)
	}
	if sinFila.WelcomeSilence != store.DefaultWelcomeSilence {
		t.Fatalf("WelcomeSilence sin fila = %v; ESPERADO %v (store.DefaultWelcomeSilence). Un 0 aquí "+
			"NO es «sin configurar»: significa VENCIDO SIEMPRE, o sea la bienvenida en cada mensaje "+
			"para todos los tenants que no tienen fila", sinFila.WelcomeSilence, store.DefaultWelcomeSilence)
	}

	// (b) CON FILA: manda la fila, incluidos la cadena vacía y el 0.
	vacio := conBienvenidaConfigurada(t, db, "", 0)
	if vacio.WelcomeText != "" {
		t.Fatalf("WelcomeText con fila en '' = %q; ESPERADO \"\" TAL CUAL. El repositorio no traduce: "+
			"quien convierte '' en el texto de plataforma es runtime/welcome.go, en UN solo sitio",
			vacio.WelcomeText)
	}
	if vacio.WelcomeSilence != 0 {
		t.Fatalf("WelcomeSilence con fila en 0 = %v; ESPERADO 0s. El 0 es el override EXPLÍCITO "+
			"«vencido siempre» (CHECK >= 0 de la 0076), no un hueco que rellenar", vacio.WelcomeSilence)
	}

	// El caso NO degenerado va en el mismo test a propósito: sin él, un método que
	// devolviera siempre el cero de cada tipo pasaría las dos aserciones de arriba.
	propio := conBienvenidaConfigurada(t, db, "Gracias, ya lo estamos viendo.", 7200)
	if propio.WelcomeText != "Gracias, ya lo estamos viendo." {
		t.Fatalf("WelcomeText con fila propia = %q; ESPERADO el texto sembrado — si sale vacío, el "+
			"método no está leyendo la columna", propio.WelcomeText)
	}
	if propio.WelcomeSilence != 2*time.Hour {
		t.Fatalf("WelcomeSilence con fila en 7200 = %v; ESPERADO 2h", propio.WelcomeSilence)
	}
}

// conBienvenidaConfigurada siembra una fila de tenant_settings NOMBRANDO
// EXPLÍCITAMENTE las dos columnas de la bienvenida y devuelve lo que el repositorio lee
// de ella. Nombrarlas es el punto: una fila que las omitiera recibiría los DEFAULT del
// esquema y no probaría nada sobre el override.
//
// Es un helper de PAQUETE y no un cierre dentro del test —mismo molde que
// conVentanaDeAgregacion— porque el `t.Cleanup` tiene que usar su propio
// context.Background(): el ctx del test puede estar cancelado cuando el cleanup corre, y
// `contextcheck` marca en rojo un Background() pasado desde una función que ya tiene ctx.
func conBienvenidaConfigurada(t *testing.T, db *sql.DB, texto string, silencio int) store.TenantSettings {
	t.Helper()
	ctx := context.Background()
	tenant := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO public.tenant_settings (tenant_id, page_size, welcome_text, welcome_silence_seconds)
		VALUES ($1, 5, $2, $3)
	`, tenant, texto, silencio); err != nil {
		t.Fatalf("sembrando tenant_settings (welcome_text=%q, silencio=%d): %v", texto, silencio, err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(),
			`DELETE FROM public.tenant_settings WHERE tenant_id = $1`, tenant); err != nil {
			t.Logf("limpiando tenant_settings de %s: %v", tenant, err)
		}
	})
	got, err := store.NewPostgresRepository(db).GetTenantSettings(ctx, tenant)
	if err != nil {
		t.Fatalf("GetTenantSettings(%s): %v", tenant, err)
	}
	return got
}

// ---------------------------------------------------------------------------
// (C) LA TABLA DEL ESTADO: `conversation_welcomes`
// ---------------------------------------------------------------------------

// TestIntegration_Bienvenida_LaTablaTieneLaFormaDeLa0076 fija la forma de la sección C,
// y en particular las dos decisiones que un `ADD COLUMN` distraído destruiría.
//
// SALIDAS ESPERADAS (information_schema.columns, tabla conversation_welcomes):
//
//	column_name      | is_nullable | column_default
//	last_incoming_at | NO          | <VACÍO>
//	welcomed_at      | YES         | <VACÍO>
//
// ⚠️ LOS DOS `column_default` VACÍOS SON EL PUNTO:
//   - un `DEFAULT now()` en `last_incoming_at` metería el reloj de POSTGRES en una
//     comparación que hace Go (rt.now, WithClock). Es el fallo permanente y silencioso
//     de comparar dos relojes, y aquí se manifestaría como un umbral de silencio que
//     mide mal por la deriva entre las dos máquinas.
//   - un `DEFAULT now()` en `welcomed_at` —el molde tentador— AFIRMARÍA que a toda
//     conversación se le saludó ya, y la bienvenida no saldría nunca. Es el mismo
//     argumento, palabra por palabra, que la 0066 escribió para greeted_at.
func TestIntegration_Bienvenida_LaTablaTieneLaFormaDeLa0076(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	casos := []struct {
		columna  string
		nullable string
		porQue   string
	}{
		{"last_incoming_at", "NO", "es el ancla del silencio y el runtime la escribe SIEMPRE"},
		{"welcomed_at", "YES", "NULL = nunca se saludó, y ese es el estado CORRECTO de una conversación nueva"},
	}
	for _, c := range casos {
		var nullable string
		var porDefecto sql.NullString
		err := db.QueryRowContext(ctx, `
			SELECT is_nullable, column_default
			  FROM information_schema.columns
			 WHERE table_schema = 'public'
			   AND table_name   = 'conversation_welcomes'
			   AND column_name  = $1
		`, c.columna).Scan(&nullable, &porDefecto)
		if err != nil {
			t.Fatalf("la columna %s no aparece en information_schema: %v. ESPERADO una fila: sin ella "+
				"la 0076 sección C no se aplicó y la bienvenida no tiene dónde guardar su estado",
				c.columna, err)
		}
		if nullable != c.nullable {
			t.Fatalf("%s: is_nullable = %q; ESPERADO %q (%s)", c.columna, nullable, c.nullable, c.porQue)
		}
		if porDefecto.Valid {
			t.Fatalf("%s tiene column_default = %q; ESPERADO NINGUNO. Un default aquí mete el reloj de "+
				"Postgres donde el runtime pone el suyo, o afirma un saludo que no ocurrió (ver la 0066)",
				c.columna, porDefecto.String)
		}
	}
}

// TestIntegration_Bienvenida_TouchContactDevuelveLaFilaANTERIOR es EL test de este
// fichero: el que no se puede escribir en memoria y el que caza el «simplifiquemos el
// CTE a un RETURNING».
//
// SECUENCIA Y SALIDAS ESPERADAS (T1 < T2 < T3):
//
//	TouchContact(T1) → {LastIncomingAt: cero,  WelcomedAt: cero}   ← no había fila
//	TouchContact(T2) → {LastIncomingAt: T1,    WelcomedAt: cero}   ← 🔴 T1, NO T2
//	MarkWelcomed(testigo=cero, T2) → true
//	TouchContact(T3) → {LastIncomingAt: T2,    WelcomedAt: T2}
//
// 🔴 LA SEGUNDA LÍNEA ES TODO. Con un `RETURNING` sobre el `ON CONFLICT DO UPDATE`
// saldría T2 —la fila YA actualizada—, el silencio mediría siempre 0 y la bienvenida no
// volvería NUNCA después de la primera. Ese defecto no da error, no deja log y solo se
// ve con un reloj falso y horas de diferencia: es exactamente la clase de fallo que
// esta casa persigue.
func TestIntegration_Bienvenida_TouchContactDevuelveLaFilaANTERIOR(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	key := claveDeBienvenida(t, db)

	// Instantes con microsegundos exactos: Postgres guarda timestamptz con precisión de
	// microsegundo, así que un `time.Now()` con nanosegundos NO round-trip-ea y las
	// comparaciones fallarían por una diferencia que no significa nada.
	t1 := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(90 * time.Second)
	t3 := t2.Add(30 * time.Hour)

	primero, err := repo.TouchContact(ctx, key, t1)
	if err != nil {
		t.Fatalf("TouchContact(T1): %v", err)
	}
	if !primero.LastIncomingAt.IsZero() || !primero.WelcomedAt.IsZero() {
		t.Fatalf("TouchContact sobre una conversación NUEVA devolvió %+v; ESPERADO la marca CERO "+
			"(«nunca habló, nunca se le saludó»): no había fila que leer", primero)
	}

	segundo, err := repo.TouchContact(ctx, key, t2)
	if err != nil {
		t.Fatalf("TouchContact(T2): %v", err)
	}
	if !segundo.LastIncomingAt.Equal(t1) {
		t.Fatalf("TouchContact(T2).LastIncomingAt = %v; ESPERADO T1 (%v), o sea la fila ANTERIOR. "+
			"Si sale T2, el CTE `previo` está leyendo la fila YA actualizada (o se sustituyó por un "+
			"RETURNING) y el umbral de silencio medirá 0 para siempre",
			segundo.LastIncomingAt, t1)
	}
	if !segundo.WelcomedAt.IsZero() {
		t.Fatalf("TouchContact(T2).WelcomedAt = %v; ESPERADO cero: nadie ha saludado todavía",
			segundo.WelcomedAt)
	}

	marcada, err := repo.MarkWelcomed(ctx, key, segundo, t2)
	if err != nil {
		t.Fatalf("MarkWelcomed(testigo cero): %v", err)
	}
	if !marcada {
		t.Fatal("MarkWelcomed con el testigo CERO devolvió false; ESPERADO true. Es la PRIMERA " +
			"bienvenida: el centinela compara contra NULL, y por eso es `IS NOT DISTINCT FROM` y no " +
			"`=` (con `=` no casaría ninguna fila y la primera bienvenida no se marcaría jamás)")
	}

	tercero, err := repo.TouchContact(ctx, key, t3)
	if err != nil {
		t.Fatalf("TouchContact(T3): %v", err)
	}
	if !tercero.LastIncomingAt.Equal(t2) {
		t.Fatalf("TouchContact(T3).LastIncomingAt = %v; ESPERADO T2 (%v)", tercero.LastIncomingAt, t2)
	}
	if !tercero.WelcomedAt.Equal(t2) {
		t.Fatalf("TouchContact(T3).WelcomedAt = %v; ESPERADO T2 (%v) — el toque NO puede pisar la "+
			"marca de la bienvenida: son dos hechos distintos y el UPDATE del CTE solo toca "+
			"last_incoming_at", tercero.WelcomedAt, t2)
	}
}

// TestIntegration_Bienvenida_MarkWelcomedEsUnCompareAndSet fija el centinela: si la
// marca cambió desde que el llamante la leyó, NO se escribe y se devuelve false SIN
// error.
//
// Por qué no basta un `WHERE welcomed_at IS NULL` (que es lo que hace la 0066 con
// `greeted_at`): allí la marca se pone UNA vez y para siempre; aquí vuelve a ponerse
// cada vez que el contacto reaparece tras el silencio, así que un `IS NULL` solo
// protegería la PRIMERA bienvenida y dejaría todas las demás sin centinela.
//
// SALIDAS ESPERADAS:
//
//	MarkWelcomed(testigo AL DÍA)   → true,  y welcomed_at pasa a valer el nuevo instante
//	MarkWelcomed(testigo RANCIO)   → false, y welcomed_at NO se mueve
//	MarkWelcomed(sin fila)         → false, SIN error (no la crea: la crea TouchContact)
func TestIntegration_Bienvenida_MarkWelcomedEsUnCompareAndSet(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := store.NewPostgresRepository(db)
	key := claveDeBienvenida(t, db)

	t1 := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(25 * time.Hour)
	t3 := t2.Add(25 * time.Hour)

	if _, err := repo.TouchContact(ctx, key, t1); err != nil {
		t.Fatalf("TouchContact inicial: %v", err)
	}
	if ok, err := repo.MarkWelcomed(ctx, key, store.WelcomeMark{}, t1); err != nil || !ok {
		t.Fatalf("primera MarkWelcomed: ok=%v err=%v; ESPERADO true, nil", ok, err)
	}
	// El testigo RANCIO: alguien que leyó la marca ANTES de la primera bienvenida y
	// llega tarde. En producción esto son dos instancias del Cloud sobre la misma base
	// (dentro de un proceso, el keyedMutex de la clave lo impide).
	rancio, err := repo.MarkWelcomed(ctx, key, store.WelcomeMark{}, t2)
	if err != nil {
		t.Fatalf("MarkWelcomed con testigo rancio devolvió error %v; ESPERADO (false, nil): perder la "+
			"carrera NO es un fallo", err)
	}
	if rancio {
		t.Fatal("MarkWelcomed con un testigo RANCIO devolvió true; ESPERADO false. El centinela no " +
			"está comparando contra el valor leído y la bienvenida se puede marcar dos veces")
	}
	var enBD time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT welcomed_at FROM public.conversation_welcomes
		 WHERE tenant_id = $1 AND session_id = $2 AND contact_id = $3
	`, key.TenantID, key.SessionID, key.ContactID).Scan(&enBD); err != nil {
		t.Fatalf("leyendo welcomed_at: %v", err)
	}
	if !enBD.Equal(t1) {
		t.Fatalf("welcomed_at = %v tras el intento rancio; ESPERADO que siguiera en T1 (%v)", enBD, t1)
	}

	// El testigo AL DÍA sí escribe: es el caso del contacto que vuelve tras el silencio.
	alDia := store.WelcomeMark{LastIncomingAt: t1, WelcomedAt: t1}
	if ok, err := repo.MarkWelcomed(ctx, key, alDia, t3); err != nil || !ok {
		t.Fatalf("MarkWelcomed con testigo al día: ok=%v err=%v; ESPERADO true, nil — la segunda "+
			"bienvenida tiene que poder marcarse, que es justo lo que un `WHERE welcomed_at IS NULL` "+
			"impediría", ok, err)
	}

	// Y sin fila: false, sin error y SIN crearla. Fabricarla aquí taparía un orden de
	// llamadas equivocado (MarkWelcomed solo puede venir después de TouchContact).
	otra := claveDeBienvenida(t, db)
	ok, err := repo.MarkWelcomed(ctx, otra, store.WelcomeMark{}, t1)
	if err != nil {
		t.Fatalf("MarkWelcomed sin fila devolvió error %v; ESPERADO (false, nil)", err)
	}
	if ok {
		t.Fatal("MarkWelcomed sin fila devolvió true; ESPERADO false: el UPDATE no afecta ninguna fila")
	}
	var filas int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM public.conversation_welcomes WHERE tenant_id = $1
	`, otra.TenantID).Scan(&filas); err != nil {
		t.Fatalf("contando filas de la conversación sin tocar: %v", err)
	}
	if filas != 0 {
		t.Fatalf("MarkWelcomed CREÓ %d fila(s) sin que nadie hubiera tocado la conversación; ESPERADO 0",
			filas)
	}
}

// claveDeBienvenida siembra un tenant real (la FK de conversation_welcomes es a
// tenants, con CASCADE) y devuelve una clave conversacional nueva. El contact_id es un
// UUID porque la columna lo es: es la identidad OPACA de contacts.contact_id, no un
// teléfono.
//
// No hace falta limpiar `conversation_welcomes`: el `ON DELETE CASCADE` del tenant se
// lleva sus filas, y `seedTenant` crea uno nuevo por llamada.
func claveDeBienvenida(t *testing.T, db *sql.DB) store.Key {
	t.Helper()
	return store.Key{
		TenantID:  seedTenant(t, db),
		SessionID: "sess-bienvenida",
		ContactID: uuid.NewString(),
	}
}
