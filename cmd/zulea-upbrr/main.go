// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Command zulea-upbrr is the Zulea uploader built on upbrr. Stage 1 is
// dry-run only: it verifies the freeze -> TM -> manifest chain and shows the
// exact PTT payload upbrr would send. It has no upload command.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/autobrr/upbrr/internal/zulea/app"
	"github.com/autobrr/upbrr/internal/zulea/names"
	"github.com/autobrr/upbrr/internal/zulea/ptttax"
	"github.com/autobrr/upbrr/internal/zulea/seed"
	"github.com/autobrr/upbrr/internal/zulea/tm"
)

const version = "0.3.0"

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
  zulea-upbrr check --config FILE [--release REL ... | --list FILE]   (cały łańcuch, bez sieci; domyślnie wszystkie OK)
  zulea-upbrr verify-identity --config FILE --release REL           (diagnostyka W-3: ino/size/mtime, dysk)
  zulea-upbrr receipts --config FILE                                (pokwitowania wysyłek)
  zulea-upbrr scan-ptt --config FILE [--release REL ...] [--delay 3s] (co już jest na PTT; tylko odczyt)
  zulea-upbrr queue --config FILE [--limit N]                       (kolejne wolne wydania wg ostatniego skanu)
  zulea-upbrr verify-uploaded --config FILE --release REL [--ptt-id ID] (sprawdzenie dawnego ręcznego uploadu)
  zulea-upbrr seed --config FILE --release REL [--wait 30m] [--start] (qBittorrent: zatrzymany, pełny recheck)
  zulea-upbrr seed --config FILE --release REL --start-verified       (uruchom seed tylko przy 100%)
  zulea-upbrr sync-inputs --dest DIR --freeze F --freeze-sha SHA [--runs DIR] [--titles F] [--owner 99:100]
  zulea-upbrr ptt-taxonomy --options FILE [--apply CONFIG]            (ID z formularza PTT -> konfiguracja)
  zulea-upbrr check-config --config FILE
  zulea-upbrr names --titles FILE --tmdb-id ID REL
  zulea-upbrr compare-torrent ORYGINAL_TM.torrent POBRANY.torrent
  zulea-upbrr upload --config FILE --release REL --fingerprint FP   (canary: jedno wydanie, terminal, wpisanie potwierdzenia)
  zulea-upbrr version

