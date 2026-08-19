package gatewaygrpc_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/gateway/lease"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/storage/postgres"
)

// ============================================================================
// DEUDA-050.2 · La reproducción DETERMINISTA del pool que se queda corto.
//
// QUÉ DEUDA REPRODUCE
// Antes del Plan 050 el bucle Recv del gateway hacía el trabajo en SERIE: por
// muchas sesiones que trajera un stream, había UN solo demandante de conexión a
// la vez y el pool nunca se tensaba. La Ola 1 metió un carril por sesión
// (worklane.go), así que N sesiones latiendo son N escritores SIMULTÁNEOS
// contra un pool cuyo default sigue siendo 25 (postgres.DefaultMaxOpenConns).
// Quien no consigue conexión espera DENTRO del presupuesto de su job; quien lo
// agota muere en la consulta de revocación del tenant y renewLease se rinde con
// exactamente esto en el log:
//
//	msg="lease: renovar" error="lease: decidir corte por tenant: lease: consultar revocación del tenant: context deadline exceeded"
//
// El Edge no recibe su LeaseUpdate y lo reintenta al siguiente latido. Es
// fail-closed: cuesta un latido y ruido, no seguridad.
//
// 🔴 Lo que escala NO son las sesiones por Edge, es la FLOTA. Un Edge real trae
// 1-3 sesiones, pero cada Edge trae su propio stream y sus propios carriles
// contra el MISMO pool: 100 Edges × 2 sesiones = 200 escritores contra 25
// conexiones.
//
// POR QUÉ ESTE TEST NO ES EL ESCENARIO DE PRODUCCIÓN
// En producción la deuda solo se manifiesta como FLAKINESS: medido el
// 2026-08-18 (Apple M1 Pro, Postgres 16 en Docker, escenario N=60 de
// TestIntegration_CargaAckBajoHeartbeatsEnVuelo) con MaxOpenConns=25 pasaban 3
// de 6 corridas, y con 90 pasaban 6 de 6. Un test que falla la mitad de las
// veces no prueba nada. Así que aquí NO se intenta reproducir "se pierde un
// LeaseUpdate bajo el presupuesto de 5 s", que depende de lo rápida que sea la
// máquina: se parte la cadena en sus DOS eslabones y se hace determinista cada
// uno por separado, sin depender del reloj de nadie.
//
//	Eslabón 1 — ALGUIEN ESPERA. Pool deliberadamente estrecho y N escritores
//	concurrentes contra Postgres real ⇒ db.Stats().WaitCount > 0. Con 2
//	conexiones y 20 demandantes simultáneos alguien espera SIEMPRE.
//	🔎 Es, además, la primera vez que este proyecto observa WaitCount > 0: la
//	pregunta literal que T5.5 tiene que responder con una curva.
//
//	Eslabón 2 — QUIEN ESPERA DE MÁS SE MUERE. La ÚNICA conexión del pool está
//	reservada, y los jobs traen un presupuesto pequeño ⇒ mueren con
//	context.DeadlineExceeded envueltos en el mensaje de producción, sin haber
//	llegado a tocar Postgres.
//
// POR QUÉ ESTÁ VERDE HOY
// Porque el mecanismo EXISTE: N escritores concurrentes contra un pool más
// estrecho que N producen espera, y la espera consume el presupuesto del job.
// El test no afirma que el default de producción (25) sea insuficiente —eso es
// una curva, no un booleano—; afirma que el MECANISMO por el que se queda corto
// es real y observable.
//
// QUIÉN LA ARREGLA, Y QUÉ HACER CON ESTE TEST ENTONCES
// El arreglo es de T4.6, que está bloqueada A PROPÓSITO hasta que T5.5 levante
// la curva de WaitCount vs. tamaño de flota (ADR-0040 §Decisión.8: primero se
// mide, después se mueve). Este test NO fija el número y NO debe usarse para
// justificarlo.
// Cuando T4.6 aterrice el número:
//   - Si el arreglo es SOLO subir el pool, este test sigue valiendo tal cual: el
//     mecanismo sigue existiendo, solo se ha movido el umbral. Lo que hay que
//     hacer es añadirle al subtest de control el tamaño elegido y comprobar que
//     con la flota objetivo NO hay espera.
//   - Si el arreglo ELIMINA el mecanismo (un semáforo que acote los escritores
//     antes del pool, un pool por carril, backpressure aguas arriba), entonces
//     este test tiene que MORIR: bórralo, no lo adaptes. Un test que reproduce un
//     defecto ya inexistente es un test que nadie se atreve a tocar.
//
// La discriminancia está DENTRO del propio test: el subtest de control abre el
// mismo martilleo contra un pool ancho y exige WaitCount == 0. Si alguien
// borrara el estrechamiento, el eslabón 1 se caería; si WaitCount subiera por
// cualquier otro motivo que no fuera el tope del pool, se caería el control.
//
// 🔴 INV-050.6: aquí NO se mueve ningún timeout de producción. Los pools
// estrechos y los presupuestos pequeños son DEL TEST, se construyen a mano en
// este fichero y no tocan ni postgres.Default* ni el workBudget del Server.
// ============================================================================

