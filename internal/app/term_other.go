// SPDX-License-Identifier: 0BSD

//go:build !windows

package app

import "os"

// enableANSI assumes Unix terminals handle ANSI escapes natively.
func enableANSI(*os.File) bool { return true }
