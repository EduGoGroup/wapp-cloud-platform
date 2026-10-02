// Porta internal/flujos/contact/repository_postgres.go @ 77df20f
// cobertura: adaptador postgres (05 E-6)

package contact

import (
	"context"
	"database/sql"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/crypto"
)

// PostgresResolver implementa Resolver con SQL crudo sobre public.contacts y, en la fusión, sobre
// public.flow_state. Es la implementación que corre en producción: la identidad de los contactos
// vive en Postgres, con el identificador cifrado en reposo. Se construye con NewPostgresResolver y
// es seguro para uso concurrente (cada Resolve abre su propia transacción). La suite
// contacttest.Contrato fija lo que promete junto a MemoryResolver; su SQL solo lo ejercita un
// Postgres real (esa misma suite contra esta implementación y los procesos de F9), no un test
// unitario.
//
// Atomicidad (R-31). Toda Resolve corre en UNA transacción, abierta con postgres.WithTx del paquete
// internal/platform/storage/postgres. Ese helper revierte aunque el cuerpo entre en pánico y
// REINTENTA la transacción entera, con una espera acotada, ante un deadlock (SQLSTATE 40P01) o un
// fallo de serialización (40001). Gracias a ella la fusión (re-apuntar las refs del huérfano al
// canónico y migrar su flow_state) es atómica: ocurre entera o no ocurre. PostgresResolver NO usa
// StateMigrator: esa migración la hace en SQL dentro de su misma transacción.
//
// Cifrado en reposo del identificador (R-23, R-24). La fila de una ref guarda value_bidx,
// value_enc, value_dek y value_kek_id, y no existe una columna con el value en claro: el value solo
// vive en memoria, en el borde de la app, y no se loguea.
//   - value_bidx es el índice ciego, kp.BlindIndex(tenant, value). Con él se busca y se deduplica,
//     y la clave de unicidad de la fila es (tenant_id, kind, value_bidx): el kind es parte de la
//     clave (N-02) y, como el índice depende del tenant, el mismo value en otro tenant da otro
//     índice y otro contacto (N-01).
//   - value_enc es el value cifrado; value_dek, la DEK de ESE valor envuelta con la KEK vigente, y
//     value_kek_id, el id de la KEK que la envolvió. Cada fila lleva su propio kek_id para que, tras
//     una rotación parcial de KEK, siga siendo legible con la KEK que la cerró. En modo compat
//     (una sola KEK maestra) ese id es "1".
//
// Homónimo: la «DEK» de value_dek y de push_name_dek es la del envelope de PII de negocio (una DEK
// fresca por valor, envuelta por la KEK del keyring de este repo). NO es la DEK del ADR-0007, la
// que descifra el almacén de whatsmeow, que custodia el cliente y jamás cruza el contrato.
//
// push_name (R-26, R-27). También va cifrado, en un sobre de TRES piezas (push_name_enc,
// push_name_dek y push_name_kek_id) y SIN índice ciego: nadie busca ni deduplica contactos por su
// nombre de perfil, así que no hay nada que indexar, y un índice sobre texto libre solo añadiría una
// superficie de correlación. Tampoco se normaliza: es texto libre que el dueño del teléfono escribe
// en su perfil, y Normalize destruiría el dato. La invariante de la fila es «las tres piezas o
// ninguna». Un pushName vacío NO se cifra y las tres piezas van a NULL. No es un ahorro: el sobre de
// la cadena vacía dejaría push_name_enc no nulo, el centinela de Resolve (más abajo) no volvería a
// casar y el nombre real que llegara después se perdería para siempre.
//
// No hay lector de push_name, y es deliberado (R-33). Ningún SELECT del repositorio lo trae y el
// nombre no se puebla en ningún tipo de dominio. Añadir un descifrador sin llamador sería probar
// código que nadie ejecuta: un paquete verde que cubre una ruta que ninguna ruta real atraviesa. El
// día que aparezca un lector de verdad (una consola, un export), el patrón es el de Destino: traer
// las tres columnas y abrirlas con el kek_id de LA FILA. Es seguro porque el sobre se escribe con el
// mismo FieldCipher y el mismo KeyProvider que ya abren value_enc.
type PostgresResolver struct{}

