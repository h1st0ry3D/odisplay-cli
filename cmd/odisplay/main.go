// Command odisplay switches a monitor's input source, and the Logitech
// keyboard and mouse that go with it.
//
// It exists so that the order of operations lives in one place. The panel and
// any menu bar app both call it rather than each deciding for themselves, and
// the exit code says where the user's keyboard ended up.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"

	"github.com/h1st0ry3D/odisplay-cli/internal/backend"
	"github.com/h1st0ry3D/odisplay-cli/internal/config"
	"github.com/h1st0ry3D/odisplay-cli/internal/order"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	errOut := os.Stderr

	if len(args) == 0 {
		usage(errOut)
		return order.CodeUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()

	switch args[0] {
	case "switch":
		return cmdSwitch(ctx, args[1:], out, errOut)
	case "list":
		return cmdList(ctx, args[1:], out, errOut)
	case "set":
		return cmdSet(args[1:], out, errOut)
	case "doctor":
		return cmdDoctor(ctx, args[1:], out, errOut)
	case "plan":
		return cmdPlan(args[1:], out, errOut)
	case "path":
		return cmdPath(out, errOut)
	case "-h", "--help", "help":
		usage(out)
		return order.CodeOK
	default:
		fmt.Fprintf(errOut, "unknown command %q\n", args[0])
		usage(errOut)
		return order.CodeUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `odisplay - switch a monitor input, and the devices that go with it

  odisplay switch <input> [--dry-run]   move the monitor, then the keyboard and mouse
  odisplay plan <input>                 print the commands without running them
  odisplay list                         show the configured inputs
  odisplay list --json                  the same, for the panel to read
  odisplay doctor                       check the programs this needs are installed
  odisplay set name <input> <name>
  odisplay set host <input> <0-3>
  odisplay set hidden <input> <on|off>
  odisplay set power <input> <none|sleep|shutdown>
  odisplay set display <n>           for this platform only
  odisplay path                         print the config file's location

Try --dry-run before the first real switch. It builds every command and runs
none of them, so you can see what would happen to your keyboard.

Exit codes, which tell the caller where the keyboard ended up:
  0  done
  1  usage or config problem
  2  the display did not move, so the devices were left alone
  3  the display moved but the devices did not
  4  a program this needs is not installed
`)
}

// load reads the config, or returns false.
//
// A missing file is fine and yields the defaults. A file that exists but will
// not parse is refused: carrying on with the defaults would mean switching the
// wrong display and the wrong host, on the assumption that the numbers happened
// to match.
func load(errOut *os.File) (config.Config, bool) {
	path, err := config.Path()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return config.Config{}, false
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(errOut, "config not usable, refusing to guess: %v\n", err)
		return config.Config{}, false
	}
	cfg.SetPath(path)
	return cfg, true
}

