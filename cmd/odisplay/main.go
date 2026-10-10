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
	"fmt"
	"io"
	"os"
	"os/signal"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch args[0] {
	case "switch":
		return cmdSwitch(ctx, args[1:], out, errOut)
	case "list":
		return cmdList(args[1:], out, errOut)
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
  odisplay doctor                       check the programs this needs are installed
  odisplay set name <input> <name>
  odisplay set host <input> <0-3>
  odisplay set display <n>
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

// doctor checks that the programs this platform needs are installed. It runs
// nothing that touches hardware.
func cmdDoctor(ctx context.Context, args []string, out *bufio.Writer, errOut *os.File) int {
	if len(args) != 0 {
		fmt.Fprintln(errOut, "doctor takes no arguments")
		return order.CodeUsage
	}
	cfg, ok := load(errOut)
	if !ok {
		return order.CodeUsage
	}
	b := backend.New(cfg.Devices)

	fmt.Fprintf(out, "platform: %s\n", b.Platform())
	fmt.Fprintf(out, "config:   ")
	if path, err := config.Path(); err == nil {
		fmt.Fprintln(out, path)
	} else {
		fmt.Fprintln(out, "(none found)")
	}

	seen := map[string]bool{}
	problems := 0
	for _, in := range cfg.Inputs {
		plan, err := b.Plan(cfg.Display, in.VCP, string(in.Host))
		if err != nil {
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
				problems++
			}
		}
	}
	if problems > 0 {
		fmt.Fprintln(out, "\nsomething is missing. Switches will fail until it is installed.")
		return order.CodeMissing
	}

	// The display number is ddcutil's own index and it is positional, so it
	// shifts when a display stops enumerating. Report what is actually there
	// rather than trusting the file, because the failure otherwise shows up
	// later as a switch that does nothing.
	fmt.Fprintf(out, "\ndisplays (the config says %d):\n", cfg.Display)
	displays, err := b.Detect(ctx)
	if err != nil {
		fmt.Fprintf(out, "  could not read them: %v\n", err)
		fmt.Fprintln(out, "\nready, but the display number is unverified.")
		return order.CodeOK
	}
	if len(displays) == 0 {
		fmt.Fprintln(out, "  none")
	}
	chosen := -1
	for _, d := range displays {
		mark := "  "
		if d.Number == cfg.Display {
			mark = "* "
			chosen = d.Number
		}
		note := ""
		if !d.Usable {
			note = "  (" + d.Why + ")"
		}
		fmt.Fprintf(out, "%sdisplay %d  %-22s%s\n", mark, d.Number, d.Name, note)
	}

	if chosen < 0 {
		fmt.Fprintf(out, "\ndisplay %d is not in that list. Fix it with `odisplay set display <n>`.\n", cfg.Display)
		return order.CodeUsage
	}
	if cfg.Display > 0 && cfg.Display <= len(displays) && !displays[cfg.Display-1].Usable {
		chosen := displays[cfg.Display-1]
		fmt.Fprintf(out, "\ndisplay %d cannot be switched: %s\n", chosen.Number, chosen.Why)
		fmt.Fprintln(out, "Every switch will quietly do nothing, and the devices will still move.")
		for _, d := range displays {
			if d.Usable {
				fmt.Fprintf(out, "Display %d (%s) can be. Set it with `odisplay set display %d`.\n", d.Number, d.Name, d.Number)
				return order.CodeUsage
			}
		}
		fmt.Fprintln(out, "No display on this machine can be switched right now.")
		return order.CodeUsage
	}
	fmt.Fprintln(out, "\nready.")
	return order.CodeOK
}

func cmdList(args []string, out *bufio.Writer, errOut *os.File) int {
	if len(args) != 0 {
		fmt.Fprintln(errOut, "list takes no arguments")
		return order.CodeUsage
	}
	cfg, ok := load(errOut)
	if !ok {
		return order.CodeUsage
	}
	if path, err := config.Path(); err == nil {
		fmt.Fprintf(out, "config: %s\n", path)
	}
	fmt.Fprintf(out, "display: %d\n", cfg.Display)
	for _, in := range cfg.Inputs {
		name := in.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Fprintf(out, "%-8s vcp %-4d host %-4s %s\n", in.Key, in.VCP, in.Host, name)
	}
	return order.CodeOK
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
		cfg.Display = n
		if err := cfg.Save(); err != nil {
			fmt.Fprintln(errOut, err)
			return order.CodeUsage
		}
		fmt.Fprintf(out, "display = %d\n", n)
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

func parseNumber(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}
