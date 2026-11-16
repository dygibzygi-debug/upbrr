// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Command zulea-upbrr is the Zulea uploader built on upbrr. A real upload
// exists only as the gated canary command. dupe-accept records a human
// decision about one duplicate search; it never uploads and it has no --force.
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
	"github.com/autobrr/upbrr/internal/zulea/dupeaccept"
	"github.com/autobrr/upbrr/internal/zulea/names"
	"github.com/autobrr/upbrr/internal/zulea/ptttax"
	"github.com/autobrr/upbrr/internal/zulea/seed"
	"github.com/autobrr/upbrr/internal/zulea/tm"
)

const version = app.Version

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
  zulea-upbrr scan-ptt --config FILE [--release REL ...] [--delay 3s] (co już jest na PTT; tylko odczyt; odstęp co najmniej 3s)
  zulea-upbrr nightly-scan-enabled --config FILE                     (czy nocny scan-ptt jest włączony; domyślnie nie)
  zulea-upbrr queue --config FILE [--limit N]                       (kolejne wolne wydania wg ostatniego skanu)
  zulea-upbrr verify-uploaded --config FILE --release REL [--ptt-id ID] (sprawdzenie dawnego ręcznego uploadu)
  zulea-upbrr seed --config FILE --release REL [--wait 30m] [--start] (qBittorrent: zatrzymany, autoTMM=false, recheck, start tylko przy 100%)
  zulea-upbrr seed --config FILE --release REL --start-verified       (uruchom seed tylko przy 100%)
  zulea-upbrr sync-inputs --dest DIR --freeze F --freeze-sha SHA [--runs DIR] [--titles F] [--owner 99:100]
  zulea-upbrr ptt-taxonomy --options FILE [--apply CONFIG]            (ID z formularza PTT -> konfiguracja)
  zulea-upbrr ptt-status --config FILE [--release REL ...]            (moderacja wysłanych; tylko odczyt)
  zulea-upbrr seed-status --config FILE                               (stan seedu w qBittorrencie; tylko odczyt)
  zulea-upbrr history --config FILE [--out FILE.csv]                  (historia wysyłek CSV)
  zulea-upbrr summary --config FILE [--with-seed] [--notify]          (krótkie podsumowanie; opcjonalnie Discord/Telegram)
  zulea-upbrr resolve --config FILE --release REL --state not_sent|accepted|rejected [--ptt-id ID] --note TEKST
                                                                      (decyzja po ręcznym sprawdzeniu PTT; terminal, fraza)
  zulea-upbrr dupe-accept --config FILE --release REL --reason TEKST [--revoke]
                                                                      (akceptacja duplikatu z ostatniego raportu; terminal, fraza)
  zulea-upbrr check-config --config FILE
  zulea-upbrr names --titles FILE --tmdb-id ID REL
  zulea-upbrr compare-torrent ORYGINAL_TM.torrent POBRANY.torrent
  zulea-upbrr upload --config FILE --release REL --fingerprint FP   (canary: jedno wydanie, terminal, wpisanie potwierdzenia)
  zulea-upbrr version

Kody wyjścia: 0 wszystko READY, 3 co najmniej jeden STOP, 2 błąd użycia/konfiguracji, 1 błąd wykonania.
Upload, dupe-accept, resolve i seed --start-verified wymagają terminala albo --confirmation-file.
--json daje stabilny dokument schema_version bez sekretów. Nie ma --force. Nic nie omija bramek.
dupe-accept czyta ostatni raport scan-ptt albo plan, nie łączy się z trackerem.
Bramkę otwiera tylko zgodny zestaw kandydatów (DUPLICATE albo REVIEW).
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
		return runCheckConfig(ctx, args[1:], stdout, stderr)
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
	case "nightly-scan-enabled":
		return runNightlyScanEnabled(args[1:], stdout, stderr)
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
	case "ptt-status":
		return runPTTStatus(ctx, args[1:], stdout, stderr)
	case "seed-status":
		return runSeedStatus(ctx, args[1:], stdout, stderr)
	case "history":
		return runHistory(args[1:], stdout, stderr)
	case "summary":
		return runSummary(ctx, args[1:], stdout, stderr)
	case "resolve":
		return runResolve(args[1:], stdout, stderr, os.Stdin)
	case "dupe-accept":
		return runDupeAccept(args[1:], stdout, stderr, os.Stdin)
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
	asJSON := fs.Bool("json", false, "stabilny JSON (schema_version) zamiast tekstu")
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
	code := exitOK
	if err != nil {
		code = exitRuntime
		if len(outcomes) > 0 {
			code = exitStop
		}
	}
	for _, o := range outcomes {
		if o.Status != app.StatusReady {
			code = exitStop
		}
	}
	if *asJSON {
		return writeCommandJSON(stdout, stderr, "plan", code, outcomes, err, logger)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr plan: %s\n", logger.Sanitize(err.Error()))
		if len(outcomes) == 0 {
			return exitRuntime
		}
	}
	app.Render(stdout, outcomes, logger.MaskKnown)
	return code
}

