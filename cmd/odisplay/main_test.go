package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	// power is what the fake platform says it would run afterwards, so a test
	// can see whether doctor looked at it.
	power []backend.Step
	// grant is the command the fake platform says a rules file has to name.
	grant string
}

func (f fake) Platform() string { return "fake" }
func (f fake) Monitor() string  { return "ddcutil" }

func (f fake) Power(string) []backend.Step { return f.power }
func (f fake) Grant(string) string         { return f.grant }
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

func (f fake) Devices(context.Context) (backend.Connected, error) {
	return backend.Connected{UsesNames: true, How: "fake"}, nil
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

// The action runs last, so a command for it that is missing is found after
// everything else has already happened. A plan does not carry the action until
// switch builds one, and doctor builds its own plan, so it has to ask for the
// action itself or it calls a machine ready while the command that puts it to
// sleep is not there.
func TestDoctorChecksTheActionAsWell(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "odisplay.json")
	body := `{"version":1,"display":1,"inputs":[{"key":"dp","vcp":15,"host":"0","power":"sleep"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODISPLAY_CONFIG", path)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	run := func(power []backend.Step) (int, string) {
		t.Helper()
		f := fake{
			displays: []backend.Display{{Number: 1, Name: "card2-DP-6", Usable: true}},
			power:    power,
		}
		var buf bytes.Buffer
		w := bufio.NewWriter(&buf)
		code := doctor(context.Background(), w, f, cfg)
		w.Flush()
		return code, buf.String()
	}

	// A command that is there must not be reported as missing, or this check
	// would refuse to call any machine with an action configured ready.
	code, out := run([]backend.Step{{Bin: "/bin/sh"}})
	if code != order.CodeOK {
		t.Errorf("code = %d with the action present, want %d:\n%s", code, order.CodeOK, out)
	}

	code, out = run([]backend.Step{{Bin: "/nowhere/pmset", Args: []string{"sleepnow"}}})
	if code != order.CodeMissing {
		t.Errorf("code = %d, want %d for a missing sleep command:\n%s", code, order.CodeMissing, out)
	}
	if !strings.Contains(out, "/nowhere/pmset") {
		t.Errorf("output does not name the command that is missing:\n%s", out)
	}
}

// The rule a machine needs has to name the command by its path, and the command
// is not the sudo that runs it. Doctor is what someone reads before their first
// switch, so it is where the line belongs, printed and ready to paste.
//
// A rule that has not been added yet is not a failure. Nothing can tell whether
// it is there, because reading /etc/sudoers needs the very privilege being
// asked about, so doctor says what to add and leaves the exit code alone.
func TestDoctorPrintsTheRuleTheActionNeeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "odisplay.json")
	body := `{"version":1,"display":1,"inputs":[{"key":"dp","vcp":15,"host":"0","power":"shutdown"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODISPLAY_CONFIG", path)
	t.Setenv("USER", "heiko")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f := fake{
		displays: []backend.Display{{Number: 1, Name: "card2-DP-6", Usable: true}},
		power:    []backend.Step{{Bin: "/usr/bin/sudo", Args: []string{"-n", "/sbin/shutdown", "-h", "now"}}},
		grant:    "/sbin/shutdown -h now",
	}
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	code := doctor(context.Background(), w, f, cfg)
	w.Flush()
	out := buf.String()

	if code != order.CodeOK {
		t.Errorf("code = %d, want %d: a rule that is not there yet is not a failure\n%s",
			code, order.CodeOK, out)
	}
	if want := "heiko ALL=(root) NOPASSWD: /sbin/shutdown -h now"; !strings.Contains(out, want) {
		t.Errorf("output does not carry the line to paste, want %q in:\n%s", want, out)
	}
	// The command inside sudo has to be there too, and is checked like any
	// other program rather than being taken on trust.
	if !strings.Contains(out, "found  /sbin/shutdown") {
		t.Errorf("output does not check the command the rule names:\n%s", out)
	}
	if strings.Contains(out, "sudo") && !strings.Contains(out, "found  /usr/bin/sudo") {
		t.Errorf("output does not check the program the step runs either:\n%s", out)
	}
}

// A session with no USER set would otherwise print a line granting the command
// to every user on the machine.
func TestDoctorDoesNotGrantTheRuleToEveryone(t *testing.T) {
	t.Setenv("USER", "")
	if got := sudoersUser(); got == "" || got == "ALL" {
		t.Errorf("sudoersUser() = %q with no USER set, want a name that grants nothing", got)
	}
}

// The exit codes are the contract with the panel, and the README is the place a
// person reads them. Code 5 was missing from that table for a while, because an
// edit replaced a three-column row in a two-column table, which does nothing and
// says nothing about having done nothing.
func TestTheReadmeDocumentsEveryExitCode(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	codes := []int{
		order.CodeOK, order.CodeUsage, order.CodeDisplayFailed,
		order.CodeDevicesFailed, order.CodeMissing, order.CodePowerFailed,
	}
	for _, code := range codes {
		if !strings.Contains(body, "\n| "+fmt.Sprint(code)+" |") {
			t.Errorf("the README has no exit code row for %d", code)
		}
	}

	// And no others, so a row for a number nothing returns is caught as well.
	// The exit code table is the only one whose first cell is a number.
	rows := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cell, _, ok := strings.Cut(strings.TrimPrefix(line, "| "), " |")
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(cell); err == nil {
			rows++
		}
	}
	if rows != len(codes) {
		t.Errorf("the README's table has %d exit codes and this program returns %d",
			rows, len(codes))
	}
}
