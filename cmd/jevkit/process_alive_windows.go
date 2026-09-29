//go:build windows

package main

import "os"

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Windows FindProcess always succeeds; Signal(0) is not portable.
	// Treat unknown processes as dead so reconcile fails closed.
	_ = p
	return false
}
