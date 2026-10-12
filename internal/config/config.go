// Package config reads and writes the per-input settings that the panel and
// the CLI share.
//
// The same file drives Linux and macOS. The VCP value is stored as a plain
// integer because the two backends disagree about how to write it: ddcutil
// takes hex (0x1b) and ddcctl takes decimal (27). They are the same number.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
)

// HostOff is the channel that leaves the keyboard and mouse where they are.
const HostOff = "0"

// hosts is the closed set of Easy-Switch channels. A value outside it is
// refused rather than repaired, so the number that reaches a backend is always
// one the panel can display.
var hosts = []string{"0", "1", "2", "3"}

// MaxNameLength caps a display name. The panel shows it on one line in one
// button, and the file can be hand-edited.
const MaxNameLength = 32

// Power is what this machine does with itself once a switch has sent the
// keyboard and mouse away.
//
// The zero value is the default and means nothing happens, so a settings file
// that never mentions power says nothing about it. Sleeping the machine you are
// leaving is the point: a desk with three computers on one monitor does not need
// the other two awake.
type Power string

const (
	// PowerNone leaves this machine alone. It is the default.
	PowerNone Power = ""
	// PowerSleep suspends it.
	PowerSleep Power = "sleep"
	// PowerShutdown powers it off.
	PowerShutdown Power = "shutdown"
)

// powers is the closed set, in the order a UI should offer them.
var powers = []Power{PowerNone, PowerSleep, PowerShutdown}

// ValidPower reports whether a value is one of them. The empty string is valid
// and means none, because that is how a file that does not mention it reads.
func ValidPower(v string) bool {
	for _, p := range powers {
		if string(p) == v {
			return true
		}
	}
	return false
}

// Powers returns the closed set as words, for a message that says what to type.
// The first is spelled out because nothing is not something to type.
func Powers() []string {
	out := make([]string, 0, len(powers))
	for _, p := range powers {
		if p == PowerNone {
			out = append(out, "none")
			continue
		}
		out = append(out, string(p))
	}
	return out
}

// ParsePower reads a word as an action. "none", "off" and the empty string all
// mean the same, because they are the same word for a person typing it.
func ParsePower(s string) (Power, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none", "off", "no", "0":
		return PowerNone, true
	case "sleep", "suspend":
		return PowerSleep, true
	case "shutdown", "poweroff", "shut", "down":
		return PowerShutdown, true
	}
	return PowerNone, false
}

// Host is an Easy-Switch channel.
//
// It unmarshals from a JSON string or a JSON number, because the file is meant
// to be hand-edited and "host": 2 is the obvious thing to write. Anything else
// leaves it empty, which reads as off.
type Host string

// UnmarshalJSON accepts "2" and 2 alike.
func (h *Host) UnmarshalJSON(b []byte) error {
	raw := string(b)
	if unquoted, err := strconv.Unquote(raw); err == nil {
		*h = Host(unquoted)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return nil // leave it off rather than refusing the whole file
	}
	*h = Host(n.String())
	return nil
}

// Input is one monitor input source.
type Input struct {
	Key  string `json:"key"`
	Name string `json:"name,omitempty"`
	// VCP is the MCCS input source value, decimal. 15 DisplayPort-1, 17 HDMI-1,
	// 27 USB-C.
	VCP int `json:"vcp"`
	// Host is the Easy-Switch channel to move the devices to, or HostOff.
	Host Host `json:"host,omitempty"`
	// Hidden keeps this input out of the panel and the menu without removing
	// it. It is a view setting, not a switch: the input is still configured,
	// `switch` still reaches it, and unhiding it is one click away.
	//
	// It exists for the input a machine is already on, which is noise in a
	// list of things to switch to, and for anything unplugged for now.
	Hidden bool `json:"hidden,omitempty"`
	// Power is what this machine does with itself after a switch sends the
	// keyboard and mouse away. It runs only if the whole switch succeeded, and
	// only for the machine being left, which is this one.
	Power Power `json:"power,omitempty"`
}

// Visible returns the inputs a panel or a menu should offer, in order. A hidden
// input is left out here and nowhere else.
func (c Config) Visible() []Input {
	out := make([]Input, 0, len(c.Inputs))
	for _, in := range c.Inputs {
		if !in.Hidden {
			out = append(out, in)
		}
	}
	return out
}

