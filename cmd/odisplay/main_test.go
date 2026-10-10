package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h1st0ry3D/odisplay-cli/internal/backend"
	"github.com/h1st0ry3D/odisplay-cli/internal/config"
	"github.com/h1st0ry3D/odisplay-cli/internal/order"
)

// doctor is the only thing standing between a wrong display number and a switch
// that silently does nothing while the devices still move. Its decisions are
// checked here against a scripted backend, so nothing touches hardware.

type fake struct {
	planErr   error
	displays  []backend.Display
	detectErr error
}

func (f fake) Platform() string { return "fake" }
func (f fake) Monitor() string  { return "ddcutil" }
func (f fake) Plan(int, int, string) (backend.Plan, error) {
	if f.planErr != nil {
		return backend.Plan{}, f.planErr
	}
	return backend.Plan{Platform: "fake", Steps: []backend.Step{{Bin: "/usr/bin/ddcutil"}}}, nil
}
func (f fake) Run(context.Context, backend.Step) error { return nil }
func (f fake) Detect(context.Context) ([]backend.Display, error) {
	return f.displays, f.detectErr
}

// The display number ddcutil accepts is the one it prints, so doctor has to
// compare against those numbers rather than counting the lines it read.
func TestDoctorAcceptsTheNumberDdcutilPrints(t *testing.T) {
	// The Dell answers, so it is "Display 1". The laptop panel is rejected and
	// has no number at all. Counting would have made the Dell display 2, which
	// is the number this tool once told the user to use.
	f := fake{displays: []backend.Display{
		{Number: 0, Name: "card1-eDP-1", Usable: false, Why: "laptop display"},
		{Number: 1, Name: "card2-DP-6", Usable: true},
	}}
	code, out := runDoctor(t, f, 1)
	if code != order.CodeOK {
		t.Errorf("code = %d (%s), want %d", code, out, order.CodeOK)
	}
	if !strings.Contains(out, "* display 1") {
		t.Errorf("output does not mark the right display as in use:\n%s", out)
	}
	if strings.Contains(out, "display 2") {
		t.Errorf("output invented a display 2:\n%s", out)
	}
	if !strings.Contains(out, "ready") {
		t.Errorf("output does not say ready:\n%s", out)
	}
}

func TestDoctorWarnsWhenTheConfiguredDisplayIsNotOneDdcutilAccepts(t *testing.T) {
	f := fake{displays: []backend.Display{
		{Number: 0, Name: "card1-eDP-1", Usable: false, Why: "laptop display"},
		{Number: 1, Name: "card2-DP-6", Usable: true},
	}}
	code, out := runDoctor(t, f, 2)
	if code != order.CodeUsage {
		t.Errorf("code = %d, want %d", code, order.CodeUsage)
	}
	if !strings.Contains(out, "odisplay set display 1") {
		t.Errorf("output does not say what to do about it:\n%s", out)
	}
}

func TestDoctorSaysSoWhenNoDisplayCanBeSwitched(t *testing.T) {
	f := fake{displays: []backend.Display{
		{Number: 0, Name: "card1-eDP-1", Usable: false, Why: "laptop display"},
	}}
	code, out := runDoctor(t, f, 1)
	if code != order.CodeUsage {
		t.Errorf("code = %d, want %d", code, order.CodeUsage)
	}
	if !strings.Contains(out, "No display") {
		t.Errorf("output should say none can be switched:\n%s", out)
	}
}

// Exit 0 means "done". A caller scripting `doctor && switch` would carry on
// with a display number nothing had checked.
func TestDoctorDoesNotClaimReadyWhenItCouldNotReadTheDisplays(t *testing.T) {
	f := fake{detectErr: errFake{}}
	code, out := runDoctor(t, f, 1)
	if code == order.CodeOK {
		t.Errorf("code = %d, want a failure", code)
	}
	if strings.Contains(out, "ready") {
		t.Errorf("output claims ready after failing to check:\n%s", out)
	}
}

// A value the config got wrong is not a missing program. The same condition on
// `switch` exits 1, so doctor cannot exit 4 and mean something else.
func TestDoctorDoesNotCallABadConfigAMissingProgram(t *testing.T) {
	f := fake{planErr: errFake2{}}
	code, out := runDoctor(t, f, 1)
	if code != order.CodeUsage {
		t.Errorf("code = %d, want %d for a config problem", code, order.CodeUsage)
	}
	if strings.Contains(out, "MISSING") {
		t.Errorf("output blames a missing program for a config problem:\n%s", out)
	}
}

// runDoctor drives cmdDoctor with a scripted backend and returns the exit code
// and everything it printed.
func runDoctor(t *testing.T, f fake, display int) (int, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "odisplay.json")
	body := `{"version":1,"display":` + itoa(display) + `,"inputs":[{"key":"dp","vcp":15,"host":"0"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODISPLAY_CONFIG", path)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	code := doctor(context.Background(), w, f, cfg)
	w.Flush()
	return code, buf.String()
}

type errFake struct{}

func (errFake) Error() string { return "ddcutil detect: no permission" }

type errFake2 struct{}

func (errFake2) Error() string { return "input source 999 is out of range, 0 to 255" }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
