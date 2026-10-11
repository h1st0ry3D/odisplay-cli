//go:build linux

package backend

import (
	"context"
)

// Devices lists the Logitech devices Solaar can see.
//
// This is the list the settings' device names have to match. Solaar matches a
// name exactly and a name that does not match is an error rather than a silent
// no-op, so a rename in this list that the settings did not follow is the
// failure where the monitor moves and the keyboard does not.
func (Linux) Devices(ctx context.Context) (Connected, error) {
	text, err := runStepRaw(ctx, Step{Bin: solaar, Args: []string{"show"}}, solaarTimeout)
	if err != nil && text == "" {
		return Connected{}, err
	}
	// solaar prints a warning about Wayland on stderr, and it can exit non-zero
	// while still having printed a usable listing, so the output is what
	// decides rather than the exit status.
	return Connected{
		UsesNames: true,
		Devices:   parseSolaarShow(text),
		How:       "solaar show",
	}, nil
}
