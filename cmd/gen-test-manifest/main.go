// gen-test-manifest creates a minimal sandstorm-manifest for testing.
// Usage: gen-test-manifest -output /path/to/sandstorm-manifest -binary /path/to/binary
package main

import (
	"flag"
	"os"

	"capnproto.org/go/capnp/v3"
	spk "sandstorm.org/go/tempest/capnp/package"
)

func main() {
	output := flag.String("output", "", "Output path for sandstorm-manifest")
	binary := flag.String("binary", "/bin/test-app", "Path to binary to run")
	title := flag.String("title", "Test App", "App title")
	flag.Parse()

	if *output == "" {
		flag.Usage()
		os.Exit(1)
	}

	// Create a new Cap'n Proto message
	arena := capnp.SingleSegment(nil)
	_, seg, err := capnp.NewMessage(arena)
	if err != nil {
		panic(err)
	}

	// Create the manifest
	manifest, err := spk.NewRootManifest(seg)
	if err != nil {
		panic(err)
	}

	// Set app title
	appTitle, err := manifest.NewAppTitle()
	if err != nil {
		panic(err)
	}
	appTitle.SetDefaultText(*title)

	manifest.SetAppVersion(0)

	// Create continue command
	continueCmd, err := manifest.NewContinueCommand()
	if err != nil {
		panic(err)
	}

	// Set argv
	argv, err := continueCmd.NewArgv(1)
	if err != nil {
		panic(err)
	}
	argv.Set(0, *binary)

	// Create actions list
	actions, err := manifest.NewActions(1)
	if err != nil {
		panic(err)
	}
	action := actions.At(0)

	actionCmd, err := action.NewCommand()
	if err != nil {
		panic(err)
	}
	actionArgv, err := actionCmd.NewArgv(1)
	if err != nil {
		panic(err)
	}
	actionArgv.Set(0, *binary)

	actionNoun, err := action.NewNounPhrase()
	if err != nil {
		panic(err)
	}
	actionNoun.SetDefaultText("test")

	// Serialize to file
	data, err := manifest.Message().Marshal()
	if err != nil {
		panic(err)
	}

	if err := os.WriteFile(*output, data, 0644); err != nil {
		panic(err)
	}
}
