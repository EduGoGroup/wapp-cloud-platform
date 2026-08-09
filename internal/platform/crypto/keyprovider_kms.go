package crypto

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/EduGoGroup/wapp-shared/envelope"
)

// Providers admitidos por NewKeyProvider (valores de WAPP_KEK_PROVIDER).
const (
	// ProviderEnv es el fallback de DEV LOCAL: las KEK viajan en claro en
	// variables de entorno. Es el DEFAULT (ADR-0036 §3: el KMS se construye pero
	// NO se activa en alfa — cero gasto mientras no haya suscripciones).
	ProviderEnv = "env"
	// ProviderKMS es el camino de PRODUCCIÓN: las KEK viajan cifradas por el KMS
	// de GCP y solo el proceso autorizado puede desenvolverlas al arrancar.
	ProviderKMS = "kms"
)

// ErrUnknownKeyProvider indica un WAPP_KEK_PROVIDER que no es "env" ni "kms".
// Se falla-rápido en vez de asumir un default: un valor mal escrito ("KMS ",
// "gcp", "kms2") caería silenciosamente al fallback env, que es exactamente lo
// que la Ola T9 viene a impedir.
var ErrUnknownKeyProvider = errors.New("WAPP_KEK_PROVIDER desconocido")

// ErrKMSKeyNameMissing indica que falta el recurso de la llave del KMS.
var ErrKMSKeyNameMissing = errors.New("con WAPP_KEK_PROVIDER=kms hace falta WAPP_KEK_KMS_KEY (projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>)")

// ErrKMSKeyringMissing indica que falta el keyring cifrado por el KMS.
var ErrKMSKeyringMissing = errors.New("con WAPP_KEK_PROVIDER=kms hace falta WAPP_KEK_KMS_KEYRING (entradas id:base64-del-cifrado-KMS) + WAPP_KEK_CURRENT")

// ErrKMSDecrypt indica que el KMS no pudo desenvolver una KEK del keyring
// (permisos, llave equivocada, ciphertext de otra llave, red). Es SIEMPRE fatal
// en el arranque: nunca se degrada al fallback env.
var ErrKMSDecrypt = errors.New("el KMS no pudo desenvolver una KEK del keyring")

// KMSDecrypter es la porción del cliente de KMS que este adapter necesita: una
// sola operación, Decrypt. Se declara aquí —y no se importa del SDK— para que el
// provider sea testeable con un doble y para que el SDK de GCP quede confinado a
// kms_gcp.go.
type KMSDecrypter interface {
	// Decrypt desenvuelve ciphertext con la llave keyName del KMS. keyName es el
	// nombre de recurso completo de la CryptoKey; el KMS elige la versión por el
	// propio ciphertext.
	Decrypt(ctx context.Context, keyName string, ciphertext []byte) (plaintext []byte, err error)
}

