package backend

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

// These checks are about the shared machinery, not a platform, so they run
// everywhere.

// A name that would break out of a shell still ends up as one argument.
func TestStepQuotesWhatNeedsIt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", "simple"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"a;b", "'a;b'"},
		{"", "''"},
	}
	for _, c := range cases {
		s := Step{Bin: "/bin/x", Args: []string{c.in}}
		if got := s.String(); got != "/bin/x "+c.want {
			t.Errorf("String(%q) = %q, want %q", c.in, got, "/bin/x "+c.want)
		}
	}
}

func TestStepWithNoArgsIsJustTheBinary(t *testing.T) {
	s := Step{Bin: "/usr/local/bin/logi-gate", Args: []string{"2"}}
	if got := s.String(); got != "/usr/local/bin/logi-gate 2" {
		t.Errorf("String() = %q", got)
	}
	empty := Step{Bin: "/usr/local/bin/logi-gate"}
	if got := empty.String(); got != "/usr/local/bin/logi-gate" {
		t.Errorf("String() = %q, want just the binary", got)
	}
}

// One number, two writers. ddcutil takes hex, ddcctl takes decimal, and USB-C
// is 27 either way. The zero padding keeps DisplayPort-1 reading 0x0f, which is
// how a monitor documents it.
func TestVCPIsWrittenBothWaysFromOneNumber(t *testing.T) {
	for _, c := range []struct {
		vcp      int
		hex, dec string
	}{
		{15, "0x0f", "15"},
		{17, "0x11", "17"},
		{27, "0x1b", "27"},
		{0, "0x00", "0"},
	} {
		if got := hexVCP(c.vcp); got != c.hex {
			t.Errorf("hexVCP(%d) = %q, want %q", c.vcp, got, c.hex)
		}
		if got := decVCP(c.vcp); got != c.dec {
			t.Errorf("decVCP(%d) = %q, want %q", c.vcp, got, c.dec)
		}
	}
}

func TestValidVCPBounds(t *testing.T) {
	for _, vcp := range []int{0, 1, 15, 255} {
		if err := validVCP(vcp); err != nil {
			t.Errorf("validVCP(%d) = %v, want nil", vcp, err)
		}
	}
	for _, vcp := range []int{-1, 256, 9999} {
		if err := validVCP(vcp); err == nil {
			t.Errorf("validVCP(%d) = nil, want an error", vcp)
		}
	}
}

func TestPlanTextIsOneCommandPerLine(t *testing.T) {
	p := Plan{Platform: "linux", Steps: []Step{
		{Bin: "/usr/bin/ddcutil", Args: []string{"setvcp", "60", "0x0f", "--display", "1"}},
		{Bin: "/usr/bin/solaar", Args: []string{"config", "MX Keys S", "change-host", "2"}},
	}}
	got := p.Text()
	want := "/usr/bin/ddcutil setvcp 60 0x0f --display 1\n/usr/bin/solaar config 'MX Keys S' change-host 2"
	if got != want {
		t.Errorf("Text() =\n%q\nwant\n%q", got, want)
	}
	lines := len(strings.Split(got, "\n"))
	if lines != 2 {
		t.Errorf("lines = %d, want 2", lines)
	}
}

func TestPlanProgramsAreListedOnceInOrder(t *testing.T) {
	p := Plan{Steps: []Step{
		{Bin: "/usr/bin/ddcutil"},
		{Bin: "/usr/bin/solaar"},
		{Bin: "/usr/bin/solaar"},
	}}
	got := p.Programs()
	want := []string{"/usr/bin/ddcutil", "/usr/bin/solaar"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Programs() = %v, want %v", got, want)
	}
}

// The child environment is fixed, so no user setting can change what a program
// name resolves to.
func TestChildEnvIsFixed(t *testing.T) {
	e := env()
	want := map[string]string{
		"PATH":   "/usr/bin:/bin",
		"LANG":   "C",
		"LC_ALL": "C",
	}
	seen := map[string]bool{}
	for _, kv := range e {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			t.Errorf("%q is not KEY=VALUE", kv)
			continue
		}
		if w, known := want[key]; known {
			if val != w {
				t.Errorf("%s = %q, want %q", key, val, w)
			}
			delete(want, key)
		}
		seen[key] = true
	}
	for key := range want {
		t.Errorf("%s missing from the child environment", key)
	}
	// HOME is kept because the programs need somewhere to put their state, and
	// XDG_CACHE_HOME points inside it so ddcutil does not print a cache warning
	// on every run.
	cache := ""
	for _, kv := range e {
		if k, v, _ := strings.Cut(kv, "="); k == "XDG_CACHE_HOME" {
			cache = v
		}
	}
	if !strings.HasSuffix(cache, "/.cache/odisplay") {
		t.Errorf("XDG_CACHE_HOME = %q, want it under the home cache dir", cache)
	}
	// Nothing leaks in from the caller's environment.
	for _, key := range []string{"LD_PRELOAD", "SHELL", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS"} {
		if seen[key] {
			t.Errorf("%s leaked into the child environment", key)
		}
	}
}

// A child's multi-line output is flattened to one line, because the panel has
// one line to show it in.
func TestFlattenMakesOneLine(t *testing.T) {
	cases := map[string]string{
		"a\nb":         "a b",
		"a\r\nb":       "a b",
		"  a \n\n b  ": "a b",
		"":             "",
	}
	for in, want := range cases {
		if got := flatten(in); got != want {
			t.Errorf("flatten(%q) = %q, want %q", in, got, want)
		}
	}
}

// The output cap is a backstop, so a child cannot grow this process without
// limit. It is one budget for both streams, not one each, and it is safe to
// write from two goroutines because os/exec really does use two.
func TestCaptureStopsAtTheCap(t *testing.T) {
	c := &capture{left: 4}
	if n, err := c.Write([]byte("abcdefgh")); n != 8 || err != nil {
		t.Errorf("Write = %d, %v, want 8, nil", n, err)
	}
	if got := c.String(); got != "abcd" {
		t.Errorf("buffer = %q, want abcd", got)
	}
	// Writing past the cap still reports the full length so the child does not
	// see a broken pipe.
	c2 := &capture{left: 0}
	if n, _ := c2.Write([]byte("xyz")); n != 3 {
		t.Errorf("Write past cap = %d, want 3", n)
	}
	if got := c2.String(); got != "" {
		t.Errorf("buffer = %q, want empty", got)
	}
}

func TestCaptureIsSafeFromTwoGoroutines(t *testing.T) {
	c := &capture{left: 1 << 20}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				c.Write([]byte("0123456789"))
			}
		}()
	}
	wg.Wait()
	if len(c.String()) != 8*200*10 {
		t.Errorf("length = %d, want %d: writes were lost or torn", len(c.String()), 8*200*10)
	}
}

// One budget covers both streams. With a writer each, the cap would be twice
// what it says it is.
func TestCaptureSharesOneBudgetAcrossStreams(t *testing.T) {
	c := &capture{left: 6}
	c.Write([]byte("abc"))
	c.Write([]byte("def"))
	c.Write([]byte("ghi"))
	if got := c.String(); got != "abcdef" {
		t.Errorf("buffer = %q, want abcdef", got)
	}
}
