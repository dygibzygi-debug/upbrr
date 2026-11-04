// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Command zulea-upbrr is the Zulea uploader built on upbrr. Stage 1 is
// dry-run only: it verifies the freeze -> TM -> manifest chain and shows the
// exact PTT payload upbrr would send. It has no upload command.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/autobrr/upbrr/internal/zulea/app"
	"github.com/autobrr/upbrr/internal/zulea/names"
)

const version = "0.1.0-stage1"

// Exit codes.
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
	exitStop    = 3
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}

func usage(w io.Writer) {
	fmt.Fprint(w, `zulea-upbrr `+version+` — uploader Zulei na bazie upbrr (etap 1: tylko dry-run)

Użycie:
  zulea-upbrr plan --config FILE (--release REL ... | --list FILE) [--debug]
  zulea-upbrr check-config --config FILE
  zulea-upbrr names --titles FILE --tmdb-id ID REL
  zulea-upbrr version

Kody wyjścia: 0 wszystko READY, 3 co najmniej jeden STOP, 2 błąd użycia/konfiguracji, 1 błąd wykonania.
Wysyłki na tracker nie ma w tym etapie.
`)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "plan", "dry-run":
		return runPlan(ctx, args[1:], stdout, stderr)
	case "check-config":
		return runCheckConfig(args[1:], stdout, stderr)
	case "names":
		return runNames(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "zulea-upbrr %s (names.py %s)\n", version, names.RuleVersion)
		return exitOK
	case "upload", "submit", "send":
		fmt.Fprintln(stderr, "zulea-upbrr: wysyłka nie istnieje w etapie 1 (tylko dry-run). Upload uruchamia wyłącznie Andryou w etapie 3.")
		return exitUsage
	case "-h", "--help", "help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "zulea-upbrr: nieznana komenda %q\n", args[0])
		usage(stderr)
		return exitUsage
	}
}

func runPlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "plik konfiguracji JSON (0600)")
	listPath := fs.String("list", "", "plik z listą wydań (jedno na linię)")
	debug := fs.Bool("debug", false, "logi DEBUG/TRACE na stderr")
	var releases multiFlag
	fs.Var(&releases, "release", "nazwa wydania (można powtarzać)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *configPath == "" || (len(releases) == 0 && *listPath == "") || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "zulea-upbrr plan: wymagane --config oraz --release albo --list")
		return exitUsage
	}
	if *listPath != "" {
		listed, err := readList(*listPath)
		if err != nil {
			fmt.Fprintf(stderr, "zulea-upbrr plan: %v\n", err)
			return exitUsage
		}
		releases = append(releases, listed...)
	}
	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr plan: %v\n", err)
		return exitUsage
	}
	logger := app.NewLogger(stderr, *debug)
	runner := &app.Runner{Config: cfg, Logger: logger}
	outcomes, err := runner.DryRun(ctx, releases)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr plan: %s\n", logger.Sanitize(err.Error()))
		if len(outcomes) == 0 {
			return exitRuntime
		}
	}
	app.Render(stdout, outcomes, logger.MaskKnown)
	for _, o := range outcomes {
		if o.Status != app.StatusReady {
			return exitStop
		}
	}
	if err != nil {
		return exitRuntime
	}
	return exitOK
}

func runCheckConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-config", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "plik konfiguracji JSON (0600)")
	if err := fs.Parse(args); err != nil || *configPath == "" {
		fmt.Fprintln(stderr, "zulea-upbrr check-config: wymagane --config")
		return exitUsage
	}
	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr check-config: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "konfiguracja poprawna; trackery: %s; PTT ID potwierdzone: %t\n",
		strings.Join(cfg.Trackers, ","), cfg.PTT.Taxonomy != nil)
	return exitOK
}

func runNames(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("names", flag.ContinueOnError)
	fs.SetOutput(stderr)
	titlesPath := fs.String("titles", "", "plik tmdb-titles.tsv")
	tmdbID := fs.String("tmdb-id", "", "TMDB_ID z zamrożenia")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "zulea-upbrr names: wymagana jedna nazwa wydania")
		return exitUsage
	}
	titles := names.Titles{}
	if *titlesPath != "" {
		loaded, err := names.LoadTitles(*titlesPath)
		if err != nil {
			fmt.Fprintf(stderr, "zulea-upbrr names: %v\n", err)
			return exitUsage
		}
		titles = loaded
	}
	result := names.Build(fs.Arg(0), titles.Lookup(*tmdbID))
	if result.Error != "" {
		fmt.Fprintf(stdout, "BŁĄD: %s\n", result.Error)
		return exitStop
	}
	fmt.Fprintf(stdout, "CM:  %s\nPTT: %s\n", result.CinemaMovies, result.PTT)
	for _, w := range result.Warnings {
		fmt.Fprintf(stdout, "SPRAWDŹ: %s\n", w)
	}
	return exitOK
}

func readList(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("lista: %w", err)
	}
	defer file.Close()
	var out []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("lista: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("lista: brak wydań")
	}
	return out, nil
}
