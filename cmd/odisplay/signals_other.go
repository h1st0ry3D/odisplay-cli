//go:build !unix

package main

import "os"

// shutdownSignals are the signals that mean "stop, and finish what you know".
// Only Ctrl-C on a platform without the others.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