// KMSConfig agrupa el material para construir el KeyProvider respaldado por el
// KMS de GCP (Plan 042 · T9.1, ADR-0036).
//
// Modelo: ENVELOPE ENCRYPTION DE LA KEK. La KEK sigue siendo la que envuelve las
// DEK por-valor, pero ya no viaja en claro: lo que se configura es su CIFRADO por
// una llave del KMS, y el KMS lo desenvuelve UNA vez al arrancar. Consecuencias
// que importan:
//   - Cero llamadas al KMS por operación (solo N al arrancar, N = KEKs del
//     keyring): sin coste por request ni latencia en el camino caliente, que es
//     lo que exige la regla de costos del ADR-0036 §3.
//   - La rotación del Plan 012 (ReWrap) funciona IGUAL: sigue siendo desenvolver
//     con la KEK vieja y envolver con la current, ambas ya en memoria.
//   - Los key_id se CONSERVAN al migrar (T9.2): si la KEK "1" pasa al KMS con el
//     mismo id, las filas existentes (value_kek_id='1') siguen resolviendo sin
//     tocar la base.
type KMSConfig struct {
	// KeyName es el nombre de recurso de la CryptoKey del KMS que desenvuelve el
	// keyring: projects/<p>/locations/<l>/keyRings/<r>/cryptoKeys/<k>. Obligatorio.
	// Se lee de WAPP_KEK_KMS_KEY.
	KeyName string
	// KeyringB64 es el keyring versionado con los valores CIFRADOS por el KMS:
	// "id1:base64(ciphertext),id2:base64(ciphertext),…". Obligatorio. Se lee de
	// WAPP_KEK_KMS_KEYRING. Los id son los mismos key_id que ya están persistidos
	// en las columnas *_kek_id.
	KeyringB64 string
	// CurrentID es el key_id de la KEK current dentro del keyring; debe existir en
	// él. Se lee de WAPP_KEK_CURRENT (la MISMA variable que el provider env: la
	// semántica no cambia al migrar).
	CurrentID string
	// IndexB64 es la indexKey del índice ciego EN CLARO (WAPP_KEK_INDEX_B64), el
	// camino de transición mientras la indexKey no se lleva al KMS.
	IndexB64 string
	// IndexCiphertextB64 es la indexKey CIFRADA por la misma llave del KMS
	// (WAPP_KEK_KMS_INDEX_B64). Si viene, tiene prioridad sobre IndexB64 y evita
	// que quede un secreto en claro en el entorno productivo. Es OPCIONAL: la
	// Ola T9 solo exige la KEK, pero dejar la indexKey en claro al lado de una KEK
	// custodiada es una media protección que conviene poder cerrar.
	IndexCiphertextB64 string
	// Prod activa la política de producción: sin indexKey explícita, fail-fast
	// (§10.C). Nunca se deriva de la KEK en producción.
	Prod bool
}