Kody wyjścia: 0 wszystko READY, 3 co najmniej jeden STOP, 2 błąd użycia/konfiguracji, 1 błąd wykonania.
Upload działa tylko interaktywnie w terminalu: świeży dry-run, zgodny fingerprint, otwarta bramka canary
i ręcznie wpisana fraza potwierdzenia. Nie ma trybu bez pytania. Nic nie trafia do klienta torrent.
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
	case "upload":
		return runUpload(ctx, args[1:], stdout, stderr, os.Stdin)
	case "compare-torrent":
		return runCompare(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "verify-identity":
		return runVerifyIdentity(args[1:], stdout, stderr)
	case "receipts":
		return runReceipts(args[1:], stdout, stderr)
	case "scan-ptt":
		return runScan(ctx, args[1:], stdout, stderr)
	case "queue":
		return runQueue(args[1:], stdout, stderr)
	case "verify-uploaded":
		return runVerifyUploaded(ctx, args[1:], stdout, stderr)
	case "seed":
		return runSeed(ctx, args[1:], stdout, stderr)
	case "sync-inputs":
		return runSync(args[1:], stdout, stderr)
	case "ptt-taxonomy":
		return runTaxonomy(args[1:], stdout, stderr)
	case "submit", "send":
		fmt.Fprintln(stderr, "zulea-upbrr: nie ma tej komendy. Wysyłka: zulea-upbrr upload (tylko interaktywnie, po dry-run i potwierdzeniu fingerprintu).")
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
	personal := "nierozstrzygnięte"
	if cfg.PTT.PersonalRelease != nil {
		personal = strconv.FormatBool(*cfg.PTT.PersonalRelease)
	}
	fmt.Fprintf(stdout, "konfiguracja poprawna\n  trackery: %s\n  dysk (W-2): %s, tryb %s\n  MediaInfo (D-6): %s\n"+
		"  ID PTT potwierdzone (W-1): %t\n  personal_release (D-5): %s\n  mod_queue (D-5): %t\n",
		strings.Join(cfg.Trackers, ","), cfg.PhysicalDisk, cfg.DiskCheckMode, cfg.MediaInfo.Source,
		cfg.PTT.Taxonomy != nil, personal, cfg.PTT.ModQueue)
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

// isTerminal reports whether f is an interactive terminal.
var isTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func runUpload(ctx context.Context, args []string, stdout, stderr io.Writer, stdin *os.File) int {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "plik konfiguracji JSON (0600)")
	release := fs.String("release", "", "jedno wydanie (canary)")
	fingerprint := fs.String("fingerprint", "", "pełny fingerprint przejrzanego dry-run (64 znaki)")
	if err := fs.Parse(args); err != nil || *configPath == "" || *release == "" || *fingerprint == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "zulea-upbrr upload: wymagane --config, --release i --fingerprint")
		return exitUsage
	}
	if !isTerminal(stdin) {
		fmt.Fprintln(stderr, "zulea-upbrr upload: wymagany interaktywny terminal — potwierdzenia nie można podać przez potok ani plik")
		return exitUsage
	}
	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr upload: %v\n", err)
		return exitUsage
	}
	logger := app.NewLogger(stderr, false)
	runner := &app.Runner{Config: cfg, Logger: logger}
	reader := bufio.NewReader(stdin)
	result, err := runner.Upload(ctx, app.UploadRequest{
		Release:     *release,
		Fingerprint: *fingerprint,
		Preview:     stdout,
		Confirm: func(prompt string) (string, error) {
			fmt.Fprint(stdout, prompt)
			line, rerr := reader.ReadString('\n')
			if rerr != nil && !errors.Is(rerr, io.EOF) {
				return "", fmt.Errorf("read confirmation: %w", rerr)
			}
			return line, nil
		},
	})
	if result.Comparison != nil {
		fmt.Fprintln(stdout, logger.MaskKnown(result.Comparison.Report()))
	}
	if result.ReceiptPath != "" {
		fmt.Fprintf(stdout, "pokwitowanie: %s (stan: %s)\n", result.ReceiptPath, result.Receipt.State)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr upload: %s\n", logger.Sanitize(err.Error()))
		if errors.Is(err, app.ErrUploadRefused) {
			return exitUsage
		}
		return exitRuntime
	}
	fmt.Fprintf(stdout, "WYSŁANO: %s (PTT id %s, %s)\n", *release, result.Receipt.TorrentID, result.Receipt.TorrentURL)
	if result.Comparison == nil || !result.Comparison.OK() {
		fmt.Fprintln(stdout, "UWAGA: brak potwierdzenia DANE_ZGODNE=TAK — nie dodawaj torrenta do klienta przed ręczną kontrolą")
		return exitStop
	}
	fmt.Fprintf(stdout, "Następny krok (ręczny, jak dziś): dodaj %s do qBittorrenta z pełnym rechekiem; seed po 100%%.\n", result.Receipt.RegisteredTorrent)
	return exitOK
}

func runCompare(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "zulea-upbrr compare-torrent: wymagane dwa pliki: ORYGINAL_TM.torrent POBRANY.torrent")
		return exitUsage
	}
	original, err := tm.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr compare-torrent: %v\n", err)
		return exitUsage
	}
	downloaded, err := tm.ReadFile(args[1])
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr compare-torrent: %v\n", err)
		return exitUsage
	}
	comparison, err := tm.Compare(original, downloaded)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr compare-torrent: %v\n", err)
		return exitRuntime
	}
	fmt.Fprint(stdout, comparison.Report())
	if !comparison.OK() {
		return exitRuntime
	}
	return exitOK
}

// loadConfigFlag parses --config plus extra flags registered by register and loads the config.
func loadConfigFlag(name string, args []string, stderr io.Writer, register func(*flag.FlagSet)) (app.Config, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "plik konfiguracji JSON (0600)")
	if register != nil {
		register(fs)
	}
	if err := fs.Parse(args); err != nil || *configPath == "" || fs.NArg() != 0 {
		fmt.Fprintf(stderr, "zulea-upbrr %s: wymagane --config\n", name)
		return app.Config{}, false
	}
	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr %s: %v\n", name, err)
		return app.Config{}, false
	}
	return cfg, true
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	var releases multiFlag
	var listPath string
	cfg, ok := loadConfigFlag("check", args, stderr, func(fs *flag.FlagSet) {
		fs.Var(&releases, "release", "nazwa wydania (można powtarzać)")
		fs.StringVar(&listPath, "list", "", "plik z listą wydań")
	})
	if !ok {
		return exitUsage
	}
	if listPath != "" {
		listed, err := readList(listPath)
		if err != nil {
			fmt.Fprintf(stderr, "zulea-upbrr check: %v\n", err)
			return exitUsage
		}
		releases = append(releases, listed...)
	}
	summary, err := (&app.Runner{Config: cfg}).Check(releases)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr check: %v\n", err)
		return exitRuntime
	}
	app.RenderCheck(stdout, summary)
	if summary.Stop > 0 {
		return exitStop
	}
	return exitOK
}

