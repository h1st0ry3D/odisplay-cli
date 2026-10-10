//go:build !linux && !darwin

package backend

import (
	"context"
	"errors"
	"runtime"
)

// Other is the backend for platforms this tool does not support. It fails
// loudly rather than pretending, so a build that somehow runs somewhere
// unexpected says so instead of switching half of anything.
type Other struct{}

func (Other) Platform() string { return runtime.GOOS }

func (Other) Plan(int, int, string) (Plan, error) {
	return Plan{}, errors.New("odisplay supports Linux and macOS only")
}

func (Other) Run(context.Context, Step) error {
	return errors.New("odisplay supports Linux and macOS only")
}

// New returns the fallback backend for platforms this tool does not support.
// It lives here rather than in a shared file so that no platform's type has to
// exist on another platform to compile.
func New(_ []string) Backend { return Other{} }