func runCheckConfig(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	scan := "wyłączony"
	if cfg.NightlyScanEnabled() {
		scan = "włączony"
	}
	fmt.Fprintf(stdout, "konfiguracja poprawna\n  trackery: %s\n  dysk (W-2): %s, tryb %s\n  MediaInfo (D-6): %s\n"+
		"  ID PTT potwierdzone (W-1): %t\n  personal_release (D-5): %s\n  mod_queue (D-5): %t\n  nocny scan-ptt: %s\n",
		strings.Join(cfg.Trackers, ","), cfg.PhysicalDisk, cfg.DiskCheckMode, cfg.MediaInfo.Source,
		cfg.PTT.Taxonomy != nil, personal, cfg.PTT.ModQueue, scan)
	if cfg.QBittorrent == nil {
		fmt.Fprintln(stdout, "  qBittorrent: nie skonfigurowano")
		return exitOK
	}
	logger := app.NewLogger(stderr, false)
	probe, err := (&app.Runner{Config: cfg, Logger: logger}).CheckQBit(ctx, nil)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr check-config: %s\n", logger.Sanitize(err.Error()))
		return exitRuntime
	}
	fmt.Fprintf(stdout, "  qBittorrent: WebUI %s, save_path istnieje\n", probe.Version)
	for _, warning := range probe.Warnings {
		fmt.Fprintf(stdout, "OSTRZEŻENIE: %s\n", warning)
	}
	return exitOK
}

func runNightlyScanEnabled(args []string, stdout, stderr io.Writer) int {
	cfg, ok := loadConfigFlag("nightly-scan-enabled", args, stderr, nil)
	if !ok {
		return exitUsage
	}
	if cfg.NightlyScanEnabled() {
		fmt.Fprintln(stdout, "scan_ptt=true")
		return exitOK
	}
	fmt.Fprintln(stdout, "scan_ptt=false")
	return exitStop
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
	confirmationPath := fs.String("confirmation-file", "", "plik potwierdzenia panelu zamiast terminala")
	asJSON := fs.Bool("json", false, "stabilny JSON (schema_version) zamiast tekstu")
	if err := fs.Parse(args); err != nil || *configPath == "" || *release == "" || *fingerprint == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "zulea-upbrr upload: wymagane --config, --release i --fingerprint")
		return exitUsage
	}
	if *confirmationPath == "" && !isTerminal(stdin) {
		fmt.Fprintln(stderr, "zulea-upbrr upload: wymagany interaktywny terminal albo --confirmation-file")
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
	preview := commandPreview(*asJSON, stdout)
	result, err := runner.Upload(ctx, app.UploadRequest{
		Release:          *release,
		Fingerprint:      *fingerprint,
		Preview:          preview,
		ConfirmationPath: *confirmationPath,
		Confirm: func(prompt string) (string, error) {
			fmt.Fprint(stdout, prompt)
			line, rerr := reader.ReadString('\n')
			if rerr != nil && !errors.Is(rerr, io.EOF) {
				return "", fmt.Errorf("read confirmation: %w", rerr)
			}
			return line, nil
		},
	})
	if *asJSON {
		rc := result.Receipt
		rc.RegisteredTorrent = ""
		view := struct {
			Receipt         app.Receipt `json:"receipt"`
			DataMatch       bool        `json:"data_match"`
			NamesMatch      bool        `json:"names_match"`
			InfoHashChanged bool        `json:"info_hash_changed"`
		}{Receipt: rc}
		if result.Comparison != nil {
			view.DataMatch = result.Comparison.DataMatch
			view.NamesMatch = result.Comparison.NamesMatch
			view.InfoHashChanged = result.Comparison.InfoHashChanged
		}
		code := exitOK
		if err != nil {
			code = exitRuntime
			if errors.Is(err, app.ErrUploadRefused) {
				code = exitUsage
			}
		} else if result.Comparison == nil || !result.Comparison.OK() {
			code = exitStop
		}
		return writeCommandJSON(stdout, stderr, "upload", code, view, err, logger)
	}
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
	fmt.Fprintf(stdout, "Następny krok: zulea-upbrr seed --release %s (torrent z PTT, zatrzymany, autoTMM=false, recheck; start tylko przy 100%%).\n", *release)
	return exitOK
}

