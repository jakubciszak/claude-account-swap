package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jciszak/claude-swap/internal/swap"
)

func main() {
	addAccount := flag.Bool("add-account", false, "Add current account to managed accounts")
	removeAccount := flag.String("remove-account", "", "Remove account by number or email")
	list := flag.Bool("list", false, "List all managed accounts")
	switchNext := flag.Bool("switch", false, "Rotate to next account in sequence")
	switchTo := flag.String("switch-to", "", "Switch to specific account number or email")
	status := flag.Bool("status", false, "Show current account status")
	purge := flag.Bool("purge", false, "Remove all claude-swap data from the system")
	showVersion := flag.Bool("version", false, "Show version")
	debug := flag.Bool("debug", false, "Enable debug logging")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: cswap [options]\n\nMulti-Account Switcher for Claude Code\n\nOptions:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  cswap --add-account
  cswap --list
  cswap --switch
  cswap --switch-to 2
  cswap --switch-to user@example.com
  cswap --remove-account user@example.com
  cswap --status
  cswap --purge
`)
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("cswap %s\n", swap.Version())
		return
	}

	// Count commands
	cmds := 0
	if *addAccount {
		cmds++
	}
	if *removeAccount != "" {
		cmds++
	}
	if *list {
		cmds++
	}
	if *switchNext {
		cmds++
	}
	if *switchTo != "" {
		cmds++
	}
	if *status {
		cmds++
	}
	if *purge {
		cmds++
	}

	if cmds == 0 {
		flag.Usage()
		os.Exit(1)
	}
	if cmds > 1 {
		swap.PrintError("Error: only one command can be used at a time")
		os.Exit(1)
	}

	sw := swap.NewSwitcher(*debug)

	if sw.IsRoot() {
		swap.PrintError("Error: Do not run this script as root (unless running in a container)")
		os.Exit(1)
	}

	var err error
	switch {
	case *addAccount:
		err = sw.AddAccount()
	case *removeAccount != "":
		err = sw.RemoveAccount(*removeAccount)
	case *list:
		err = sw.ListAccounts()
	case *switchNext:
		err = sw.Switch()
	case *switchTo != "":
		err = sw.SwitchTo(*switchTo)
	case *status:
		err = sw.Status()
	case *purge:
		err = sw.Purge()
	}

	if err != nil {
		swap.PrintError(fmt.Sprintf("Error: %v", err))
		os.Exit(1)
	}
}