// NewPostgresResolver construye el resolver sobre el pool db. cipher cifra y descifra el value y el
// push_name (un envelope por valor) y kp calcula el índice ciego value_bidx.
//
// No valida sus argumentos: devuelve siempre un resolver y no tiene un error en la firma. Un nil se
// descubre en el primer uso que lo necesite, con un pánico, no aquí; Resolve con la lista vacía
// devuelve ErrNoRefs sin tocar ninguno de los tres.
//
// cipher y kp deben ser EL MISMO FieldCipher y EL MISMO KeyProvider que usa el resto del arranque
// (el cipher se construye sobre el kp con crypto.NewFieldCipher). Pasar otro KeyProvider no da
// ningún error: calcula otro value_bidx para la misma ref, así que no encuentra los contactos ya
// guardados y duplica contactos en silencio. Pasar otro keyring deja ilegibles, o hace fallar, las
// filas cerradas con una KEK que ese keyring no tiene.
func NewPostgresResolver(db *sql.DB, cipher *crypto.FieldCipher, kp crypto.KeyProvider) *PostgresResolver {
	panic(pendiente.Implementar("contact.NewPostgresResolver"))
}

// Resolve implementa Resolver.Resolve en UNA transacción (ver PostgresResolver). Cumple lo que
// promete el puerto (deduplicación de refs, creación, reutilización y fusión, R-12 a R-17, y
// contactID "" con error) y, en Postgres, lo siguiente.
//
// Precondición: cada Ref viene de NewRef. Resolve NO la re-normaliza: normalizar otra vez cambiaría
// el value_bidx de las filas que ya existen, y con él a qué contacto pertenece cada ref.
//
// Sin refs (R-18). Con la lista vacía (nil o []Ref{}), tras deduplicar, devuelve ErrNoRefs ANTES de
// abrir la transacción: no toca la base de datos, ni el cifrado, ni el índice ciego. Es el único
// camino de Resolve que se prueba sin Postgres, con un resolver construido con argumentos nil.
//
// Búsqueda, creación y get-or-create (R-32). Busca cada ref por (tenant_id, kind, value_bidx) y
// bloquea con FOR UPDATE las filas que encuentra, para serializar fusiones concurrentes del mismo
// contacto. Si ninguna existe, inserta la primera ref con un contact_id nuevo (el que genera la
// base de datos por defecto) y ata las demás a ese mismo id. El INSERT de la primera ref lleva
// ON CONFLICT (tenant_id, kind, value_bidx) DO UPDATE ... RETURNING contact_id: si otra transacción
// insertó esa ref entre la búsqueda y el INSERT (la carrera get-or-create: el arranque de un flujo
// y un entrante simultáneos), devuelve el contact_id que ya existe en lugar de fallar con duplicate
// key (23505). Las refs que se atan después van con ON CONFLICT DO NOTHING. Por eso es seguro con
// concurrencia: N llamadas simultáneas con la misma ref terminan con UN solo contact_id.
//
// Fusión (R-16, R-17, N-03). Si las refs pertenecen a contact_id distintos, el canónico es el de
// created_at más antiguo (ante un empate, el id menor). Por cada huérfano, y en este orden: borra
// las filas de flow_state del huérfano cuya sesión ya tiene fila del canónico (política de
// conflicto: se CONSERVA la del canónico, la identidad autoritativa); pasa el resto de su
// flow_state al canónico, ya sin colisión de la clave (tenant, sesión, contact_id); y re-apunta al
// canónico las refs de public.contacts del huérfano. Después el huérfano ya no existe. Las tres
// sentencias comparten la transacción de Resolve.
//
// push_name (R-27, R-28). Con pushName no vacío lo sella: en la fila de cada ref que inserta y, para
// un contacto que la búsqueda encontró, con un UPDATE sobre las filas del contacto canónico cuyo
// WHERE lleva el CENTINELA push_name_enc IS NULL. Así el nombre que llega tarde a un contacto creado
// sin él (verdad de campo: «a veces el nombre no llega en los primeros eventos, llega posterior») se
// guarda entonces. Con pushName vacío no toca el nombre. Gana el primer nombre no vacío POR FILA: si
// el cliente se cambia el nombre en WhatsApp, la fila conserva el primero. Qué nombre sobrevive NO es
// parte del puerto (la memoria conserva el último) y pushName nunca cambia el contact_id (N-04).
//
// Una rama NO sella el nombre en lo que ya existía: la carrera get-or-create. Si la búsqueda no
// encontró ninguna ref y el INSERT de la primera choca con la fila que otra transacción acaba de
// insertar, su ON CONFLICT ... DO UPDATE solo toca updated_at, no escribe el sobre en esa fila; y ese
// camino, el de la creación, no ejecuta el UPDATE del centinela, que solo corre cuando la búsqueda
// encontró el contacto. El pushName de esa llamada queda entonces, como mucho, en las filas de las
// demás refs que ella misma inserte. La fila que ganó la carrera conserva lo que tuviera: si nació
// sin nombre, sigue sin él hasta la siguiente Resolve con pushName no vacío que sí encuentre el
// contacto, que lo sella porque el centinela sigue casando.
//
// Por qué un centinela y no una comparación de valores (MD-046.5). Dos cifrados del mismo texto
// nunca son iguales, porque cada escritura usa una DEK y un nonce frescos: un guard por valor
// casaría SIEMPRE y tomaría row-locks sobre todas las filas del contacto en CADA entrante,
// reabriendo el deadlock de más abajo. Con el centinela, tras el primer nombre no vacío el UPDATE
// no casa ninguna fila y no toma ningún lock. El precio es el de arriba, un nombre desactualizado en
// un dato de negocio auxiliar que nadie lee, y está aceptado: no se «arregla» ni se hace que la
// memoria copie esta regla.
//
// Tampoco se evita cifrar un nombre que quizá no se escriba (evaluado y descartado en MD-046.5).
// Saber si el contacto ya tiene sobre exigiría una consulta más después de la fusión, porque la
// búsqueda inicial solo trae el contact_id de las refs presentes en ESTA llamada y el canónico no
// se conoce hasta fundir. Eso es un viaje de ida y vuelta a Postgres más en el camino caliente del
// historial, para ahorrar un cifrado local (AES-GCM y envolver la DEK, sin ninguna llamada al KMS
// por operación) que cuesta microsegundos. Lo que sí se garantiza es lo que importaba: a quien no
// casa el centinela, el WHERE no le toma locks.
//
// Reintento y deadlock (R-29, R-31). Bajo una inundación de historial (un número nuevo hace que
// WhatsApp vuelque su historial) el runtime lanza una goroutine por mensaje entrante y cada una
// llama a Resolve ANTES de tomar el lock por conversación, que se toma después, con el contact_id ya
// resuelto: N transacciones de contactos corren de verdad en paralelo. El deadlock aparece cuando
// dos entrantes del MISMO contacto llegan con identidad parcial y disjunta, porque WhatsApp
// enriquece de forma desigual (uno trae solo el número, otro solo el LID): cada transacción bloquea
// con FOR UPDATE solo la fila de SU ref, pero el UPDATE del nombre va por contact_id y toca TODAS
// las filas del contacto. Una retiene la fila del número y pide la del LID, la otra al revés: ciclo.
//
// No hay una clave común, conocida de antemano, con la que serializarlas (la identidad unificada
// solo se descubre a mitad de la transacción), así que ni ordenar las refs ni un lock previo lo
// evitan. El remedio es dejar que Postgres rompa el ciclo (aborta una de las transacciones) y
// reintentar, y converge porque la transacción es atómica y el upsert idempotente. El centinela
// reduce la ventana: no la elimina. Para reproducir el ciclo en un test contra Postgres hay que
// sembrar el contacto SIN nombre; sembrado con nombre el centinela ya no casa, no hay locks que
// cruzar y el test queda verde y hueco. Hoy ningún test pone rojo la ausencia del reintento: es
// cosa del proceso «entrante a respuesta» de F9.
//
// Errores. Fuera de ErrNoRefs, que se inspecciona con errors.Is, todo error del cuerpo de la
// transacción va envuelto con %w y con el prefijo «contact: »; sus textos son observables y no
// cambian:
//   - «contact: buscar ref: %w», la búsqueda por value_bidx;
//   - «contact: cifrar value: %w» y «contact: cifrar push_name: %w», el cifrado, que solo puede
//     fallar por el stack de claves, esto es, para TODAS las filas y no para esta;
//   - «contact: insertar contacto: %w», el INSERT de la primera ref;
//   - «contact: adjuntar ref: %w», el INSERT de cada ref que se ata después;
//   - «contact: actualizar push_name: %w», el UPDATE del nombre de un contacto existente;
//   - «contact: created_at del contacto: %w», la elección del canónico;
//   - «contact: podar flow_state huérfano: %w», «contact: migrar flow_state en fusión: %w» y
//     «contact: re-apuntar refs en fusión: %w», los tres pasos de la fusión.
//
// Los errores de la transacción misma los devuelve postgres.WithTx, y Resolve no los vuelve a
// envolver ni les antepone «contact: ». Son de tres clases, y solo la primera lleva un prefijo propio:
//   - con el prefijo «postgres: »: «postgres: iniciar transacción: %w» (abrirla), «postgres:
//     confirmar transacción: %w» (el COMMIT) y, agotados los intentos ante un deadlock o un fallo de
//     serialización, «postgres: transacción tras %d intentos (último deadlock/serialización): %w»
//     (hoy, 8 intentos), que envuelve el último error del cuerpo: ahí el «contact: ...» del cuerpo
//     no abre el texto, va detrás de ese prefijo;
//   - el error del contexto DESNUDO, sin ningún prefijo (context.Canceled o
//     context.DeadlineExceeded tal cual), si el ctx se cancela durante la espera que sigue a un
//     intento abortado por deadlock o serialización: el error que provocó el reintento no aparece;
//   - un error compuesto con errors.Join, si además falla el ROLLBACK de un intento: une el error
//     que lo abortó (el del cuerpo, con su «contact: », o el de confirmar si el ctx se canceló justo
//     antes del COMMIT) y el del rollback, cada uno en su línea del texto y sin un prefijo común.
//     errors.Is y errors.As ven los dos.
//
// Un tenantID mal formado (no UUID) da el error de parseo de Postgres, nunca un centinela. Ningún
// error contiene el value ni el pushName (R1.4.d): son PII y los errores suben a los logs; los de
// cifrado dicen solo que no se pudo cifrar.
func (r *PostgresResolver) Resolve(ctx context.Context, tenantID string, refs []Ref, pushName string) (string, error) {
	panic(pendiente.Implementar("contact.PostgresResolver.Resolve"))
}

