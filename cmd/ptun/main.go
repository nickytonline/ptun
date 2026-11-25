// Command ptun exposes a local port at a fresh Pomerium URL, like
// `cloudflared tunnel --url`. See README.md.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nickytonline/ptun/internal/cli"
)

// version is set at release time with -ldflags "-X main.version=...".
var version string

// buildVersion is the release version, else the module version from `go install ...@vX`, else "dev".
// The leading "v" is dropped so every install method prints the same thing as the release (0.2.0).
func buildVersion() string {
	if version != "" {
		return strings.TrimPrefix(version, "v")
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

func main() {
	root, exitCode := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ptun:", err)
		os.Exit(1)
	}
	os.Exit(*exitCode)
}

const rootLong = `ptun shares an app running on your computer at a web address.

  ptun 3000             only you can open the link (you sign in first)
  ptun --public 3000    anyone with the link can open it
  ptun --mcp 3000       share an MCP server (your MCP client gets the URL)
  ptun --stable 3000    use the same link every time
  ptun --name demo 3000 the same link every time, with a name in it

Press Ctrl-C to stop sharing.

First time? Run the setup command your admin gave you, e.g.
  ptun init ssh.example.com:2200`

type runFlags struct {
	public, quiet, showVisitor, noCheck, mcp, stable, noBanner bool
	mcpPath, name                                              string
}

func (f *runFlags) add(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.public, "public", false, "let anyone with the link open it, no sign-in (if your organization allows it)")
	cmd.Flags().BoolVar(&f.mcp, "mcp", false, "share the app as a remote MCP server (prints the MCP URL for your MCP client)")
	cmd.Flags().StringVar(&f.mcpPath, "mcp-path", "/mcp", "path of the MCP endpoint on your app (with --mcp)")
	cmd.Flags().BoolVar(&f.stable, "stable", false, "use the same link every time you run ptun (one live at a time)")
	cmd.Flags().StringVar(&f.name, "name", "", "use the same link every time, with this name in it (implies --stable)")
	cmd.Flags().BoolVar(&f.quiet, "quiet", false, "do not print a line for every request")
	cmd.Flags().BoolVar(&f.showVisitor, "show-visitor", false, "show who opened each request (signed-in visitors only)")
	cmd.Flags().BoolVar(&f.noCheck, "no-check", false, "do not check that your app is running before sharing")
	cmd.Flags().BoolVar(&f.noBanner, "no-banner", false, "do not show the Pomeranian at startup (or set PTUN_NO_BANNER=1)")
}

// share runs the shared flow for `ptun <target>` and `ptun start <target>`.
func share(cmd *cobra.Command, arg string, f *runFlags, exitCode *int) error {
	target, err := cli.ParseTarget(arg)
	if err != nil {
		return err
	}
	dir, err := cli.ConfigDir()
	if err != nil {
		return err
	}
	*exitCode, err = cli.Run(cmd.Context(), cli.Options{
		Target: target, Arg: arg, Public: f.public, MCP: f.mcp, MCPPath: f.mcpPath, Stable: f.stable, Name: f.name, Quiet: f.quiet, ShowVisitor: f.showVisitor, NoCheck: f.noCheck,
		Dir: dir, Out: cmd.ErrOrStderr(),
		Banner: func() { printBanner(cmd.ErrOrStderr(), f.noBanner) },
	})
	return err
}

func newRootCmd() (*cobra.Command, *int) {
	exitCode := new(int)
	var f runFlags
	root := &cobra.Command{
		Use:   "ptun <port|url>",
		Short: "Share an app on your computer at a web address",
		Long:  rootLong,
		Example: `  ptun 3000
  ptun --public 3000
  ptun --mcp 3000      # share an MCP server
  ptun --stable 3000   # same link every run
  ptun --name demo 3000
  ptun http://localhost:8081
  ptun start 3000      # same as ptun 3000`,
		Version:       buildVersion(),
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help() // bare `ptun` shows help, not "missing argument"
			}
			return share(cmd, args[0], &f, exitCode)
		},
	}
	f.add(root)

	start := &cobra.Command{
		Use:     "start <port|url>",
		Short:   "Same as ptun <port|url>",
		Example: "  ptun start 3000\n  ptun start --public 3000",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return share(cmd, args[0], &f, exitCode)
		},
	}
	// Own flag set bound to the same values, so `ptun start --public 3000` works.
	f.add(start)
	root.AddCommand(start, newInitCmd())
	return root, exitCode
}

func newInitCmd() *cobra.Command {
	var broker string
	cmd := &cobra.Command{
		Use:   "init <pomerium-ssh-host>",
		Short: "Save your Pomerium SSH endpoint (the tunnel broker is derived from it)",
		Long: "Give the Pomerium SSH endpoint you use for `ssh -R` (e.g. ssh.example.com:2200).\n" +
			"ptun derives the tunnel broker from it (ssh.X -> tunnels.X). Use --broker if yours lives elsewhere.",
		Example: "  ptun init ssh.example.com:2200\n  ptun init ssh.example.com:2200 --broker tunnels.example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := cli.BrokerFromInit(args[0], broker)
			if err != nil {
				return err
			}
			dir, err := cli.ConfigDir()
			if err != nil {
				return err
			}
			if err := cli.SaveConfig(dir, cli.Config{Broker: host}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved tunnel broker %s to %s\n", host, dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&broker, "broker", "", "tunnel broker host, if it is not tunnels.<your domain>")
	return cmd
}
