package main

import (
	"os/exec"
	"strconv"
	"strings"
)

// runPS returns the resident set size (KiB) of pid via ps. On darwin `ps -o rss`
// reports RSS in KiB, which is the OS-accounted figure we want (includes stacks,
// bufio buffers, and — for loopback — socket buffers), not a Go-heap delta.
func runPS(pid int) (int64, error) {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}