func runVerifyIdentity(args []string, stdout, stderr io.Writer) int {
	var release string
	cfg, ok := loadConfigFlag("verify-identity", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&release, "release", "", "nazwa wydania")
	})
	if !ok || release == "" {
		if ok {
			fmt.Fprintln(stderr, "zulea-upbrr verify-identity: wymagane --release")
		}
		return exitUsage
	}
	rep, err := (&app.Runner{Config: cfg}).VerifyIdentity(release)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr verify-identity: %v\n", err)
		return exitRuntime
	}
	app.RenderIdentity(stdout, rep)
	if !rep.IdentityOK {
		return exitStop
	}
	return exitOK
}

func runReceipts(args []string, stdout, stderr io.Writer) int {
	cfg, ok := loadConfigFlag("receipts", args, stderr, nil)
	if !ok {
		return exitUsage
	}
	list, err := (&app.Runner{Config: cfg}).Receipts()
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr receipts: %v\n", err)
		return exitRuntime
	}
	app.RenderReceipts(stdout, list)
	return exitOK
}

func runScan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var releases multiFlag
	var listPath string
	delay := 3 * time.Second
	cfg, ok := loadConfigFlag("scan-ptt", args, stderr, func(fs *flag.FlagSet) {
		fs.Var(&releases, "release", "nazwa wydania (można powtarzać)")
		fs.StringVar(&listPath, "list", "", "plik z listą wydań")
		fs.DurationVar(&delay, "delay", delay, "odstęp między wydaniami (grzeczność wobec trackera)")
	})
	if !ok {
		return exitUsage
	}
	if listPath != "" {
		listed, err := readList(listPath)
		if err != nil {
			fmt.Fprintf(stderr, "zulea-upbrr scan-ptt: %v\n", err)
			return exitUsage
		}
		releases = append(releases, listed...)
	}
	logger := app.NewLogger(stderr, false)
	summary, err := (&app.Runner{Config: cfg, Logger: logger}).ScanPTT(ctx, releases, delay)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr scan-ptt: %s\n", logger.Sanitize(err.Error()))
		if len(summary.Results) == 0 {
			return exitRuntime
		}
	}
	app.RenderScan(stdout, summary)
	return exitOK
}

func runQueue(args []string, stdout, stderr io.Writer) int {
	limit := 20
	cfg, ok := loadConfigFlag("queue", args, stderr, func(fs *flag.FlagSet) {
		fs.IntVar(&limit, "limit", limit, "ile pozycji pokazać (0 = wszystkie)")
	})
	if !ok {
		return exitUsage
	}
	entries, scanPath, err := (&app.Runner{Config: cfg}).Queue()
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr queue: %v\n", err)
		return exitRuntime
	}
	app.RenderQueue(stdout, entries, scanPath, limit)
	return exitOK
}

func runVerifyUploaded(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var release, id string
	cfg, ok := loadConfigFlag("verify-uploaded", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&release, "release", "", "nazwa wydania")
		fs.StringVar(&id, "ptt-id", "", "ID torrenta na PTT (opcjonalnie)")
	})
	if !ok || release == "" {
		if ok {
			fmt.Fprintln(stderr, "zulea-upbrr verify-uploaded: wymagane --release")
		}
		return exitUsage
	}
	logger := app.NewLogger(stderr, false)
	res, err := (&app.Runner{Config: cfg, Logger: logger}).VerifyUploaded(ctx, release, id)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr verify-uploaded: %s\n", logger.Sanitize(err.Error()))
		return exitRuntime
	}
	fmt.Fprint(stdout, logger.MaskKnown(res.Comparison.Report()))
	fmt.Fprintf(stdout, "PTT id %s, pobrany torrent: %s\n", res.PTTID, res.Path)
	if !res.Comparison.OK() {
		return exitStop
	}
	return exitOK
}

