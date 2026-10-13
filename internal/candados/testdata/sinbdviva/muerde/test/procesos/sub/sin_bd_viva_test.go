package sub

import "os"

// Un fichero que se llama como el candado, pero en otro directorio: hasta D-F9-6 la
// auto-exención era por nombre base y este fichero quedaba sin mirar. Ahora es por ruta
// exacta (test/procesos/sin_bd_viva_test.go) y muerde como cualquiera.
var dsn = os.Getenv("WAPP_TEST_DB_DSN")
