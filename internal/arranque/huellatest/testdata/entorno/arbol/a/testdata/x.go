package x

import "os"

// X no cuenta: cuelga de un testdata.
func X() string { return os.Getenv("WAPP_X") }
