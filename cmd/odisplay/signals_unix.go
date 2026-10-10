//go:build unix

package main

import (
	"os"
	"syscall"
)

// shutdownSignals are the signals that mean "stop, and finish what you know".
//
// SIGTERM is here because the panel sends it when its watchdog fires and when
// the shell reloads. Caught, that cancels the context, the switch stops where it
// is, and it exits with an exit code saying whether the keyboard moved.
// Uncaught, the process dies instantly, the child keeps running and finishes
// moving a device, and nothing reports that it happened at all.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