// Destino implementa Resolver.Destino con una lectura FUERA de transacción: trae kind, value_enc,
// value_dek y value_kek_id de todas las filas del contacto en el tenant, descifra el value de cada
// una y elige la ref enviable por la preferencia del puerto (phone_e164 > wa_username > wa_lid,
// entre las direccionables, R-19, R-20). Devuelve la Ref con el value NORMALIZADO tal como quedó
// guardado, no el crudo con el que llegó (R-23).
//
// Abre cada fila con SU value_kek_id, no con la KEK vigente (R-24): tras una rotación parcial
// coexisten filas cerradas por varias KEK, y la rotación (crypto.Rekey, de platform) solo vuelve a
// envolver la DEK y cambia value_kek_id, sin tocar value_enc ni value_bidx. Si la KEK de una fila no
// está en el keyring, falla claro, con «contact: descifrar value: %w», nunca con basura ni con un
// pánico; y la primera fila que no abre hace fallar la llamada entera, aunque otra fila del contacto
// sí abriera: no devuelve un destino parcial. El value en claro solo vive en memoria y no se loguea.
//
// Errores, con la Ref cero:
//   - ErrContactNotFound, envuelto con %w y el contactID entre comillas (%q), si no hay ninguna fila
//     del contacto en ese tenant: no existe, es de otro tenant (N-01) o es el de un huérfano que
//     una fusión ya borró (R-21, N-03);
//   - ErrNoDestino, sin envolver, si el contacto existe pero ninguna de sus refs es direccionable
//     (p. ej. solo un wa_username, R-20);
//   - «contact: leer refs del contacto: %w», «contact: escanear ref: %w», «contact: descifrar
//     value: %w» y «contact: iterar refs: %w», los fallos de la lectura, de cada fila y del cursor.
//
// Solo si no hubo ningún otro error, un fallo al cerrar el cursor se devuelve como «contact: cerrar
// filas: %w». Un tenantID o un contactID mal formados (no UUID) dan el error de parseo de Postgres
// envuelto en «leer refs del contacto», NO ErrContactNotFound (la memoria sí lo devuelve). Ningún
// error contiene el value (R1.4.d).
func (r *PostgresResolver) Destino(ctx context.Context, tenantID, contactID string) (Ref, error) {
	panic(pendiente.Implementar("contact.PostgresResolver.Destino"))
}