// Config is the whole file.
type Config struct {
	Version int `json:"version"`
	Display int `json:"display"`
	// Displays overrides Display for a named platform, keyed the way Go names
	// it: "linux", "darwin".
	//
	// It exists because the two programs number displays independently, and
	// the same monitor driven from a Linux laptop and a Mac is display 1 on
	// one and something else on the other. Sharing one settings file across
	// both is otherwise a way to point a switch at the wrong screen.
	Displays map[string]int `json:"displays,omitempty"`
	Inputs   []Input        `json:"inputs"`
	// Devices are the names Solaar needs. macOS moves every paired device in
	// one call and ignores this, so it is only read by the Solaar backend.
	Devices []string `json:"devices,omitempty"`

	// Monitor is the program that moves the display on this machine, filled in
	// by `list --json` and never read from a file. It is here so a caller can
	// name the right program rather than assuming one: the two macOS ones are
	// for different Macs, and naming the wrong one is misleading.
	Monitor string `json:"monitor,omitempty"`

	path string
}

// platforms are the keys accepted in Displays. A closed set on purpose: a
// typo like "macos" would otherwise be ignored in silence, leaving the wrong
// display number in charge, and a wrong display number is a switch that does
// nothing while the keyboard still moves.
var platforms = []string{"linux", "darwin"}

// ValidPlatform reports whether a name is one Displays may be keyed by.
func ValidPlatform(name string) bool {
	for _, p := range platforms {
		if p == name {
			return true
		}
	}
	return false
}

// Platforms returns the accepted keys, for a message that says what to type.
func Platforms() []string { return append([]string(nil), platforms...) }

// DisplayNumber is the display to use here, which is the platform's own entry
// if it has one and the shared default otherwise.
func (c Config) DisplayNumber() int {
	return resolveDisplay(c.Display, c.Displays, runtime.GOOS)
}

// resolveDisplay takes the platform as an argument rather than reading it, so
// one test can check what the other machine will do. A resolution rule that can
// only ever be checked for the platform it runs on is a rule nobody can check.
func resolveDisplay(fallback int, per map[string]int, goos string) int {
	if n, ok := per[goos]; ok && n >= 1 {
		return n
	}
	return fallback
}

// SetDisplayNumber records the number for the platform this is running on, so
// running `set display` on one machine cannot change the other one's. An entry
// that matches the shared default is dropped, because a file carrying both is
// strictly harder to read than one carrying either.
func (c *Config) SetDisplayNumber(n int) {
	if c.Displays == nil {
		c.Displays = map[string]int{}
	}
	c.Displays[runtime.GOOS] = n
	if n == c.Display {
		delete(c.Displays, runtime.GOOS)
		if len(c.Displays) == 0 {
			c.Displays = nil
		}
	}
}

// Default is the setup this tool was built for: a Dell S2725DC on
// USB-C, DisplayPort and HDMI. Editing the file is how you point it at
// different hardware.
func Default() Config {
	return Config{
		Version: 1,
		Display: 1,
		// Host is off for every one of them. Left unset it is the empty
		// string, which is not a channel and not the same as off.
		Inputs: []Input{
			{Key: "usbc", Name: "USB-C", VCP: 27, Host: HostOff},
			{Key: "dp", Name: "DisplayPort", VCP: 15, Host: HostOff},
			{Key: "hdmi", Name: "HDMI", VCP: 17, Host: HostOff},
		},
		// The order is the order they are sent, and the mouse goes first.
		//
		// The sequence stops at the first device that fails, so which is sent
		// first decides what a failure leaves you holding. Mouse first means a
		// keyboard that will not move leaves the pointer here, and a keyboard
		// that moves and a mouse that does not leaves the keyboard here. Both
		// leave one way to act; the display has already moved either way, which
		// is the part that matters and is not affected by this.
		Devices: []string{"LIFT VERTICAL ERGONOMIC MOUSE", "MX Keys S"},
	}
}

// Path is where the file lives on this platform. Settings are configuration,
// not state, so this is the config directory rather than the state directory
// the panel used before the CLI existed.
func Path() (string, error) {
	if v := os.Getenv("ODISPLAY_CONFIG"); v != "" {
		// Accept either the file itself or the directory holding it, because
		// both readings are natural and picking the wrong one silently
		// pointed at a path that could never exist.
		if strings.HasSuffix(strings.ToLower(v), ".json") {
			return v, nil
		}
		return filepath.Join(v, "odisplay.json"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "odisplay", "odisplay.json"), nil
	}
	if v, err := os.UserConfigDir(); err == nil && v != "" {
		return filepath.Join(v, "odisplay", "odisplay.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no config directory: %w", err)
	}
	return filepath.Join(home, ".config", "odisplay", "odisplay.json"), nil
}