// NewKMSKeyProvider construye el KeyProvider de PRODUCCIÓN: valida la
// configuración, abre el cliente del KMS de GCP, desenvuelve el keyring, CIERRA el
// cliente y devuelve un provider que ya no vuelve a hablar con el KMS. La interfaz
// KeyProvider no cambia, así que FieldCipher, los repos y la rotación del Plan 012
// son idénticos (REQ-17).
//
// El ORDEN importa: primero la configuración, después la red. Abrir el cliente
// antes convertía un fallo local —falta WAPP_KEK_KMS_KEY— en un error de
// credenciales ADC, que manda al operador a investigar el sitio equivocado y en un
// entorno sin ADC (el contenedor del CI) TAPA por completo el error de verdad.
// Nada que se pueda comprobar en el proceso justifica salir a internet primero.
//
// El cliente se cierra aquí a propósito: sin conexión viva no hay reintentos de
// fondo ni llamadas accidentales que generen gasto (ADR-0036 §3).
//
// Falla-rápido y NUNCA cae al fallback env: si el KMS no responde, la llave no
// existe, faltan permisos o el ciphertext es de otra llave, el arranque muere con
// error claro. Esa es la invariante de la ola.
func NewKMSKeyProvider(ctx context.Context, cfg KMSConfig) (KeyProvider, error) {
	plan, err := validateKMSConfig(cfg)
	if err != nil {
		return nil, err
	}

	client, err := newGCPKMSDecrypter(ctx)
	if err != nil {
		return nil, fmt.Errorf("abriendo el cliente del KMS de GCP: %w", err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil {
			log.Printf("[wapp][crypto][WARN] cerrando el cliente del KMS: %v", cerr)
		}
	}()
	return unwrapKeyring(ctx, cfg, plan, client)
}

// newKMSKeyProvider es el núcleo testeable: valida igual que el camino real y
// desenvuelve el keyring con dec (el cliente real o un doble).
func newKMSKeyProvider(ctx context.Context, cfg KMSConfig, dec KMSDecrypter) (KeyProvider, error) {
	plan, err := validateKMSConfig(cfg)
	if err != nil {
		return nil, err
	}
	return unwrapKeyring(ctx, cfg, plan, dec)
}

// kmsKeyringPlan es la KMSConfig ya validada y decodificada: todo lo que se puede
// comprobar SIN hablar con el KMS. Existe para separar los dos tipos de fallo
// —configuración (local, barato, culpa del operador) y KMS (red, credenciales,
// permisos)— y poder hacerlos en ese orden.
type kmsKeyringPlan struct {
	keyName   string
	currentID string
	wrapped   map[string][]byte // key_id → KEK CIFRADA por el KMS, ya en bytes.
	indexCT   []byte            // indexKey cifrada por el KMS; nil si no se configuró.
}

// validateKMSConfig comprueba y decodifica cuanto no requiere red. Es el PRIMER
// paso del arranque en modo kms: un error de configuración debe nombrar la
// configuración, y además no tiene sentido gastar N llamadas al KMS por un
// arranque que va a morir igual.
func validateKMSConfig(cfg KMSConfig) (kmsKeyringPlan, error) {
	keyName := strings.TrimSpace(cfg.KeyName)
	if keyName == "" {
		return kmsKeyringPlan{}, ErrKMSKeyNameMissing
	}
	if strings.TrimSpace(cfg.KeyringB64) == "" {
		return kmsKeyringPlan{}, ErrKMSKeyringMissing
	}
	currentID := strings.TrimSpace(cfg.CurrentID)
	if currentID == "" {
		return kmsKeyringPlan{}, ErrCurrentKeyMissing
	}

	wrapped, err := parseKeyringEntries(cfg.KeyringB64)
	if err != nil {
		return kmsKeyringPlan{}, fmt.Errorf("keyring del KMS: %w", err)
	}
	if _, ok := wrapped[currentID]; !ok {
		return kmsKeyringPlan{}, fmt.Errorf("%w: %q no está en el keyring del KMS", ErrCurrentKeyMissing, currentID)
	}

	var indexCT []byte
	switch idxB64 := strings.TrimSpace(cfg.IndexCiphertextB64); {
	case idxB64 != "":
		if indexCT, err = base64.StdEncoding.DecodeString(idxB64); err != nil {
			return kmsKeyringPlan{}, fmt.Errorf("indexKey cifrada por el KMS: base64 inválido: %w", err)
		}
	case cfg.Prod && strings.TrimSpace(cfg.IndexB64) == "":
		// La política de producción (§10.C) también es configuración: sin indexKey
		// explícita el arranque muere igual, así que se dice ya y no después de
		// desenvolver el keyring entero.
		return kmsKeyringPlan{}, ErrIndexKeyRequiredInProd
	}

	return kmsKeyringPlan{keyName: keyName, currentID: currentID, wrapped: wrapped, indexCT: indexCT}, nil
}

// unwrapKeyring es la parte que SÍ habla con el KMS: desenvuelve cada KEK del
// keyring y la indexKey, y arma el provider en memoria.
func unwrapKeyring(ctx context.Context, cfg KMSConfig, plan kmsKeyringPlan, dec KMSDecrypter) (KeyProvider, error) {
	keys := make(map[string][]byte, len(plan.wrapped))
	for id, ct := range plan.wrapped {
		kek, derr := dec.Decrypt(ctx, plan.keyName, ct)
		if derr != nil {
			// Sin el valor ni pistas del material: solo el key_id y la llave del KMS.
			return nil, fmt.Errorf("%w (key_id %q, llave %s): %w", ErrKMSDecrypt, id, plan.keyName, derr)
		}
		if len(kek) != envelope.DEKSize {
			return nil, fmt.Errorf("%w (key_id %q): el KMS devolvió %d bytes", ErrKEKSize, id, len(kek))
		}
		keys[id] = kek
	}

	indexKey, err := resolveKMSIndexKey(ctx, cfg, plan, dec, keys[plan.currentID])
	if err != nil {
		return nil, err
	}

	log.Printf("[wapp][crypto][INFO] KeyProvider=kms: keyring desenvuelto por el KMS (keys=%d current=%q llave=%s); "+
		"no se vuelve a llamar al KMS en caliente", len(keys), plan.currentID, plan.keyName)
	return &envKeyProvider{keys: keys, currentID: plan.currentID, indexKey: indexKey}, nil
}

// resolveKMSIndexKey obtiene la indexKey del índice ciego en modo KMS: primero la
// cifrada por el KMS (si se configuró), luego la explícita en claro, y si no hay
// ninguna aplica la MISMA política que el provider env (fail-fast en prod —ya
// resuelto en validateKMSConfig—, HKDF con warning en dev).
func resolveKMSIndexKey(ctx context.Context, cfg KMSConfig, plan kmsKeyringPlan, dec KMSDecrypter, currentKEK []byte) ([]byte, error) {
	if len(plan.indexCT) == 0 {
		return resolveIndexKey(cfg.IndexB64, cfg.Prod, currentKEK)
	}

	indexKey, err := dec.Decrypt(ctx, plan.keyName, plan.indexCT)
	if err != nil {
		return nil, fmt.Errorf("%w (indexKey, llave %s): %w", ErrKMSDecrypt, plan.keyName, err)
	}
	if len(indexKey) != indexKeySize {
		return nil, ErrIndexKeySize
	}
	return indexKey, nil
}

// ProviderConfig es lo que NewKeyProvider necesita para ELEGIR y construir el
// KeyProvider. Agrupa el material de los dos caminos porque la elección es de
// arranque y debe ser explícita.
type ProviderConfig struct {
	// Provider selecciona el camino: "env" (default) o "kms" (WAPP_KEK_PROVIDER).
	Provider string
	// Env es el material del fallback de dev local (KEKs en claro).
	Env KeyringConfig
	// KMS es el material del camino de producción (KEKs cifradas por el KMS).
	KMS KMSConfig
	// Prod activa la política de producción en el camino elegido.
	Prod bool
}

// NewKeyProvider construye el KeyProvider según cfg.Provider. Es el ÚNICO punto de
// arranque que debe usar el código de producción: así ningún otro camino lee la
// KEK de variables de entorno (criterio de grep de T9.1).
//
// Reglas, en orden de importancia:
//  1. Con Provider="kms" y el KMS mal configurado (llave ausente, sin permisos,
//     keyring que no descifra) el arranque FALLA con error claro. JAMÁS se
//     degrada al fallback env: un fallback silencioso convertiría un incidente de
//     permisos en una KEK en claro sirviendo producción sin que nadie se entere.
//  2. Un Provider desconocido también falla: no se adivina un default.
//  3. Con Provider="env" (default) el comportamiento es EXACTAMENTE el de antes
//     de la Ola T9 — regresión cero. En modo producción se avisa por log de que
//     ese camino es de dev local y de que el gate T9.2 es el que lo cierra; no se
//     mata el proceso porque el ADR-0036 §3 mantiene deliberadamente la alfa en
//     env (cero gasto) mientras no haya suscripciones que absorban el KMS.
func NewKeyProvider(ctx context.Context, cfg ProviderConfig) (KeyProvider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", ProviderEnv:
		if cfg.Prod {
			log.Printf("[wapp][crypto][WARN] WAPP_KEK_PROVIDER=env en modo producción: las KEK viajan EN CLARO por " +
				"variables de entorno. Es el camino de DEV LOCAL; producción debe pasar a WAPP_KEK_PROVIDER=kms " +
				"(ADR-0036, Plan 042 · T9.2) y retirar WAPP_KEK_MASTER_B64 del entorno.")
		}
		env := cfg.Env
		env.Prod = cfg.Prod
		return NewEnvKeyProvider(env)

	case ProviderKMS:
		// El material env NO se usa en esta rama —el keyring sale del KMS—, pero si
		// sigue en el entorno hay una KEK en claro que ya no protege nada y que el
		// gate T9.2 exige retirar. Se avisa alto: un secreto olvidado no falla, y por
		// eso nadie lo borra.
		if strings.TrimSpace(cfg.Env.MasterB64) != "" || strings.TrimSpace(cfg.Env.KeyringB64) != "" {
			log.Printf("[wapp][crypto][WARN] WAPP_KEK_PROVIDER=kms pero el entorno TODAVÍA trae KEK en claro " +
				"(WAPP_KEK_MASTER_B64 / WAPP_KEK_KEYRING). No se usan, pero hay que retirarlas del entorno " +
				"productivo (Plan 042 · T9.2).")
		}
		kms := cfg.KMS
		kms.Prod = cfg.Prod
		return NewKMSKeyProvider(ctx, kms)

	default:
		return nil, fmt.Errorf("%w: %q (valores admitidos: %q, %q)",
			ErrUnknownKeyProvider, cfg.Provider, ProviderEnv, ProviderKMS)
	}
}
