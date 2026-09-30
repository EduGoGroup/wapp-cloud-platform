package puertotest

import (
	"testing"

	"github.com/EduGoGroup/wapp-cloud-platform/internal/modulos/m/puerto"
)

// Contrato es la suite del puerto.
func Contrato(t *testing.T, nuevo func() puerto.Repo) { _ = nuevo() }
