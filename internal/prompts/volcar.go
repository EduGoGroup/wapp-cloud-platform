package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/EduGoGroup/wapp-shared/llm"
)

// NombreDeFichero es el nombre que Volcar le da a cada etapa. El prefijo es
// contrato (lo lee etapaDeNombre); el resto es descripción y se puede cambiar.
//
// Se declara como tabla y no se compone al vuelo para que el nombre que un
// operador ve en su directorio sea SIEMPRE el mismo y se pueda buscar en la
// documentación tal cual está escrito.
var NombreDeFichero = map[llm.Etapa]string{
	llm.EtapaP2: "p2-extraer-ideas" + Extension,
	llm.EtapaP3: "p3-especificar-item" + Extension,
	llm.EtapaP4: "p4-normalizar-cantidades" + Extension,
	llm.EtapaP5: "p5-redactar-cotizacion" + Extension,
}

// QueHaceLaEtapa describe en una línea para qué sirve cada prompt. Va en el
// preámbulo del fichero volcado: quien lo abre para tocarlo tiene que saber qué
// está tocando sin ir a buscar el código.
var QueHaceLaEtapa = map[llm.Etapa]string{
	llm.EtapaP2: "saca las ideas principales del mensaje: una entrada por cosa distinta que pide el cliente.",
	llm.EtapaP3: "especifica UN ítem —producto, variante, añadidos, personalizaciones—. Se llama una vez por ítem.",
	llm.EtapaP4: "normaliza cantidades, paquetes y rangos, y resuelve la fecha de entrega contra la del mensaje.",
	llm.EtapaP5: "redacta el mensaje de cotización con la voz del negocio, copiando importes del borrador.",
}

// Volcar escribe en dir los CUATRO ficheros con el texto que corre HOY, y devuelve
// las rutas escritas.
//
// 🔴 ES LA ÚNICA FORMA CORRECTA DE EMPEZAR A EDITAR UN PROMPT. Escribir los
// ficheros a mano copiando de la documentación, de un ejemplo o de un fichero de
// otro entorno produce plantillas que ya nacen viejas y que cambian el prompt sin
// que nadie lo haya querido — y el cambio no se ve en ningún diff, porque el
// fichero es nuevo. Volcar sale del código compilado, así que el diff contra lo
// que edites es exactamente lo que cambiaste.
//
// No sobrescribe: un fichero que ya existe se respeta y se informa en el error.
// El operador que quiera regenerarlo lo borra a conciencia; perderle una tarde de
// ajustes a alguien por un comando de más no es un intercambio aceptable.
func Volcar(dir string) ([]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("%w: volcar necesita un directorio", ErrPromptsDir)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("%w: no se puede crear %s: %w", ErrPromptsDir, dir, err)
	}

	rutas := make([]string, 0, len(llm.EtapasAjustables))
	for _, e := range llm.EtapasAjustables {
		p, ok := llm.PlantillaPorDefecto(e)
		if !ok {
			return nil, fmt.Errorf("%w: la etapa %q no tiene plantilla compilada", ErrPromptsDir, e)
		}
		ruta := filepath.Join(dir, NombreDeFichero[e])
		if _, err := os.Stat(ruta); err == nil {
			return nil, fmt.Errorf("%w: %s ya existe y NO se sobrescribe; bórralo tú si de verdad "+
				"quieres perder lo que tiene", ErrPromptsDir, ruta)
		}
		if err := os.WriteFile(ruta, []byte(Serializar(e, p)), 0o600); err != nil {
			return nil, fmt.Errorf("%w: no se puede escribir %s: %w", ErrPromptsDir, ruta, err)
		}
		rutas = append(rutas, ruta)
	}
	return rutas, nil
}

// Serializar convierte una plantilla en el contenido de su fichero. Es la inversa
// exacta de Parsear —hay un test que lo exige sobre las cuatro etapas—, y esa
// simetría es lo que hace que volcar, editar y cargar sea un ciclo cerrado en vez
// de dos formatos que se parecen.
//
// La versión del artefacto se escribe como el hueco {{version}} y no como el
// número: así un fichero editado hoy sigue valiendo el día que ese número cambie,
// en vez de quedarse clavado en un valor que ya no es.
func Serializar(e llm.Etapa, p llm.Plantilla) string {
	version := fmt.Sprintf("%d", llm.ArtifactVersion)
	esquema := strings.Replace(p.Esquema, `"version": `+version, `"version": `+HuecoVersion, 1)

	var b strings.Builder
	fmt.Fprintf(&b, "Prompt de la etapa %s — %s\n\n", strings.ToUpper(string(e)), QueHaceLaEtapa[e])
	b.WriteString("Esto de aquí arriba, ANTES del primer marcador, es documentación y NO se le manda\n")
	b.WriteString("al modelo. Escribe aquí lo que haga falta para el que venga detrás.\n\n")
	b.WriteString("Cómo se usa este fichero:\n")
	b.WriteString("  - Edítalo y REINICIA el cloud. No hay recarga en caliente, a propósito.\n")
	b.WriteString("  - Si te equivocas, el cloud NO ARRANCA y te dice qué fichero y por qué.\n")
	b.WriteString("  - " + HuecoVersion + " se sustituye por la versión de artefacto que el código sabe leer.\n")
	b.WriteString("  - El texto entre marcadores se preserva EXACTO, líneas en blanco incluidas.\n\n")
	b.WriteString("🔴 EN EL ESQUEMA NO PUEDE HABER UN VALOR QUE EL VALIDADOR RECHACE. El modelo COPIA\n")
	b.WriteString("el ejemplo: un 0 escrito ahí es un 0 en su respuesta. Los `...` sí pueden quedarse\n")
	b.WriteString("—son huecos reconocibles y se detectan si el modelo los ecoa—, pero un número tiene\n")
	b.WriteString("que ser válido tal cual está impreso. Esto ya costó una etapa entera: P4 fue 0 de 14\n")
	b.WriteString("en su primer día en campo porque su esquema imprimía `\"package_size\": 0`.\n\n")
	// Sin recortes ni saltos añadidos: lo que se escribe es lo que Parsear devuelve.
	// Serializar y Parsear son inversas exactas, y hay un test que lo exige.
	b.WriteString(MarcaInstruccion + "\n")
	b.WriteString(p.Instruccion)
	b.WriteString(MarcaEsquema + "\n")
	b.WriteString(esquema)
	return b.String()
}
