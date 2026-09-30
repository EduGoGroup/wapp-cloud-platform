package embebido

import _ "embed"

// Plantilla viene embebida.
//
//go:embed plantilla.txt
var Plantilla string

var (
	// Esquema viene embebido, dentro de un grupo.
	//
	//go:embed esquema.sql
	Esquema string
)
