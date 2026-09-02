package app

import (
	"flag"
	"strings"
)

// SplitArgs partitions a command line into the flags fs defines and
// everything else.
//
// It exists because internal/infrastructure/config binds a command-line flag
// for every environment-variable knob it supports (-server-port,
// -llm-provider, -storage, ...) inside its own FlagSet, and the standard
// flag package has no notion of two FlagSets sharing one argument list:
// whichever parses first stops at the other's flags. So each binary parses
// its own flags from `own` and hands `rest` to config.Load, which is what
// lets `cloudoptix-api --seed-demo -server-port=9000` work at all. The
// obvious alternative — re-declaring every config flag in each binary — puts
// the two definitions a rename apart from silently diverging, which is
// exactly what config's binding table exists to prevent.
//
// An argument that is not a flag at all (a subcommand, a file path) lands in
// rest, where the caller decides what it means. A genuinely unknown flag also
// lands in rest and is rejected by config.Load's own parse, with the standard
// flag-package message.
func SplitArgs(fs *flag.FlagSet, args []string) (own, rest []string) {
	known := map[string]bool{}
	boolean := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		known[f.Name] = true
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			boolean[f.Name] = true
		}
	})

	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := splitFlagArg(a)
		if name == "" || !known[name] {
			rest = append(rest, a)
			// A bare value belonging to an unknown flag ("-server-port 9000")
			// must travel with it, or config.Load would see "9000" as a
			// positional argument and the flag as valueless.
			if name != "" && !hasValue && !boolean[name] && i+1 < len(args) && !looksLikeFlag(args[i+1]) {
				i++
				rest = append(rest, args[i])
			}
			continue
		}
		switch {
		case hasValue:
			own = append(own, "-"+name+"="+value)
		case boolean[name]:
			own = append(own, "-"+name)
		case i+1 < len(args):
			own = append(own, "-"+name, args[i+1])
			i++
		default:
			own = append(own, "-"+name)
		}
	}
	return own, rest
}

func looksLikeFlag(s string) bool { return len(s) > 1 && strings.HasPrefix(s, "-") }

// splitFlagArg decomposes "-name=value", "--name=value", "-name" or
// "--name". A non-flag argument returns an empty name.
func splitFlagArg(arg string) (name, value string, hasValue bool) {
	if !looksLikeFlag(arg) {
		return "", "", false
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	if n, v, ok := strings.Cut(trimmed, "="); ok {
		return n, v, true
	}
	return trimmed, "", false
}
