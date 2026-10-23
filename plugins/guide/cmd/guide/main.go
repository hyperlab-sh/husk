// Command guide runs the optional layout annotation plugin.
package main

import (
	"fmt"
	"os"

	"github.com/hyperlab-sh/husk/plugins/guide"
)

func main() {
	if err := guide.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "guide:", err)
		os.Exit(1)
	}
}
