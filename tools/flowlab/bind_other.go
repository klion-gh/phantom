//go:build !windows

package main

import (
	"log"
	"syscall"
)

// Interface binding is only implemented for Windows, where the stand is run;
// elsewhere -iface is refused rather than silently ignored.

func bindFD(iface int) func(fd int) bool {
	if iface != 0 {
		log.Fatal("-iface is only supported on Windows")
	}
	return nil
}

func bindControl(iface int) func(network, address string, c syscall.RawConn) error {
	if iface != 0 {
		log.Fatal("-iface is only supported on Windows")
	}
	return nil
}
