package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// requireQuiet refuses to record or compare on a machine that is busy.
//
// A benchmark taken under load is not a slow benchmark, it is no benchmark: a
// rustc build at sixteen cores turned a 1.45 ms parse into 3.5 ms, which as a
// baseline would have enshrined a number nothing can ever regress against, and
// as a comparison would have failed everything.
//
// The check is the one-minute load average against the core count, which is
// crude and is the right kind of crude — it catches the case that matters,
// which is somebody else's build, and it costs nothing.
func requireQuiet(max float64) error {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil // not Linux; the caller gets no protection and no obstacle
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return nil
	}
	load, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil
	}
	if load > max {
		return fmt.Errorf("load average is %.2f, above %.2f: wait for the machine "+
			"to go quiet or pass -maxload. A benchmark taken under load is not a "+
			"slow benchmark, it is no benchmark", load, max)
	}
	return nil
}