func runResolve(args []string, stdout, stderr io.Writer, stdin *os.File) int {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "plik konfiguracji JSON (0600)")
	release := fs.String("release", "", "nazwa wydania")
	state := fs.String("state", "", "wynik sprawdzony na PTT: not_sent, accepted albo rejected")
	pttID := fs.String("ptt-id", "", "ID torrenta na PTT (tylko z --state accepted)")
	note := fs.String("note", "", "jak sprawdzono wynik (zapisywane w pokwitowaniu)")
	confirmationPath := fs.String("confirmation-file", "", "plik potwierdzenia panelu zamiast terminala")
	asJSON := fs.Bool("json", false, "stabilny JSON (schema_version) zamiast tekstu")
	if err := fs.Parse(args); err != nil || *configPath == "" || *release == "" || *state == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "zulea-upbrr resolve: wymagane --config, --release, --state i --note")
		return exitUsage
	}
	if *confirmationPath == "" && !isTerminal(stdin) {
		fmt.Fprintln(stderr, "zulea-upbrr resolve: wymagany interaktywny terminal albo --confirmation-file")
		return exitUsage
	}
	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr resolve: %v\n", err)
		return exitUsage
	}
	logger := app.NewLogger(stderr, false)
	reader := bufio.NewReader(stdin)
	preview := commandPreview(*asJSON, stdout)
	receipt, err := (&app.Runner{Config: cfg, Logger: logger}).Resolve(app.ResolveRequest{
		Release:          *release,
		State:            *state,
		PTTID:            *pttID,
		Note:             *note,
		Preview:          preview,
		ConfirmationPath: *confirmationPath,
		Confirm: func(prompt string) (string, error) {
			fmt.Fprint(stdout, prompt)
			line, rerr := reader.ReadString('\n')
			if rerr != nil && !errors.Is(rerr, io.EOF) {
				return "", fmt.Errorf("read confirmation: %w", rerr)
			}
			return line, nil
		},
	})
	if *asJSON {
		receipt.RegisteredTorrent = ""
		code := exitOK
		if err != nil {
			code = exitRuntime
			if errors.Is(err, app.ErrUploadRefused) {
				code = exitUsage
			}
		}
		return writeCommandJSON(stdout, stderr, "resolve", code, receipt, err, logger)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr resolve: %v\n", err)
		if errors.Is(err, app.ErrUploadRefused) {
			return exitUsage
		}
		return exitRuntime
	}
	fmt.Fprintf(stdout, "ROZSTRZYGNIĘTO: %s -> %s\n", *release, receipt.State)
	if receipt.State == app.ReceiptAccepted {
		fmt.Fprintln(stdout, "Dalej: ptt-status (kolejka czeka na APPROVED); verify-uploaded porówna torrent z PTT z TM.")
	}
	return exitOK
}