const (
	// poolEstrechoDeuda es el tope de conexiones del eslabón 1. Dos, y no uno,
	// para que el pool siga siendo un pool (hay concurrencia real contra la BD) y
	// la espera no sea un artefacto de haberlo dejado en serie.
	poolEstrechoDeuda = 2
	// poolAnchoDeuda es el control: MUY por encima de escritoresDeuda, de modo
	// que ningún demandante pueda encontrarse el tope. Es el que demuestra que la
	// aserción del eslabón 1 discrimina en vez de estar siempre verde.
	poolAnchoDeuda = 64
	// escritoresDeuda son los demandantes simultáneos del eslabón 1. Modelan los
	// carriles de la FLOTA (cada sesión de cada Edge es un escritor), no las
	// sesiones de un Edge.
	escritoresDeuda = 20
	// consultasPorEscritorDeuda hace que el martilleo no dependa de que las 20
	// goroutines coincidan en el mismo instante: 20 × 25 = 500 adquisiciones de
	// conexión contra un tope de 2. La espera deja de ser probable y pasa a ser
	// aritmética.
	consultasPorEscritorDeuda = 25
	// hambrientosDeuda son los jobs del eslabón 2 que se quedan sin conexión.
	// Bastarían menos; 8 hace que el fallo diga "todos" en vez de "el único".
	hambrientosDeuda = 8
	// presupuestoJobDeuda es el presupuesto ARTIFICIALMENTE PEQUEÑO de los jobs
	// del eslabón 2 (producción usa 5 s: defaultWorkBudget). 50 ms está elegido
	// por dos razones, y ninguna es "cabe justo":
	//  1. Es ~2 órdenes de magnitud MÁS de lo que tarda esa consulta —un SELECT
	//     de una fila por clave primaria— sobre una conexión ya caliente. El
	//     último paso del eslabón 2 lo COMPRUEBA en vez de suponerlo: repite la
	//     misma consulta con el MISMO presupuesto sobre el pool ya libre, y si no
	//     sobrara, el test se declara inválido en vez de dar un falso positivo.
	//  2. No es de él de quien depende el determinismo. La conexión se libera
	//     DESPUÉS de que todos los jobs hayan vuelto, así que ningún job puede
	//     conseguirla por rápida que sea la máquina. 50 ms solo decide lo que
	//     tarda el test, no si pasa.
	presupuestoJobDeuda = 50 * time.Millisecond
	// holguraDeuda es el presupuesto de todo lo que NO está bajo medición
	// (sembrar, reservar la conexión, el martilleo del eslabón 1). Generoso a
	// propósito: si algo de esto vence, es un fallo de entorno y así se lee.
	holguraDeuda = 30 * time.Second
)

