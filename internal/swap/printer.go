package swap

import (
	"fmt"
	"os"
	"sync"
)

const (
	reset     = "\033[0m"
	bold      = "\033[1m"
	dim       = "\033[2m"
	red       = "\033[31m"
	yellow    = "\033[33m"
	accentClr = "\033[38;5;173m" // warm salmon
	mutedClr  = "\033[38;5;250m" // soft gray
)

var (
	colorOnce    sync.Once
	colorEnabled bool
)

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func hasColor() bool {
	colorOnce.Do(func() { colorEnabled = detectColor() })
	return colorEnabled
}

func style(text string, codes ...string) string {
	if !hasColor() {
		return text
	}
	s := ""
	for _, c := range codes {
		s += c
	}
	return s + text + reset
}

func Accent(t string) string     { return style(t, accentClr) }
func Muted(t string) string      { return style(t, mutedClr) }
func Dimmed(t string) string     { return style(t, dim) }
func Bolded(t string) string     { return style(t, bold) }
func BoldAccent(t string) string { return style(t, bold, accentClr) }

func PrintError(msg string)   { fmt.Fprintln(os.Stderr, style(msg, red)) }
func PrintWarning(msg string) { fmt.Println(style(msg, yellow)) }