func parseDryRun(args []string) (rest []string, dry bool) {
	for _, a := range args {
		if a == "--dry-run" || a == "-n" {
			dry = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, dry
}

func cmdSwitch(ctx context.Context, args []string, out *bufio.Writer, errOut *os.File) int {
	rest, dry := parseDryRun(args)
	if len(rest) != 1 {
		fmt.Fprintln(errOut, "switch takes one input key")
		return order.CodeUsage
	}
	cfg, ok := load(errOut)
	if !ok {
		return order.CodeUsage
	}
	res := order.Switch(ctx, backend.New(cfg.Devices), cfg, rest[0], dry)
	fmt.Fprintln(out, res.Message)
	if res.Detail != "" && res.Code != order.CodeOK {
		fmt.Fprintln(errOut, res.Detail)
	}
	return res.Code
}

// plan is switch with dry-run forced on. It exists so the dry run can be bound
// to a key or a menu item without anyone having to remember a flag.
func cmdPlan(args []string, out *bufio.Writer, errOut *os.File) int {
	if len(args) != 1 {
		fmt.Fprintln(errOut, "plan takes one input key")
		return order.CodeUsage
	}
	cfg, ok := load(errOut)
	if !ok {
		return order.CodeUsage
	}
	res := order.Switch(context.Background(), backend.New(cfg.Devices), cfg, args[0], true)
	fmt.Fprintln(out, res.Message)
	return res.Code
}

// doctor checks that the programs this platform needs are installed, and that
// the display number is one this machine actually has. It runs nothing that
// touches hardware.
func cmdDoctor(ctx context.Context, args []string, out *bufio.Writer, errOut *os.File) int {
	if len(args) != 0 {
		fmt.Fprintln(errOut, "doctor takes no arguments")
		return order.CodeUsage
	}
	cfg, ok := load(errOut)
	if !ok {
		return order.CodeUsage
	}
	return doctor(ctx, out, backend.New(cfg.Devices), cfg)
}

// doctor is cmdDoctor with the backend supplied, so its decisions can be
// checked against a scripted one.
func doctor(ctx context.Context, out *bufio.Writer, b backend.Backend, cfg config.Config) int {
	fmt.Fprintf(out, "platform: %s\n", b.Platform())
	fmt.Fprintf(out, "config:   ")
	if path, err := config.Path(); err == nil {
		fmt.Fprintln(out, path)
	} else {
		fmt.Fprintln(out, "(none found)")
	}

	seen := map[string]bool{}
	problems := 0
	missing := 0
	for _, in := range cfg.Inputs {
		plan, err := b.Plan(cfg.Display, in.VCP, string(in.Host))
		if err != nil {
			// A value the config got wrong is not a missing program. The
			// same condition on `switch` exits 1, so it has to exit 1 here
			// too, or the exit code means two things.
			fmt.Fprintf(out, "  %-6s BAD  %v\n", in.Key, err)
			problems++
			continue
		}
		for _, prog := range plan.Programs() {
			if seen[prog] {
				continue
			}
			seen[prog] = true
			if backend.Installed(prog) {
				fmt.Fprintf(out, "  found  %s\n", prog)
			} else {
				fmt.Fprintf(out, "  MISSING %s\n", prog)
				missing++
			}
		}
	}
	if missing > 0 {
		fmt.Fprintln(out, "\nsomething is missing. Switches will fail until it is installed.")
		return order.CodeMissing
	}
	if problems > 0 {
		fmt.Fprintln(out, "\nthe settings have a problem in them. Fix that before switching.")
		return order.CodeUsage
	}

	// The display number is ddcutil's own index, read from what it prints.
	// Report what is actually there rather than trusting the file, because the
	// failure otherwise shows up later as a switch that does nothing while the
	// devices still move.
	fmt.Fprintf(out, "\ndisplays (the config says %d on %s):\n", cfg.DisplayNumber(), runtime.GOOS)
	displays, err := b.Detect(ctx)
	if err != nil {
		// Exit 0 means "done", and a caller scripting `doctor && switch`
		// would proceed on a display number nothing has checked.
		fmt.Fprintf(out, "  could not read them: %v\n", err)
		fmt.Fprintln(out, "\nnot checked. Treat the display number as unknown until this works.")
		return order.CodeMissing
	}
	if len(displays) == 0 {
		fmt.Fprintln(out, "  none")
	}
	chosen := false
	for _, d := range displays {
		if d.Usable {
			mark := "  "
			if d.Number == cfg.DisplayNumber() {
				mark = "* "
				chosen = true
			}
			fmt.Fprintf(out, "%sdisplay %-3d %s\n", mark, d.Number, d.Name)
			continue
		}
		if d.Number > 0 {
			// Numbered but not answering. The number may well be right;
			// saying it does not exist would send someone looking for a
			// different number when the problem is the port or the cable.
			fmt.Fprintf(out, "  display %-3d %s\n", d.Number, d.Why)
			continue
		}
		// A connector the program rejected outright, which is the Linux case:
		// it numbers what it accepts and gives no number to what it does not.
		fmt.Fprintf(out, "  (not addressable) %-14s %s\n", d.Name, d.Why)
	}

	if !chosen {
		fmt.Fprintf(out, "\ndisplay %d is not one this machine accepts, so every switch would do nothing.\n", cfg.DisplayNumber())
		usable := backend.Addressable(displays)
		if len(usable) == 0 {
			fmt.Fprintln(out, "No display on this machine can be switched right now.")
			fmt.Fprintln(out, "If the monitor is on, this is usually the connection:")
			fmt.Fprintln(out, "on Apple Silicon, a built-in HDMI port often has no working DDC path.")
		} else {
			fmt.Fprintf(out, "Display %d (%s) is. Set it with `odisplay set display %d`.\n",
				usable[0].Number, usable[0].Name, usable[0].Number)
		}
		return order.CodeUsage
	}
	fmt.Fprintln(out, "\nready.")
	return order.CodeOK
}

func cmdList(ctx context.Context, args []string, out *bufio.Writer, errOut *os.File) int {
	// --json is how the panel reads its settings. It prints the config as it
	// stands after validation, so the panel renders exactly the numbers that
	// would be sent, rather than reimplementing the rules that decide them.
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
			continue
		}
		fmt.Fprintf(errOut, "unknown option %q for list\n", a)
		return order.CodeUsage
	}

	cfg, ok := load(errOut)
	if !ok {
		return order.CodeUsage
	}

	// Whoever reads this wants the number in force here and the name of the
	// program that will use it, not the file's two views of the number. The
	// `display` in the output is always the answer for the machine asking;
	// `displays` still carries every platform's for anyone editing the file.
	b := backend.New(cfg.Devices)
	effective := cfg
	effective.Display = cfg.DisplayNumber()
	effective.Monitor = b.Monitor()

	if asJSON {
		raw, err := json.MarshalIndent(effective, "", "  ")
		if err != nil {
			fmt.Fprintln(errOut, err)
			return order.CodeUsage
		}
		fmt.Fprintln(out, string(raw))
		return order.CodeOK
	}

	if path, err := config.Path(); err == nil {
		fmt.Fprintf(out, "config: %s\n", path)
	}
	fmt.Fprintf(out, "platform: %s\n", runtime.GOOS)
	fmt.Fprintf(out, "display: %d", effective.Display)
	if len(cfg.Displays) > 0 {
		fmt.Fprintf(out, "  (shared default %d", cfg.Display)
		for _, name := range config.Platforms() {
			if n, ok := cfg.Displays[name]; ok {
				fmt.Fprintf(out, ", %s %d", name, n)
			}
		}
		fmt.Fprint(out, ")")
	}
	fmt.Fprintln(out)
	for _, in := range cfg.Inputs {
		name := in.Name
		if name == "" {
			name = "(unnamed)"
		}
		mark := " "
		if in.Hidden {
			mark = "h"
		}
		power := string(in.Power)
		if power == "" {
			power = "none"
		}
		fmt.Fprintf(out, "%s %-8s vcp %-4d host %-4s power %-8s %s\n",
			mark, in.Key, in.VCP, in.Host, power, name)
	}
	if len(cfg.Inputs) > 0 {
		fmt.Fprintln(out, "  (h marks an input the panel and the menu leave out)")
	}

	reportDevices(ctx, out, b, cfg)
	return order.CodeOK
}

