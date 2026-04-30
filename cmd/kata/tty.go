package main

import "os"

// isTTY reports whether f is a terminal device. Used to gate interactive
// prompts in delete/purge.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
