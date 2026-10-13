// SPDX-License-Identifier: 0BSD

package main

import (
	"os"

	"mod-archiver/internal/app"
)

func main() {
	os.Exit(app.Run(os.Args[1:], os.Stdout, os.Stderr))
}