func runDupeAccept(args []string, stdout, stderr io.Writer, stdin *os.File) int {
	fs := flag.NewFlagSet("dupe-accept", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "plik konfiguracji JSON (0600)")
	release := fs.String("release", "", "jedno wydanie")
	reason := fs.String("reason", "", "dlaczego ten zestaw kandydatów może wejść (albo dlaczego wycofujesz)")
	revoke := fs.Bool("revoke", false, "wycofaj zapisaną akceptację")
	confirmationPath := fs.String("confirmation-file", "", "plik potwierdzenia panelu zamiast terminala")
	asJSON := fs.Bool("json", false, "stabilny JSON (schema_version) zamiast tekstu")
	if err := fs.Parse(args); err != nil || *configPath == "" || *release == "" || strings.TrimSpace(*reason) == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "zulea-upbrr dupe-accept: wymagane --config, --release i --reason")
		return exitUsage
	}
	if *confirmationPath == "" && !isTerminal(stdin) {
		fmt.Fprintln(stderr, "zulea-upbrr dupe-accept: wymagany interaktywny terminal albo --confirmation-file")
		return exitUsage
	}
	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr dupe-accept: %v\n", err)
		return exitUsage
	}
	logger := app.NewLogger(stderr, false)
	reader := bufio.NewReader(stdin)
	preview := commandPreview(*asJSON, stdout)
	record, err := (&app.Runner{Config: cfg, Logger: logger}).DupeAccept(app.DupeAcceptRequest{
		Release:          *release,
		Reason:           *reason,
		Revoke:           *revoke,
		Preview:          preview,
		ConfirmationPath: *confirmationPath,
		Confirm: func(prompt string) (string, error) {
			fmt.Fprint(stdout, prompt)
			line, rerr := reader.ReadString('\n')
			if rerr != nil && !errors.Is(rerr, io.EOF) {
				return "", fmt.Errorf("read confirmation: %w", rerr)
			}
			return line, nil
		},
	})
	if *asJSON {
		code := exitOK
		if err != nil {
			code = exitRuntime
			if errors.Is(err, app.ErrUploadRefused) {
				code = exitUsage
			}
		}
		return writeCommandJSON(stdout, stderr, "dupe-accept", code, record, err, logger)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr dupe-accept: %s\n", logger.Sanitize(err.Error()))
		if errors.Is(err, app.ErrUploadRefused) {
			return exitUsage
		}
		return exitRuntime
	}
	verb := "ZAPISANO"
	if *revoke {
		verb = "WYCOFANO"
	}
	fmt.Fprintf(stdout, "%s: %s (decyzja %s, sha %s)\n", verb, dupeaccept.Path(cfg.WorkDir, *release), record.Action, record.CandidateSHA)
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
	var asJSON bool
	cfg, ok := loadConfigFlag("check", args, stderr, func(fs *flag.FlagSet) {
		fs.Var(&releases, "release", "nazwa wydania (można powtarzać)")
		fs.StringVar(&listPath, "list", "", "plik z listą wydań")
		fs.BoolVar(&asJSON, "json", false, "stabilny JSON (schema_version) zamiast tekstu")
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
	code := exitOK
	if err != nil {
		code = exitRuntime
	} else if summary.Stop > 0 {
		code = exitStop
	}
	if asJSON {
		return writeCommandJSON(stdout, stderr, "check", code, summary, err, nil)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr check: %v\n", err)
		return exitRuntime
	}
	app.RenderCheck(stdout, summary)
	return code
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
	var asJSON bool
	cfg, ok := loadConfigFlag("receipts", args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "stabilny JSON (schema_version) zamiast tekstu")
	})
	if !ok {
		return exitUsage
	}
	list, err := (&app.Runner{Config: cfg}).Receipts()
	if asJSON {
		code := exitOK
		if err != nil {
			code = exitRuntime
		}
		return writeCommandJSON(stdout, stderr, "receipts", code, list, err, nil)
	}
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
	fs := flag.NewFlagSet("scan-ptt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "plik konfiguracji JSON (0600)")
	fs.Var(&releases, "release", "nazwa wydania (można powtarzać)")
	fs.StringVar(&listPath, "list", "", "plik z listą wydań")
	asJSON := fs.Bool("json", false, "stabilny JSON (schema_version) zamiast tekstu")
	fs.DurationVar(&delay, "delay", delay, "odstęp między wydaniami (co najmniej 3s)")
	if err := fs.Parse(args); err != nil || *configPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "zulea-upbrr scan-ptt: wymagane --config")
		return exitUsage
	}
	if delay < 3*time.Second {
		fmt.Fprintln(stderr, "zulea-upbrr scan-ptt: --delay musi wynosić co najmniej 3s")
		return exitUsage
	}
	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr scan-ptt: %v\n", err)
		return exitUsage
	}
	if listPath != "" {
		listed, lerr := readList(listPath)
		if lerr != nil {
			fmt.Fprintf(stderr, "zulea-upbrr scan-ptt: %v\n", lerr)
			return exitUsage
		}
		releases = append(releases, listed...)
	}
	logger := app.NewLogger(stderr, false)
	summary, err := (&app.Runner{Config: cfg, Logger: logger}).ScanPTT(ctx, releases, delay)
	code := exitOK
	if err != nil {
		code = exitRuntime
	}
	if *asJSON {
		return writeCommandJSON(stdout, stderr, "scan-ptt", code, summary, err, logger)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr scan-ptt: %s\n", logger.Sanitize(err.Error()))
		if len(summary.Results) > 0 {
			app.RenderScan(stdout, summary)
		}
		return exitRuntime
	}
	app.RenderScan(stdout, summary)
	return exitOK
}

