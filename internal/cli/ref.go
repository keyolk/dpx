package cli

import (
	"fmt"
	"strings"
)

// splitRef reads an n-part reference (project, config, secret) from the front
// of args and returns it with the arguments left over.
//
// Parts may be joined by slashes or given as separate arguments, in any mix:
// "proj/dev/KEY", "proj/dev KEY" and "proj dev KEY" are the same reference.
// The slash form is what makes a reference one copyable token; the spaced form
// is kept because it is what dpx has always taken. Neither project, config
// nor secret names can contain a slash, so the split is never ambiguous.
func splitRef(args []string, n int) (parts, rest []string, err error) {
	want := []string{"project", "config", "secret"}[:n]
	usage := strings.Join(want, "/")
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("want %s", usage)
	}

	parts = strings.Split(args[0], "/")
	rest = args[1:]
	if len(parts) > n {
		return nil, nil, fmt.Errorf("%q has more parts than %s", args[0], usage)
	}
	for len(parts) < n && len(rest) > 0 {
		parts = append(parts, rest[0])
		rest = rest[1:]
	}
	if len(parts) < n {
		return nil, nil, fmt.Errorf("want %s, got %q", usage, strings.Join(parts, "/"))
	}
	for i, p := range parts {
		// An "=" here means an assignment was swallowed as a ref part —
		// `dpx set proj KEY=v` with the config forgotten.
		if p == "" || strings.ContainsAny(p, "= ") {
			return nil, nil, fmt.Errorf("bad %s %q in %s", want[i], p, usage)
		}
	}
	return parts, rest, nil
}
