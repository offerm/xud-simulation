package main

const (
	defaultXudKill    = true
	defaultXudCleanup = true
)

// config defines the configuration for integration tests.
type config struct {
	xudKill    bool `long:"xudkill" description:"whether to kill an hanging xud process which otherwise will fail the test procedure"`
	xudCleanup bool `long:"xudclean" description:"whether to delete xud instances data directories"`
}

// loadConfig initializes and parses the config using a config file and command
// line options.
// TODO: parse command-line args and override the default values
func loadConfig() (*config) {
	return &config{
		xudKill:    defaultXudKill,
		xudCleanup: defaultXudCleanup,
	}
}