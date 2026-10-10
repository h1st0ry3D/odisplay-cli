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
}

// Config is the whole file.
type Config struct {
	Version int     `json:"version"`
	Display int     `json:"display"`
	Inputs  []Input `json:"inputs"`
	// Devices are the names Solaar needs. macOS moves every paired device in
	// one call and ignores this, so it is only read by the Solaar backend.
	Devices []string `json:"devices,omitempty"`

	path string
}

// Default is the setup this tool was built for: a Dell S2725DC on
// USB-C, DisplayPort and HDMI. Editing the file is how you point it at
// different hardware.
func Default() Config {
	return Config{
		Version: 1,
		Display: 1,
		Inputs: []Input{
			{Key: "usbc", Name: "USB-C", VCP: 27},
			{Key: "dp", Name: "DisplayPort", VCP: 15},
			{Key: "hdmi", Name: "HDMI", VCP: 17},
		},
		Devices: []string{"MX Keys S", "LIFT VERTICAL ERGONOMIC MOUSE"},
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
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s will not parse: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate drops anything unusable and says what it dropped. Settings come
// from a hand-editable file, so an entry the panel no longer has, or a channel
// off the list, is ignored rather than fatal.
func (c *Config) Validate() error {
	if c.Display < 1 || c.Display > 999 {
		c.Display = 1
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
