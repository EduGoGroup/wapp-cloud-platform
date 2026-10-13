package embebido

import _ "embed"

// Plantilla viene embebida.
//
//go:embed plantilla.txt
var Plantilla string

// Extra no es un //go:embed: el fichero deja de ser solo de embebidos.
var Extra = 2
