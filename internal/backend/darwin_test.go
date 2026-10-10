//go:build darwin

package backend

import (
	"reflect"
	"testing"
)

// The macOS half of the plan, which only compiles where Darwin exists. Run it
// on a Mac, or with `GOOS=darwin go vet ./...`.
func TestDarwinPlanIsOneStepForAllDevices(t *testing.T) {
	plan, err := Darwin{}.Plan(1, 17, "3")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("steps = %d, want the monitor plus one device call", len(plan.Steps))
	}
	want := Step{Bin: "/usr/local/bin/ddcctl", Args: []string{"-d", "1", "-i", "17"}}
	if !reflect.DeepEqual(plan.Steps[0], want) {
		t.Errorf("monitor step = %+v, want %+v", plan.Steps[0], want)
	}
	// logi-gate moves every paired device at once and takes no names, so
	// there is one step however many devices are configured.
	if plan.Steps[1].Bin != "/usr/local/bin/logi-gate" || plan.Steps[1].Args[0] != "3" {
		t.Errorf("device step = %+v", plan.Steps[1])
	}
}

func TestDarwinHostOffIsOneStep(t *testing.T) {
	plan, err := Darwin{}.Plan(1, 27, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 {
		t.Fatalf("steps = %d, want only the monitor", len(plan.Steps))
	}
	// USB-C is 27 in decimal here, where ddcutil would write 0x1b.
	if plan.Steps[0].Args[3] != "27" {
		t.Errorf("value = %q, want 27", plan.Steps[0].Args[3])
	}
}

func TestDarwinPlanRefusesAnOutOfRangeValue(t *testing.T) {
	if _, err := (Darwin{}).Plan(1, 999, "0"); err == nil {
		t.Error("accepted 999")
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
