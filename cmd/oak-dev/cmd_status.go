package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"oak-dev/internal/components"
	"oak-dev/internal/config"
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "status",
		GroupID: groupInspect,
		Short:   "Show what's configured and whether Oakestra is healthy",
		Long: `Answers "is Oakestra healthy", not just "which containers are up".

Starts with the local setup - the active scope and where it came from, which
components are running from your working tree - then, if the real ` + "`oak`" + ` CLI is
on PATH, the cluster and instance state it reports. It finishes with a
container table, so container-level health is visible even without ` + "`oak`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := cfgFrom(cmd)

			printSetup(cfg)

			if _, err := exec.LookPath("oak"); err != nil {
				fmt.Println("\noak-dev: `oak` CLI not on PATH - showing container status only.")
				fmt.Println("         Install oakestra-cli and run `oak-dev doctor --fix` for cluster/instance status.")
			} else {
				fmt.Println("\n== clusters ==")
				runInherit("oak", "cluster", "list", "--all")
				fmt.Println("\n== service instances ==")
				runInherit("oak", "service", "show")
			}

			fmt.Println("\n== containers ==")
			out, err := exec.Command("docker", "ps",
				"--format", "table {{.Names}}\t{{.Status}}\t{{.Ports}}").Output()
			if err != nil {
				return err
			}
			printFilteredContainers(string(out))

			fmt.Println("\n== endpoints ==")
			fmt.Println("  root API      http://localhost:10000")
			fmt.Println("  Swagger UI    http://localhost:10000/api/docs")
			fmt.Println("  cluster API   http://localhost:10100")
			return nil
		},
	}
	return cmd
}

// printSetup shows the scope and live set. Both used to be invisible: the
// scope is sticky across commands, and nothing reported which components were
// running from mounted source rather than their baked image.
func printSetup(cfg *config.Config) {
	fmt.Println("== setup ==")
	fmt.Printf("  scope         %s (%s)\n", cfg.Stack, cfg.StackSource)
	fmt.Printf("  oakestra      %s\n", cfg.OakestraRepo)
	if cfg.LibsRepo != "" {
		fmt.Printf("  libs          %s\n", cfg.LibsRepo)
	}

	live := cfg.LiveNames()
	if len(live) == 0 {
		fmt.Println("  live          nothing - every service runs its baked image")
		fmt.Println("                (`oak-dev reload <component>` mounts one from your working tree)")
		return
	}
	for i, name := range live {
		label := "  live         "
		if i > 0 {
			label = "               "
		}
		c, err := components.Resolve(name)
		if err != nil {
			fmt.Printf("%s %s\n", label, name)
			continue
		}
		how := "gunicorn --reload"
		if c.Kind == components.KindGo {
			how = "cross-compiled binary"
		}
		fmt.Printf("%s %-28s %s\n", label, name, how)
	}
}

func runInherit(name string, args ...string) {
	c := exec.Command(name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	_ = c.Run() // best-effort: an unconfigured `oak` shouldn't kill `status`
}

func printFilteredContainers(psOutput string) {
	keep := []string{"NAMES", "system_manager", "cluster_manager", "root_", "cluster_",
		"mongo", "redis", "mqtt", "scheduler", "abstractor", "jwt", "worker"}
	for line := range strings.SplitSeq(psOutput, "\n") {
		for _, k := range keep {
			if strings.Contains(line, k) {
				fmt.Println(line)
				break
			}
		}
	}
}