// TestIntegration_DEUDA0502_ElPoolEstrechoHaceEsperarYVencer reproduce, de forma
// determinista, los dos eslabones de DEUDA-050.2. Ver la cabecera del fichero.
func TestIntegration_DEUDA0502_ElPoolEstrechoHaceEsperarYVencer(t *testing.T) {
	// openTestDB (load_integration_test.go) hace dos cosas que aquí se
	// aprovechan: aplica el gating por WAPP_TEST_DB_DSN / WAPP_TEST_REQUIRE_DB
	// —sin DSN, skip— y deja el esquema migrado. Su pool es ANCHO (90) a
	// propósito; este test no lo usa para medir nada, solo para sembrar. Lo que
	// se mide corre siempre sobre los pools estrechos que abre abajo.
	admin := openTestDB(t)
	tenantID := sembrarTenantDeuda(t, admin)

	t.Run("eslabon1_el_pool_estrecho_hace_esperar", func(t *testing.T) {
		eslabon1PoolEstrecho(t, tenantID)
	})
	t.Run("eslabon1_control_con_pool_ancho_nadie_espera", func(t *testing.T) {
		eslabon1ControlPoolAncho(t, tenantID)
	})
	t.Run("eslabon2_quien_espera_agota_su_presupuesto", func(t *testing.T) {
		eslabon2PresupuestoVencido(t, tenantID)
	})
}

// eslabon1PoolEstrecho es la mitad observable de la deuda: con menos conexiones
// que demandantes, database/sql ENCOLA a quien llega tarde y lo contabiliza en
// WaitCount. Ese contador es la única evidencia directa de que el pool es el
// cuello, y hasta hoy este proyecto nunca lo había mirado.
func eslabon1PoolEstrecho(t *testing.T, tenantID string) {
	t.Helper()
	stats := martillearRevocacion(t, poolEstrechoDeuda, tenantID)
	if stats.WaitCount == 0 {
		t.Fatalf("WaitCount = 0 con MaxOpenConns=%d y %d escritores concurrentes: "+
			"o el pool dejó de ser el cuello, o este test ya no está martilleando. "+
			"Si el mecanismo de DEUDA-050.2 desapareció de verdad, este test debe BORRARSE, no relajarse",
			poolEstrechoDeuda, escritoresDeuda)
	}
	t.Logf("eslabón 1 · pool estrecho (MaxOpenConns=%d, %d escritores × %d consultas): "+
		"WaitCount=%d, WaitDuration=%v, MaxOpenConnections=%d",
		poolEstrechoDeuda, escritoresDeuda, consultasPorEscritorDeuda,
		stats.WaitCount, stats.WaitDuration, stats.MaxOpenConnections)
}

// eslabon1ControlPoolAncho es la prueba de DISCRIMINANCIA, y vive dentro del
// test a propósito: sin ella, un WaitCount > 0 podría venir de cualquier otra
// cosa y la aserción de arriba estaría verde para siempre. Mismo martilleo,
// mismo código, misma BD; lo ÚNICO que cambia es el tope del pool.
func eslabon1ControlPoolAncho(t *testing.T, tenantID string) {
	t.Helper()
	stats := martillearRevocacion(t, poolAnchoDeuda, tenantID)
	if stats.WaitCount != 0 {
		t.Fatalf("WaitCount = %d con MaxOpenConns=%d y solo %d escritores: nadie debería haber esperado. "+
			"La aserción del eslabón 1 no discrimina y la medición no significa nada",
			stats.WaitCount, poolAnchoDeuda, escritoresDeuda)
	}
	t.Logf("eslabón 1 · control con pool ancho (MaxOpenConns=%d): WaitCount=%d (nadie esperó), MaxOpenConnections=%d",
		poolAnchoDeuda, stats.WaitCount, stats.MaxOpenConnections)
}

