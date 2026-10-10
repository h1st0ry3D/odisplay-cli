//go:build darwin

package backend

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// These only compile and run where Darwin exists. Run them on a Mac:
//   GOOS=darwin go vet ./...

// The display is always the first step, and the arguments are kfix/ddcctl's own
// documented form.
func TestDarwinPlanWritesTheDdcctlInvocation(t *testing.T) {
	plan, err := Darwin{Devices: 2}.Plan(1, 17, "3")
	if err != nil {
		t.Fatal(err)
	}
	want := Step{Bin: ddcctl, Args: []string{"-d", "1", "-i", "17"}}
	if !reflect.DeepEqual(plan.Steps[0], want) {
		t.Errorf("monitor step = %+v, want %+v", plan.Steps[0], want)
	}
}

// One number, two spellings: USB-C is 27 to ddcctl and 0x1b to ddcutil.
func TestDarwinWritesTheValueInDecimal(t *testing.T) {
	plan, err := Darwin{}.Plan(1, 27, "0")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Steps[0].Args[3]; got != "27" {
		t.Errorf("value = %q, want 27", got)
	}
}

func TestDarwinHostOffIsOneStep(t *testing.T) {
	plan, err := Darwin{Devices: 2}.Plan(1, 27, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %d, want only the monitor", len(plan.Steps))
	}
}

// mxswitch moves one device per call, so the number of calls is how many
// devices the config lists.
func TestDarwinMovesOneStepPerDevice(t *testing.T) {
	plan, err := Darwin{Devices: 2}.Plan(1, 15, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 3 {
		t.Fatalf("steps = %d, want the monitor plus 2 devices", len(plan.Steps))
	}
	want := Step{Bin: mxswitch, Args: []string{"2"}}
	for i := 1; i < 3; i++ {
		if !reflect.DeepEqual(plan.Steps[i], want) {
			t.Errorf("device step %d = %+v, want %+v", i, plan.Steps[i], want)
		}
	}
}

// A config with no devices must not silently claim to have moved them.
func TestDarwinWithNoDevicesMakesNoDeviceSteps(t *testing.T) {
	plan, err := Darwin{Devices: 0}.Plan(1, 15, "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Errorf("steps = %d, want only the monitor", len(plan.Steps))
	}
}

// mxswitch accepts only 1, 2 and 3. A channel it would refuse is caught here,
// where the devices are still where they are.
func TestDarwinRefusesAChannelMxswitchWouldReject(t *testing.T) {
	for _, channel := range []string{"0", "4", "9", "", "x"} {
		if _, err := (Darwin{Devices: 1}).Plan(1, 15, channel); err == nil {
			// "0" is off, which is not an error, just one step.
			plan, _ := (Darwin{Devices: 1}).Plan(1, 15, channel)
			if channel != "0" {
				t.Errorf("accepted channel %q", channel)
			} else if len(plan.Steps) != 1 {
				t.Errorf("channel 0 made %d steps, want only the monitor", len(plan.Steps))
			}
		}
	}
}

func TestDarwinPlanRefusesAnOutOfRangeValue(t *testing.T) {
	if _, err := (Darwin{}).Plan(1, 999, "0"); err == nil {
		t.Error("accepted 999")
	}
	if _, err := (Darwin{}).Plan(1, -1, "0"); err == nil {
		t.Error("accepted -1")
	}
}

// Homebrew puts things in /opt/homebrew/bin on Apple Silicon and
// /usr/local/bin on Intel. A single hardcoded path is wrong on half of Macs.
func TestMustFindFindsAProgramInEitherPrefix(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "thing")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := mustFind("thing", "/nowhere", dir); got != prog {
		t.Errorf("mustFind = %q, want %q", got, prog)
	}
	// Not executable, so not a program it can run.
	plain := filepath.Join(dir, "notexec")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustFind("notexec", "/nowhere", dir); got != filepath.Join("/nowhere", "notexec") {
		t.Errorf("mustFind = %q, want the first candidate when nothing is runnable", got)
	}
	// A directory is not a program either.
	if got := mustFind("thing", dir, "/nowhere"); got != filepath.Join(dir, "thing") {
		t.Errorf("mustFind = %q, want to skip the directory", got)
	}
}

// The count of configured devices is what the macOS plan uses, and Solaar's
// names mean nothing here.
func TestNewCountsDevicesAndIgnoresTheirNames(t *testing.T) {
	d := New([]string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"})
	darwin, ok := d.(Darwin)
	if !ok {
		t.Fatalf("New() = %T, want Darwin", d)
	}
	if darwin.Devices != 2 {
		t.Errorf("Devices = %d, want 2", darwin.Devices)
	}
	if New([]string{"a", "", "  "}).(Darwin).Devices != 1 {
		t.Error("a blank device name still counted")
	}
	if New(nil).(Darwin).Devices != 0 {
		t.Error("no devices should mean no device steps")
	}
}

func TestDarwinDetectReportsAMissingDdcctl(t *testing.T) {
	d := Darwin{}
	if _, err := d.Detect(context.Background()); err == nil {
		t.Skip("ddcctl is installed here")
	}
}

func TestNewReturnsTheDarwinBackend(t *testing.T) {
	b := New(nil)
	if _, ok := b.(Darwin); !ok {
		t.Fatalf("New() = %T, want Darwin", b)
	}
	if got := b.Platform(); got != "darwin" {
		t.Errorf("Platform() = %q, want darwin", got)
	}
}

// The same case the Mac hit: a settings file with no channel for an input.
func TestDarwinRefusesAnEmptyChannel(t *testing.T) {
	if _, err := (Darwin{Devices: 1}).Plan(1, 15, ""); err == nil {
		t.Fatal("an empty channel was accepted")
	}
}

func TestDarwinRefusesAChannelOutsideOneToThree(t *testing.T) {
	for _, channel := range []string{"4", "9", "-1", "x", " 2"} {
		if _, err := (Darwin{Devices: 1}).Plan(1, 15, channel); err == nil {
			t.Errorf("accepted channel %q", channel)
		}
	}
}