// Load reads the file, falling back to the defaults when it is absent. A file
// that will not parse is an error rather than a silent reset, so a typo cannot
// quietly throw away a hand-edited setup.
func Load(path string) (Config, error) {
	cfg := Default()
	cfg.path = path

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// Validated like a file on disk, because these are what run on a
		// machine that has never been configured. Returning them unvalidated
		// is how every input came to carry an empty channel.
		if err := cfg.Validate(); err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}

	// encoding/json decodes an array element by element into the slice it is
	// given, merging fields rather than replacing them. Unmarshalling over the
	// defaults therefore gave every input the default name at its own
	// position: a file listing only dp and hdmi named them "USB-C" and
	// "DisplayPort". Start from nothing so the file is the whole truth.
	//
	// Devices is left alone. It is a list of plain strings with nothing to
	// merge, and a file that does not mention devices is better served by the
	// defaults than by none.
	cfg.Inputs = nil
	cfg.Displays = nil

	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s will not parse: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate drops what is unusable and refuses what would be dangerous.
//
// A channel off the closed list is dropped to "off" rather than refused: the
// result is that the keyboard and mouse stay where they are, which is the safe
// direction, and the file is somewhere a person edits by hand.
//
// The display number is different. Repairing an out-of-range one to 1 would
// point every switch at whichever monitor happens to be first, and the panel
// would report success. A number that names no monitor is refused instead.
func (c *Config) Validate() error {
	if c.Display < 1 || c.Display > 999 {
		return fmt.Errorf("display is %d, which is not a display number: it must be 1 to 999, "+
			"from `ddcutil detect`", c.Display)
	}
	for name, n := range c.Displays {
		if !ValidPlatform(name) {
			return fmt.Errorf("displays has %q, which is not a platform: use one of %s. "+
				"A name that does not match would be ignored, and that leaves the wrong "+
				"display number in charge", name, strings.Join(platforms, ", "))
		}
		if n < 1 || n > 999 {
			return fmt.Errorf("displays.%s is %d, which is not a display number: it must be "+
				"1 to 999, from `ddcutil detect` or `ddcctl -m`", name, n)
		}
	}
	kept := c.Inputs[:0]
	for _, in := range c.Inputs {
		if in.Key == "" {
			continue
		}
		if in.Name != "" {
			in.Name = CleanName(in.Name)
		}
		if !ValidHost(string(in.Host)) {
			in.Host = HostOff
		}
		// An action this does not know is dropped to doing nothing rather than
		// refused. The safe direction is a machine that stays on, and a file
		// that says something else is a file to read, not one to obey.
		if !ValidPower(string(in.Power)) {
			in.Power = PowerNone
		}
		kept = append(kept, in)
	}
	c.Inputs = kept
	for i := range c.Devices {
		c.Devices[i] = strings.TrimSpace(c.Devices[i])
	}
	return nil
}

// Input returns the named input.
func (c Config) Input(key string) (Input, bool) {
	for _, in := range c.Inputs {
		if in.Key == key {
			return in, true
		}
	}
	return Input{}, false
}

// Save writes the file, creating the directory, and replaces it atomically so
// an interrupted write cannot leave a half file that parses as no settings.
func (c Config) Save() error {
	if c.path == "" {
		return errors.New("config has no path")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(c.path), err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".odisplay-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, c.path)
}

// Set binds the file path so a loaded config can be saved back.
func (c *Config) SetPath(path string) { c.path = path }

// ValidHost reports whether a value is one of the four channels.
func ValidHost(v string) bool {
	for _, h := range hosts {
		if h == v {
			return true
		}
	}
	return false
}

// Hosts returns the closed channel list, for a UI to render.
func Hosts() []string { return append([]string(nil), hosts...) }

// NextHost steps a channel to the next one, wrapping at the end.
func NextHost(current string) string {
	if !ValidHost(current) {
		return HostOff
	}
	for i, h := range hosts {
		if h == current {
			return hosts[(i+1)%len(hosts)]
		}
	}
	return HostOff
}

// CleanName strips what a single-line label cannot carry. A name reaches a
// button and the panel's status line, and it comes back off disk where it may
// have been edited by hand.
func CleanName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if len(b.String()) >= MaxNameLength {
			break
		}
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