// eslabon2PresupuestoVencido cierra la cadena: la espera del eslabón 1 se paga
// con el presupuesto del job, y el job que lo agota MUERE en la consulta de
// revocación del tenant, que es exactamente donde muere en producción.
//
// El determinismo no viene de un sleep: viene de que la ÚNICA conexión del pool
// está reservada por este test y no se suelta hasta que todos los jobs han
// vuelto. Ningún job puede conseguirla, por rápida que sea la máquina.
func eslabon2PresupuestoVencido(t *testing.T, tenantID string) {
	t.Helper()
	db := abrirPoolDeuda(t, 1)
	repo := lease.NewPostgresRepository(db)

	ctxReserva, cancelReserva := context.WithTimeout(context.Background(), holguraDeuda)
	defer cancelReserva()
	ocupante, err := db.Conn(ctxReserva)
	if err != nil {
		t.Fatalf("reservando la única conexión del pool: %v", err)
	}
	liberada := false
	defer func() {
		if !liberada {
			if cerr := ocupante.Close(); cerr != nil {
				t.Logf("liberando la conexión reservada: %v", cerr)
			}
		}
	}()

	esperasAntes := db.Stats().WaitCount
	errores := lanzarHambrientos(repo, tenantID)

	vencidos := 0
	for _, err := range errores {
		switch {
		case err == nil:
			t.Errorf("un job consiguió conexión con la única del pool reservada: el arnés no está saturando nada")
		case !errors.Is(err, context.DeadlineExceeded):
			t.Errorf("el job murió por otra causa, no por agotar su presupuesto: %v", err)
		case !strings.Contains(err.Error(), "lease: consultar revocación del tenant"):
			t.Errorf("el job venció fuera de la consulta de revocación del tenant (que es donde muere en producción): %v", err)
		default:
			vencidos++
		}
	}
	if vencidos != hambrientosDeuda {
		t.Fatalf("vencieron %d de %d jobs; se esperaban todos", vencidos, hambrientosDeuda)
	}

	// La aserción que ata la muerte a la ESPERA POR CONEXIÓN y no a otra cosa:
	// cada job que murió tuvo que pasar antes por la cola de connRequest, y
	// database/sql lo contabiliza incluso cuando el contexto vence esperando.
	esperas := db.Stats().WaitCount - esperasAntes
	if esperas < int64(hambrientosDeuda) {
		t.Fatalf("solo %d de %d jobs llegaron a esperar por una conexión: murieron por otra cosa", esperas, hambrientosDeuda)
	}
	t.Logf("eslabón 2 · %d/%d jobs muertos con context.DeadlineExceeded dentro de la consulta de revocación "+
		"(presupuesto %v, pool MaxOpenConns=1 con su única conexión reservada); esperas por conexión: %d",
		vencidos, hambrientosDeuda, presupuestoJobDeuda, esperas)

	if err := ocupante.Close(); err != nil {
		t.Fatalf("liberando la conexión reservada: %v", err)
	}
	liberada = true

	// CONTROL del eslabón 2: la MISMA consulta con el MISMO presupuesto sobre el
	// pool ya libre. Si esto no sobrara, lo de arriba no probaría contención sino
	// que 50 ms es poco para la consulta, y el test estaría mintiendo.
	ctxControl, cancelControl := context.WithTimeout(context.Background(), presupuestoJobDeuda)
	defer cancelControl()
	inicio := time.Now()
	if _, err := repo.TenantRevoked(ctxControl, tenantID); err != nil {
		t.Fatalf("CONTROL: sin contención, la misma consulta con el mismo presupuesto (%v) tenía que sobrar y falló: %v. "+
			"El eslabón 2 estaría midiendo un presupuesto demasiado corto, no la espera por conexión", presupuestoJobDeuda, err)
	}
	t.Logf("eslabón 2 · control sin contención: la misma consulta con el mismo presupuesto (%v) tardó %v",
		presupuestoJobDeuda, time.Since(inicio))
}

