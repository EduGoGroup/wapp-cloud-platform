//go:build integracion

package procesos

import (
	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/contact"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/helpertest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/nucleo/latest"
	"github.com/EduGoGroup/wapp-cloud-platform/internal/platform/xhelpertest"
)

// Sin el …helpertest de contact en ESTE fichero, contact no es «el puerto que prueba»; latest
// no termina en helpertest; helpertest a secas no es la suite de nadie; y un …helpertest fuera
// de internal/modulos e internal/nucleo no cuenta. internal/modulos tampoco es el padre de
// ningún helpertest válido.
var _ = []any{modulos.X, contact.NewPostgresResolver, helpertest.X, latest.X, xhelpertest.X}