// reportDevices prints the Logitech devices, configured and seen.
//
// Deliberately not in `list --json`. Finding them runs solaar, which takes
// nearly three seconds, and the panel reads the json every time it opens; a
// menu that took three seconds to appear would be worse than one that does not
// say which keyboard is plugged in.
//
// The two lists are shown together because the interesting thing is where they
// disagree. A configured name Solaar cannot find is a switch that moves the
// monitor and then fails on the devices, which is the case that costs someone
// their keyboard.
func reportDevices(ctx context.Context, out *bufio.Writer, b backend.Backend, cfg config.Config) {
	fmt.Fprintln(out)
	if len(cfg.Devices) == 0 {
		fmt.Fprintln(out, "devices configured: none, so a switch never moves the keyboard or mouse")
	} else {
		fmt.Fprintf(out, "devices configured (%d): %s\n", len(cfg.Devices), strings.Join(cfg.Devices, ", "))
	}

	seen, err := b.Devices(ctx)
	if err != nil {
		fmt.Fprintf(out, "devices connected:  unknown, %v\n", err)
		return
	}
	if len(seen.Devices) == 0 {
		fmt.Fprintf(out, "devices connected:  none seen by %s\n", seen.How)
		return
	}
	fmt.Fprintf(out, "devices connected (%s):\n", seen.How)
	for _, d := range seen.Devices {
		state := "online"
		if !d.Online {
			state = d.Why
		}
		line := "  " + d.Name
		if d.Kind != "" {
			line += "  (" + d.Kind + ")"
		}
		if d.Host != "" {
			line += "  host " + d.Host
		}
		fmt.Fprintf(out, "%-52s %s\n", line, state)
	}

	// A device that is not answering does not report the name Solaar matches
	// on, only a display name above it. So an offline device can be compared
	// with nothing, and saying its name is wrong because it was not found would
	// be a claim the output cannot support.
	var online []string
	offline := 0
	for _, d := range seen.Devices {
		if d.Online {
			online = append(online, d.Name)
		} else {
			offline++
		}
	}

	if offline > 0 {
		fmt.Fprintf(out, "\n%d device(s) are offline, which is what a device switched to another\n", offline)
		fmt.Fprintln(out, "machine looks like. Their names cannot be checked until they answer.")
	}

	if !seen.UsesNames {
		// On a platform that picks the devices itself, the settings' names are
		// not what it matches on, so a list that does not line up is not a
		// problem.
		fmt.Fprintln(out, "\nthis platform picks the devices itself, so the names above do not have to match")
		return
	}

	var missing []string
	for _, want := range cfg.Devices {
		found := false
		for _, name := range online {
			if name == want {
				found = true
			}
		}
		if !found {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 || offline > 0 {
		return
	}
	fmt.Fprintf(out, "\nconfigured but not seen: %s\n", strings.Join(missing, ", "))
	fmt.Fprintln(out, "a switch that includes one of these will move the monitor and then fail on the devices")
}

func cmdPath(out *bufio.Writer, errOut *os.File) int {
	path, err := config.Path()
	if err != nil {
		fmt.Fprintln(errOut, err)
		return order.CodeUsage
	}
	fmt.Fprintln(out, path)
	return order.CodeOK
}

// cmdSet writes one setting. The three fields take different numbers of
// arguments, so the arity is checked per field rather than once for all of
// them: `display` is global and takes no input key.
func cmdSet(args []string, out *bufio.Writer, errOut *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "set takes a field and a value")
		return order.CodeUsage
	}
	cfg, ok := load(errOut)
	if !ok {
		return order.CodeUsage
	}
	field := args[0]

	if field == "display" {
		if len(args) != 2 {
			fmt.Fprintln(errOut, "set display takes one number, 1 to 999")
			return order.CodeUsage
		}
		n, err := parseNumber(args[1])
		if err != nil || n < 1 || n > 999 {
			fmt.Fprintln(errOut, "display must be a number, 1 to 999")
			return order.CodeUsage
		}
		// This platform's entry, so setting it on one machine cannot change
		// the other's. The same monitor is a different index under ddcutil
		// than under ddcctl.
		cfg.SetDisplayNumber(n)
		if err := cfg.Save(); err != nil {
			fmt.Fprintln(errOut, err)
			return order.CodeUsage
		}
		fmt.Fprintf(out, "display = %d on %s\n", n, runtime.GOOS)
		if len(cfg.Displays) == 0 {
			fmt.Fprintln(out, "that is the same as the shared default, so no per-platform entry was needed")
		}
		return order.CodeOK
	}

	if len(args) < 3 {
		fmt.Fprintf(errOut, "set %s takes an input key and a value\n", field)
		return order.CodeUsage
	}
	key, value := args[1], strings.Join(args[2:], " ")

	idx := -1
	for i, in := range cfg.Inputs {
		if in.Key == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		fmt.Fprintf(errOut, "no input called %q\n", key)
		return order.CodeUsage
	}

	switch field {
	case "name":
		cfg.Inputs[idx].Name = config.CleanName(value)
	case "host":
		if !config.ValidHost(value) {
			fmt.Fprintf(errOut, "host must be one of %s\n", strings.Join(config.Hosts(), " "))
			return order.CodeUsage
		}
		cfg.Inputs[idx].Host = config.Host(value)
	case "power":
		action, ok := config.ParsePower(value)
		if !ok {
			fmt.Fprintf(errOut, "power must be one of %s\n", strings.Join(config.Powers(), " "))
			return order.CodeUsage
		}
		cfg.Inputs[idx].Power = action
	case "hidden":
		on, ok := parseOnOff(value)
		if !ok {
			fmt.Fprintln(errOut, "hidden must be on or off")
			return order.CodeUsage
		}
		cfg.Inputs[idx].Hidden = on
	default:
		fmt.Fprintf(errOut, "unknown field %q\n", field)
		return order.CodeUsage
	}

	if err := cfg.Save(); err != nil {
		fmt.Fprintln(errOut, err)
		return order.CodeUsage
	}
	fmt.Fprintf(out, "%s %s = %s\n", key, field, value)
	return order.CodeOK
}

// parseNumber reads a whole string as a number. strconv.Atoi rather than
// Sscanf, which takes the leading digits of "2abc" and reports success, so
// `set display 2abc` would quietly write 2.
// parseOnOff reads the two words a boolean setting is written with, rather than
// accepting anything truthy. A value that is not one of them is refused, so a
// typo cannot silently hide an input or bring one back.
func parseOnOff(s string) (value, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "yes", "1":
		return true, true
	case "off", "false", "no", "0":
		return false, true
	}
	return false, false
}

func parseNumber(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
