package candados

import "github.com/EduGoGroup/wapp-cloud-platform/internal/pendiente"

// ContractAdapterDirs devuelve la lista CERRADA de pares «suite …helpertest → paquete del
// adaptador» en los que el adaptador Postgres NO vive en el padre del helpertest, y que
// ProcessImports (regla 3b) admite igual: un test/procesos/*_contrato_test.go que importa la
// clave puede importar también el valor, y de él solo usar constructores (New…), como del
// padre.
//
// Existe por la zona hexagonal: en iam la suite de los puertos de salida cuelga de
// iam/ports/out (outhelpertest) y sus adaptadores Postgres viven en iam/infra/postgres, que no
// es su padre (D-F2-9, Jhoan, 2026-10-04, sesión F2-03).
//
// Promesas:
//   - claves y valores van relativos a la raíz del repo y con «/», la misma forma que la ruta
//     de import relativizada, y se comparan por IGUALDAD: ni un subdirectorio ni un hermano
//     que comparta prefijo quedan dentro;
//   - el par solo vale en el fichero que importa la clave: importar el adaptador sin su
//     helpertest sigue mordiendo;
//   - devuelve una copia nueva en cada llamada: mutar el mapa devuelto no cambia lo que
//     devuelve la siguiente;
//   - hoy contiene exactamente internal/modulos/acceso/iam/ports/out/outhelpertest →
//     internal/modulos/acceso/iam/infra/postgres.
//
// Añadir un par exige antes una decisión en
// documentations/reorganizacion-modular/plan/DECISIONES.md.
func ContractAdapterDirs() map[string]string {
	panic(pendiente.Implementar("candados.ContractAdapterDirs"))
}
