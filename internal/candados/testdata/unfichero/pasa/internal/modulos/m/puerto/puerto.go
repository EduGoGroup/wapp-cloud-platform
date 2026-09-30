package puerto

// Repo es el puerto.
type Repo interface{ Leer() int }

// Escritor es otro puerto del mismo fichero.
type Escritor interface{ Escribir(int) }