func runSeed(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var release string
	var wait time.Duration
	var start, startVerified bool
	cfg, ok := loadConfigFlag("seed", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&release, "release", "", "nazwa wydania z pokwitowaniem accepted")
		fs.DurationVar(&wait, "wait", 0, "ile czekać na koniec rechecku (np. 30m)")
		fs.BoolVar(&start, "start", false, "uruchom seed po rechecku, tylko przy 100%")
		fs.BoolVar(&startVerified, "start-verified", false, "uruchom istniejący torrent, tylko przy 100%")
	})
	if !ok || release == "" {
		if ok {
			fmt.Fprintln(stderr, "zulea-upbrr seed: wymagane --release")
		}
		return exitUsage
	}
	runner := &app.Runner{Config: cfg}
	var res seed.Result
	var err error
	if startVerified {
		res, err = runner.StartSeedVerified(ctx, release)
	} else {
		res, err = runner.Seed(ctx, app.SeedRequest{
			Release: release,
			Wait:    wait,
			Start:   start,
		}, nil)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr seed: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "infohash %s; dodano=%t; był=%t; stan=%s; postęp=%.2f%%; seed=%t\n%s\n",
		res.Hash, res.Added, res.AlreadyExists, res.State, res.Progress*100, res.Started, res.Message)
	return exitOK
}

func runSync(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sync-inputs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var req app.SyncRequest
	var owner string
	fs.StringVar(&req.Dest, "dest", "", "katalog docelowy kopii (appdata uploadera)")
	fs.StringVar(&req.FreezeSrc, "freeze", "", "aktywny plik zamrożenia")
	fs.StringVar(&req.FreezeSHA256, "freeze-sha", "", "oczekiwane SHA-256 zamrożenia")
	fs.StringVar(&req.RunsSrc, "runs", "", "katalog runs/ wrappera TM")
	fs.StringVar(&req.TitlesSrc, "titles", "", "tmdb-titles.tsv")
	fs.StringVar(&owner, "owner", "", "uid:gid dla kopii, np. 99:100 (wymaga roota)")
	if err := fs.Parse(args); err != nil || req.Dest == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "zulea-upbrr sync-inputs: wymagane --dest oraz co najmniej jedno źródło")
		return exitUsage
	}
	if owner != "" {
		var uid, gid int
		if _, err := fmt.Sscanf(owner, "%d:%d", &uid, &gid); err != nil {
			fmt.Fprintln(stderr, "zulea-upbrr sync-inputs: --owner w postaci uid:gid")
			return exitUsage
		}
		req.Owner = &[2]int{uid, gid}
	}
	res, err := app.SyncInputs(req)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr sync-inputs: %v\n", err)
		return exitRuntime
	}
	for _, p := range res.Copied {
		fmt.Fprintf(stdout, "skopiowano    %s\n", p)
	}
	for _, p := range res.Unchanged {
		fmt.Fprintf(stdout, "bez zmian     %s\n", p)
	}
	for _, p := range res.Incomplete {
		fmt.Fprintf(stdout, "pominięto (przebieg TM w toku) %s\n", p)
	}
	return exitOK
}

func runTaxonomy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ptt-taxonomy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	optionsPath := fs.String("options", "", "plik z wklejonymi listami <select>/<option> z formularza PTT")
	apply := fs.String("apply", "", "zapisz tabelę do tego pliku konfiguracji")
	if err := fs.Parse(args); err != nil || *optionsPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "zulea-upbrr ptt-taxonomy: wymagane --options")
		return exitUsage
	}
	data, err := os.ReadFile(*optionsPath)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr ptt-taxonomy: %v\n", err)
		return exitUsage
	}
	lists, err := ptttax.Parse(string(data))
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr ptt-taxonomy: %v\n", err)
		return exitStop
	}
	tax, notes, err := ptttax.Build(lists)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr ptt-taxonomy: %v\n", err)
		return exitStop
	}
	out, err := json.MarshalIndent(tax, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr ptt-taxonomy: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "%s\n", out)
	for _, n := range notes {
		fmt.Fprintf(stdout, "UWAGA: %s\n", n)
	}
	if *apply != "" {
		if err := app.ApplyTaxonomy(*apply, tax); err != nil {
			fmt.Fprintf(stderr, "zulea-upbrr ptt-taxonomy: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "zapisano do %s\n", *apply)
	}
	return exitOK
}
