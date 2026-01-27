// `tempest-grain-agent` runs inside the grain's sandbox, and is the first
// program executed during grain startup. Its file descriptor #3 is a socket
// over which we can speak capnp to the sandstorm server outside the sandbox.
//
// Any APIs available to the grain which don't actually need privileges the grain
// doesn't have should ideally be implemented here; this helps us minimize attack
// surface.
package grainagentmain

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"capnproto.org/go/capnp/v3"
	"golang.org/x/exp/slog"
	spk "sandstorm.org/go/tempest/capnp/package"
	grainagent "sandstorm.org/go/tempest/internal/capnp/grain-agent"
	"sandstorm.org/go/tempest/internal/server/logging"
	"zenhack.net/go/util"
)

type Command struct {
	Args []string
	Env  []string
}

func (c Command) ToOsCmd() *exec.Cmd {
	ret := exec.Command(c.Args[0], c.Args[1:]...)
	ret.Env = c.Env
	return ret
}

func parseCmd(cmd spk.Manifest_Command) (Command, error) {
	argv, err := cmd.Argv()
	if err != nil {
		return Command{}, err
	}
	var args []string
	for i := 0; i < argv.Len(); i++ {
		arg, err := argv.At(i)
		if err != nil {
			return Command{}, err
		}
		args = append(args, arg)
	}
	if len(args) == 0 {
		return Command{}, fmt.Errorf("len(cmd.argv) == 0")
	}
	environ, err := cmd.Environ()
	if err != nil {
		return Command{}, err
	}
	var env []string
	for i := 0; i < environ.Len(); i++ {
		kv := environ.At(i)
		k, err := kv.Key()
		if err != nil {
			return Command{}, err
		}
		v, err := kv.Value()
		if err != nil {
			return Command{}, err
		}
		env = append(env, k.Text()+"="+v.Text())
	}
	return Command{
		Args: args,
		Env:  env,
	}, nil

}

func Main() {
	lg := logging.NewLogger()

	data, err := os.ReadFile("/sandstorm-manifest")
	util.Chkfatal(err)
	msg, err := capnp.Unmarshal(data)
	util.Chkfatal(err)
	manifest, err := spk.ReadRootManifest(msg)
	util.Chkfatal(err)
	appTitle, err := manifest.AppTitle()
	util.Chkfatal(err)
	appTitleText, err := appTitle.DefaultText()
	util.Chkfatal(err)

	if len(os.Args) < 2 {
		panic("Too few arugments")
	}
	buf, err := base64.StdEncoding.DecodeString(os.Args[1])
	util.Chkfatal(err)
	launchMsg := &capnp.Message{Arena: capnp.SingleSegment(buf)}
	launchCmd, err := grainagent.ReadRootLaunchCommand(launchMsg)
	util.Chkfatal(err)

	switch launchCmd.Which() {
	case grainagent.LaunchCommand_Which_continueGrain:
		cmd, err := manifest.ContinueCommand()
		util.Chkfatal(err)
		spawnSpkCmd(lg, appTitleText, cmd)
	case grainagent.LaunchCommand_Which_initGrain:
		index := launchCmd.InitGrain()
		actions, err := manifest.Actions()
		util.Chkfatal(err)
		cmd, err := actions.At(int(index)).Command()
		util.Chkfatal(err)
		spawnSpkCmd(lg, appTitleText, cmd)
	default:
		err := errors.New("unrecognized launch command")
		lg.Error("BUG",
			"error", err,
			"launch command", launchCmd.Which(),
		)
		panic(err)
	}
}

func spawnSpkCmd(lg *slog.Logger, appTitle string, spkCmd spk.Manifest_Command) {
	cmd, err := parseCmd(spkCmd)
	util.Chkfatal(err)

	lg.Info("Starting up app",
		"appTitle", appTitle,
		"command", cmd.Args,
	)

	// Debug: check if command exists and Rosetta is available
	if _, err := os.Stat(cmd.Args[0]); err != nil {
		lg.Error("Command binary not found", "path", cmd.Args[0], "error", err)
	} else {
		lg.Info("Command binary exists", "path", cmd.Args[0])
	}
	if _, err := os.Stat("/tmp/lima-rosetta/rosetta"); err != nil {
		lg.Error("Rosetta not available in sandbox", "error", err)
	} else {
		lg.Info("Rosetta available at /tmp/lima-rosetta/rosetta")
	}
	// Debug: list /run directory and check mount info
	if entries, err := os.ReadDir("/run"); err == nil {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		lg.Info("Contents of /run", "entries", names)
	}
	// Check /proc/self/exe symlink
	if link, err := os.Readlink("/proc/self/exe"); err == nil {
		lg.Info("Current /proc/self/exe", "target", link)
	}
	// Check mount info for /mnt/lima-rosetta
	if data, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		lines := string(data)
		for _, line := range strings.Split(lines, "\n") {
			if strings.Contains(line, "lima-rosetta") {
				lg.Info("Rosetta mount info", "line", line)
			}
		}
	}

	// Test: Try running Rosetta directly to verify it works in the sandbox
	lg.Info("Testing Rosetta directly...")
	testRosetta := exec.Command("/tmp/lima-rosetta/rosetta")
	testOutput, testErr := testRosetta.CombinedOutput()
	if testErr != nil {
		lg.Info("Rosetta direct test result", "output", string(testOutput), "error", testErr)
	} else {
		lg.Info("Rosetta direct test succeeded", "output", string(testOutput))
	}

	// Test: Try running hello-x86_64 if it exists (tests binfmt_misc path)
	if _, err := os.Stat("/tmp/hello-x86_64"); err == nil {
		lg.Info("Testing hello-x86_64 via binfmt_misc...")
		testHello := exec.Command("/tmp/hello-x86_64")
		helloOutput, helloErr := testHello.CombinedOutput()
		if helloErr != nil {
			lg.Error("hello-x86_64 test FAILED", "output", string(helloOutput), "error", helloErr)
		} else {
			lg.Info("hello-x86_64 test SUCCEEDED", "output", string(helloOutput))
		}
	} else {
		lg.Info("hello-x86_64 not found in sandbox, skipping binfmt_misc test")
	}

	// Check if the app binary is x86_64 (ELF magic + machine type)
	if appData, err := os.ReadFile(cmd.Args[0]); err == nil && len(appData) >= 20 {
		if appData[0] == 0x7f && appData[1] == 'E' && appData[2] == 'L' && appData[3] == 'F' {
			// ELF file - check machine type at offset 18 (little-endian 16-bit)
			machine := uint16(appData[18]) | uint16(appData[19])<<8
			if machine == 0x3e { // EM_X86_64
				lg.Info("App binary is x86_64, will use Rosetta via binfmt_misc")
			} else if machine == 0xb7 { // EM_AARCH64
				lg.Info("App binary is ARM64, native execution")
			} else {
				lg.Info("App binary ELF machine type", "machine", fmt.Sprintf("0x%x", machine))
			}
		}
	}

	lg.Info("Starting actual app command...")
	osCmd := cmd.ToOsCmd()

	// TODO: make direct these in a more structured way?
	osCmd.Stdout = os.Stdout
	osCmd.Stderr = os.Stderr

	apiSocket := os.NewFile(3, "supervisor socket")
	osCmd.ExtraFiles = []*os.File{apiSocket}

	util.Chkfatal(osCmd.Start())
	defer os.Exit(1)
	util.Chkfatal(osCmd.Wait())
	lg.Info("App exited; shutting down grain.")
}