// lanzarHambrientos dispara los jobs del eslabón 2 y devuelve el error de cada
// uno. Cada job lleva SU propio presupuesto, igual que en el carril: el reloj se
// lo pone el job, no el stream (worklane.go).
func lanzarHambrientos(repo *lease.PostgresRepository, tenantID string) []error {
	errores := make([]error, hambrientosDeuda)
	var wg sync.WaitGroup
	for i := range errores {
		wg.Add(1)
		go func(pos int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), presupuestoJobDeuda)
			defer cancel()
			_, err := repo.TenantRevoked(ctx, tenantID)
			errores[pos] = err
		}(i)
	}
	wg.Wait()
	return errores
}

// martillearRevocacion pone escritoresDeuda goroutines a ejecutar la consulta
// REAL de producción (lease.PostgresRepository.TenantRevoked, el mismo camino
// que muere en el log de renewLease) contra un pool de maxOpen conexiones, y
// devuelve las estadísticas del pool. Los presupuestos aquí son amplios: lo que
// se mide es la ESPERA, no el vencimiento (eso es el eslabón 2).
func martillearRevocacion(t *testing.T, maxOpen int, tenantID string) sql.DBStats {
	t.Helper()
	db := abrirPoolDeuda(t, maxOpen)
	repo := lease.NewPostgresRepository(db)

	// Barrera de salida: las goroutines se crean primero y arrancan todas a la
	// vez. Sin ella, arrancar 20 goroutines secuencialmente podría escalonarlas
	// lo bastante como para que el pool nunca se tensara.
	salida := make(chan struct{})
	fallos := make(chan error, escritoresDeuda)
	var wg sync.WaitGroup
	for i := 0; i < escritoresDeuda; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-salida
			for j := 0; j < consultasPorEscritorDeuda; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), holguraDeuda)
				_, err := repo.TenantRevoked(ctx, tenantID)
				cancel()
				if err != nil {
					fallos <- err
					return
				}
			}
		}()
	}
	close(salida)
	wg.Wait()
	close(fallos)
	for err := range fallos {
		t.Fatalf("martilleo con MaxOpenConns=%d: la consulta falló y no debía (el presupuesto es amplio): %v", maxOpen, err)
	}
	return db.Stats()
}

// abrirPoolDeuda abre un pool PROPIO con el tope pedido. No reutiliza el de
// openTestDB a propósito: aquel va a 90 conexiones justamente para que la deuda
// no le afecte, y modificarlo taparía el mecanismo que este test viene a
// enseñar. El DSN es el mismo (ya validado por openTestDB, que corre antes).
func abrirPoolDeuda(t *testing.T, maxOpen int) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Fatalf("%s vacío después de openTestDB: no debería poder ocurrir", dsnEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), holguraDeuda)
	defer cancel()
	// MaxIdleConns = MaxOpenConns para que el pool no cierre y reabra conexiones
	// entre consultas: el churn de handshakes añadiría latencia que no es la que
	// se está midiendo. El esquema NO se migra aquí (ya lo dejó openTestDB).
	db, err := postgres.Open(ctx, postgres.Config{DSN: dsn, MaxOpenConns: maxOpen, MaxIdleConns: maxOpen})
	if err != nil {
		t.Fatalf("abriendo pool de %d conexiones: %v", maxOpen, err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Logf("cerrando pool de %d conexiones: %v", maxOpen, cerr)
		}
	})
	return db
}

// sembrarTenantDeuda crea el tenant sobre el que se consulta la revocación, con
// slug único (la BD del gate es efímera, pero -p 1 comparte proceso y otros
// tests siembran en la misma tabla) y borrado al final para no dejar rastro.
func sembrarTenantDeuda(t *testing.T, db *sql.DB) string {
	t.Helper()
	repo := postgres.NewTenantRepository(db)
	slug := fmt.Sprintf("tenant-deuda-050-2-%d", time.Now().UnixNano())
	ten, err := repo.Create(context.Background(), slug, "DEUDA-050.2 · pool estrecho")
	if err != nil {
		t.Fatalf("sembrando tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, derr := db.Exec(`DELETE FROM public.tenants WHERE id = $1`, ten.ID); derr != nil {
			t.Logf("borrando el tenant sembrado: %v", derr)
		}
	})
	return ten.ID
}
