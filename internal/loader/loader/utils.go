package loader

import (
	"fmt"
	"os"
)

func fatalManifestError(err error) {
	_, err = fmt.Fprintf(os.Stderr, "error: %v\n", err)
	if err != nil {
		return
	}
	os.Exit(1)
}