func runQueue(args []string, stdout, stderr io.Writer) int {
	limit := 20
	var asJSON bool
	cfg, ok := loadConfigFlag("queue", args, stderr, func(fs *flag.FlagSet) {
		fs.IntVar(&limit, "limit", limit, "ile pozycji pokazać (0 = wszystkie)")
		fs.BoolVar(&asJSON, "json", false, "stabilny JSON (schema_version) zamiast tekstu")
	})
	if !ok {
		return exitUsage
	}
	entries, scanPath, err := (&app.Runner{Config: cfg}).Queue()
	if asJSON {
		view := app.QueueView{Entries: entries, Scan: scanPath}
		code := exitOK
		if errors.Is(err, app.ErrQueueBlocked) {
			view.Blocked = err.Error()
			code = exitStop
			err = nil
		} else if err != nil {
			code = exitRuntime
		}
		return writeCommandJSON(stdout, stderr, "queue", code, view, err, nil)
	}
	if errors.Is(err, app.ErrQueueBlocked) {
		fmt.Fprintf(stderr, "zulea-upbrr queue: kolejka wstrzymana: %v\n", err)
		return exitStop
	}
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
	var release, confirmationPath string
	var wait time.Duration
	var start, startVerified, asJSON bool
	cfg, ok := loadConfigFlag("seed", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&release, "release", "", "nazwa wydania z pokwitowaniem accepted")
		fs.DurationVar(&wait, "wait", 0, "ile czekać na koniec rechecku (np. 30m)")
		fs.BoolVar(&start, "start", false, "uruchom seed po rechecku, tylko przy 100%")
		fs.BoolVar(&startVerified, "start-verified", false, "uruchom istniejący torrent, tylko przy 100%")
		fs.StringVar(&confirmationPath, "confirmation-file", "", "plik potwierdzenia panelu (tylko z --start-verified)")
		fs.BoolVar(&asJSON, "json", false, "stabilny JSON (schema_version) zamiast tekstu")
	})
	if !ok || release == "" {
		if ok {
			fmt.Fprintln(stderr, "zulea-upbrr seed: wymagane --release")
		}
		return exitUsage
	}
	logger := app.NewLogger(stderr, false)
	runner := &app.Runner{Config: cfg, Logger: logger}
	var res seed.Result
	var err error
	if confirmationPath != "" && !startVerified {
		fmt.Fprintln(stderr, "zulea-upbrr seed: --confirmation-file jest tylko dla --start-verified")
		return exitUsage
	}
	if startVerified {
		res, err = runner.StartSeedVerifiedFile(ctx, release, confirmationPath)
	} else {
		res, err = runner.Seed(ctx, app.SeedRequest{
			Release: release,
			Wait:    wait,
			Start:   start,
		}, nil)
	}
	if asJSON {
		code := exitOK
		if err != nil {
			code = exitRuntime
		}
		return writeCommandJSON(stdout, stderr, "seed", code, res, err, logger)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr seed: %s\n", logger.Sanitize(err.Error()))
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

func runPTTStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var releases multiFlag
	var asJSON bool
	cfg, ok := loadConfigFlag("ptt-status", args, stderr, func(fs *flag.FlagSet) {
		fs.Var(&releases, "release", "nazwa wydania (domyślnie wszystkie accepted)")
		fs.BoolVar(&asJSON, "json", false, "stabilny JSON (schema_version) zamiast tekstu")
	})
	if !ok {
		return exitUsage
	}
	logger := app.NewLogger(stderr, false)
	results, err := (&app.Runner{Config: cfg, Logger: logger}).PTTStatus(ctx, releases)
	code := exitOK
	if err != nil {
		code = exitRuntime
	}
	for _, r := range results {
		if r.State != "APPROVED" {
			code = exitStop
		}
	}
	if asJSON {
		return writeCommandJSON(stdout, stderr, "ptt-status", code, results, err, logger)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr ptt-status: %s\n", logger.Sanitize(err.Error()))
		return exitRuntime
	}
	app.RenderStatus(stdout, results)
	return code
}

func runSeedStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var asJSON bool
	cfg, ok := loadConfigFlag("seed-status", args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "stabilny JSON (schema_version) zamiast tekstu")
	})
	if !ok {
		return exitUsage
	}
	logger := app.NewLogger(stderr, false)
	results, err := (&app.Runner{Config: cfg, Logger: logger}).SeedStatus(ctx, nil)
	code := exitOK
	if err != nil {
		code = exitRuntime
	}
	for _, r := range results {
		if r.Seed != app.SeedSeeding {
			code = exitStop
		}
	}
	if asJSON {
		return writeCommandJSON(stdout, stderr, "seed-status", code, results, err, logger)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr seed-status: %s\n", logger.Sanitize(err.Error()))
		return exitRuntime
	}
	app.RenderSeedStatus(stdout, results)
	return code
}

func runHistory(args []string, stdout, stderr io.Writer) int {
	var out string
	var asJSON bool
	cfg, ok := loadConfigFlag("history", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&out, "out", "", "plik CSV (domyślnie work/history/zulea-upbrr-history.csv)")
		fs.BoolVar(&asJSON, "json", false, "stabilny JSON (schema_version) zamiast tekstu")
	})
	if !ok {
		return exitUsage
	}
	runner := &app.Runner{Config: cfg}
	n, err := runner.WriteHistory(out)
	if asJSON {
		list, lerr := runner.Receipts()
		if err == nil {
			err = lerr
		}
		code := exitOK
		if err != nil {
			code = exitRuntime
		}
		return writeCommandJSON(stdout, stderr, "history", code, app.HistoryView{Count: n, Receipts: list}, err, nil)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr history: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "zapisano %d wpisów historii\n", n)
	return exitOK
}

func runSummary(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var withSeed, send, asJSON bool
	cfg, ok := loadConfigFlag("summary", args, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&withSeed, "with-seed", false, "sprawdź też stan seedu w qBittorrencie")
		fs.BoolVar(&send, "notify", false, "wyślij do skonfigurowanych kanałów (Discord/Telegram)")
		fs.BoolVar(&asJSON, "json", false, "stabilny JSON (schema_version) zamiast tekstu")
	})
	if !ok {
		return exitUsage
	}
	logger := app.NewLogger(stderr, false)
	runner := &app.Runner{Config: cfg, Logger: logger}
	var client seed.Client
	if withSeed && cfg.QBittorrent != nil {
		c, err := runner.QBitClient()
		if err != nil {
			fmt.Fprintf(stderr, "zulea-upbrr summary: %s\n", logger.Sanitize(err.Error()))
			return exitRuntime
		}
		client = c
	}
	summary, err := runner.BuildSummary(ctx, client)
	code := exitOK
	if err != nil {
		code = exitRuntime
	} else if summary.Attention {
		code = exitStop
	}
	if asJSON {
		return writeCommandJSON(stdout, stderr, "summary", code, summary, err, logger)
	}
	if err != nil {
		fmt.Fprintf(stderr, "zulea-upbrr summary: %s\n", logger.Sanitize(err.Error()))
		return exitRuntime
	}
	fmt.Fprintln(stdout, summary.Text)
	if send {
		results, err := runner.Notify(ctx, summary.Text)
		if err != nil {
			fmt.Fprintf(stderr, "zulea-upbrr summary: %v\n", err)
			return exitRuntime
		}
		for name, e := range results {
			if e != nil {
				fmt.Fprintf(stderr, "powiadomienie %s: %v\n", name, e)
			} else {
				fmt.Fprintf(stderr, "powiadomienie %s: wysłane\n", name)
			}
		}
	}
	if summary.Attention {
		return exitStop
	}
	return exitOK
}
