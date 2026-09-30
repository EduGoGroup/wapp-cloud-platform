package puerto

// Repo es el puerto.
type Repo interface{ Leer() int }

// Nuevo ya no es una interfaz: con él, el fichero deja de ser solo de interfaces.
func Nuevo() Repo { return nil }
