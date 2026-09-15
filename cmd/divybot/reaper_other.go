//go:build !unix

package main

// divybot is a Linux container daemon; everywhere else it is a developer running the binary
// locally, never pid 1, so there are no orphans of ours to collect and no wait4 to collect them
// with. These stubs keep the package building on those platforms.

func waitAllExited() int { return 0 }

func startReaper(<-chan struct{}) {}
