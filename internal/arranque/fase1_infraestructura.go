// Copia de internal/bootstrap/arranque/fase1_infraestructura.go @ 80807ba (F0 · 05 §6): cableaba paquetes VIEJOS.
// 🔀 F8 · conmutar(conversacion): ya no cablea ninguno; desde F8 el arranque nuevo es todo módulos nuevos.
package arranque

import (
	"context"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/metrics"
)

// faseInfraestructura construye lo que no es dominio y sin lo cual no hay dominio: el
// registro de métricas, el pool de PostgreSQL con sus migraciones, el material PKI, la
// clave del lease y el servidor de enrolamiento.
//
// Todo lo de aquí es FAIL-FAST: si algo falla, el proceso muere en vez de arrancar
// degradado. Un servidor a medias que acepta tráfico es peor que uno que no levanta,
// porque el segundo se nota.
type faseInfraestructura struct{}

func (faseInfraestructura) nombre() string { return "infraestructura" }

// requiere nada: es la primera y solo consume cfg y log, que existen desde el prólogo.
func (faseInfraestructura) requiere() []string { return nil }

func (faseInfraestructura) ejecutar(ctx context.Context, c *contenedor) error {
	// Observabilidad Prometheus (Plan 018 · T10, R11): registry propio compartido
	// por los dos listeners HTTP (métricas de request/latencia/login/rate-limit) y
	// el sink de acuses. /metrics se sirve en el listener admin (:8100), en la fase
	// de transporte.
	c.mtx = metrics.New()

	db, err := setupDatabase(ctx, c.cfg, c.log)
	if err != nil {
		return err
	}
	// El cierre lo hace el contenedor al terminar Ejecutar, y cubre también un fallo
	// de cualquier fase posterior a ésta.
	c.db = db

	// El pool de conexiones, en /metrics (Plan 050 · T4.3): las seis series
	// wapp_db_* — entre ellas WaitCount y WaitDuration, la prueba DIRECTA de que
	// alguien esperó por una conexión, que este proyecto no había medido nunca.
	// Las lee T5.5 para levantar la curva con la que T4.6 decide DEUDA-050.2.
	//
	// Va AQUÍ y no dentro de metrics.New() porque cuando se construyen las
	// métricas el *sql.DB todavía no existe: la base se abre unas líneas más
	// arriba. Registrar sobre el registry ya creado es legal (prometheus.Registry
	// se protege por dentro), y es la primera vez que este repo lo hace.
	//
	// Se aborta el arranque si falla: el único error posible es un choque de
	// nombres en el registry, o sea un bug de programación, y esos se ven al
	// arrancar o no se ven nunca. Un fallo de la base no llega hasta aquí.
	if err := c.mtx.RegisterDBStats(c.db); err != nil {
		return err
	}

	// --- PKI: CA firmante (enroll) + Pool (mTLS) + cert de servidor (ambos). ---
	ca, serverCert, err := loadPKI(c.cfg)
	if err != nil {
		return err
	}
	c.ca, c.serverCert = ca, serverCert

	// --- Lease (kill-switch): clave de firma + persistencia en PostgreSQL.
	// Construido ANTES que el servidor de enrolamiento (Plan 055 · T4.2,
	// D-055.5): el enrolamiento necesita leaseMgr.PublicKey() para publicarla
	// al Edge en EnrollEdgeResponse.lease_pubkey (H-5). Sin este orden, la
	// pública del lease no existiría todavía cuando buildEnrollServer la
	// necesita. buildLeaseManager solo depende de cfg/db/log —construidos
	// arriba (setupDatabase)— así que adelantarla es seguro: no usa nada de lo
	// que antes se construía entre medias (enrollSrv, cloudEncPriv).
	leaseMgr, err := buildLeaseManager(c.cfg, c.db, c.log)
	if err != nil {
		return err
	}
	c.leaseMgr = leaseMgr

	// --- Enrolamiento + par X25519 de cifrado de tránsito de la nube (Plan 011
	// §10.F): el enrolamiento publica la pública al Edge; la privada la usa el
	// gateway para abrir el enc_payload sellado al ingreso. También publica la
	// pública del lease (Plan 055 · T4.2), ya disponible por el reordenamiento
	// de arriba. ---
	enrollSrv, cloudEncPriv, err := buildEnrollServer(c.cfg, c.db, c.ca, c.leaseMgr.PublicKey(), c.log)
	if err != nil {
		return err
	}
	c.enrollSrv, c.cloudEncPriv = enrollSrv, cloudEncPriv

	c.marca("db", "metricas", "pki", "lease", "enroll")
	return nil
}
