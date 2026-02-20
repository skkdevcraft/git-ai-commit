package main

// These variables are set at build time via -ldflags.
// Defaults are used when building locally without GoReleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)
