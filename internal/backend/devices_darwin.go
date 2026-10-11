//go:build darwin

package backend

import (
	"context"
	"fmt"
)

// Devices lists the Logitech devices mxswitch can see.
//
// Unlike Solaar this is not a list the settings have to match: mxswitch takes
// no device name, it picks one itself and moves one per call. So what this is
// good for is answering whether the keyboard and mouse are there at all.
//
// `mxswitch --list` prints one line per HID collection, including the
// receiver's own vendor collections, so those are skipped: they are not
// something the settings could ever name.
func (Darwin) Devices(ctx context.Context) (Connected, error) {
	if monitorProgram() == ddcctl {
		// Intel, where mxswitch does not work either. Nothing to report.
		return Connected{UsesNames: false, How: "mxswitch"}, nil
	}
	text, err := runStepRaw(ctx, Step{Bin: mxswitch, Args: []string{"--list"}}, mxswitchTimeout)
	if err != nil {
		return Connected{}, fmt.Errorf("mxswitch --list: %w", err)
	}
	return Connected{
		UsesNames: false,
		Devices:   parseMxswitchList(text),
		How:       "mxswitch --list",
	}, nil
}
