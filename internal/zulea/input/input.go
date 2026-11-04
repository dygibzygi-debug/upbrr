// Copyright (c) 2026, Zulea contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

// Package input is the Zulea input adapter: it turns one frozen release into
// verified, ready-made upload inputs without recomputing anything.
//
// Chain of evidence (any break is a STOP for the release):
//
//	freeze (status OK, MKV stat identity)
//	  -> release directory (read only: exactly one MKV and one NFO, no partial state)
//	  -> NFO (non-empty, RELEASE field = directory name, stat + SHA-256 = TM manifest)
//	  -> TM manifest (APPLY, row OK, profile APPLY_OK/ALREADY_OK, same MKV identity and TMDB_ID)
//	  -> TM torrent (exact info, SHA-256 and infohash = manifest)
//	  -> tracker title (names.Build on the frozen name and TMDB titles file)
//
// The adapter never opens <REL>.screens.json: screenshot links come only from
// the ready description. Tracker specifics are data in Profile, so a new
// tracker (CinemaMovies later) is a new Profile value, not an adapter change.
package input

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/autobrr/upbrr/internal/zulea/freeze"
	"github.com/autobrr/upbrr/internal/zulea/names"
	"github.com/autobrr/upbrr/internal/zulea/tm"
)

// ErrStop marks a release that must not be uploaded. Every adapter refusal wraps it.
var ErrStop = errors.New("zulea: STOP")

// Size limits for the small text inputs.
const (
	MaxNFOBytes         = 1 << 20
	MaxDescriptionBytes = 1 << 20
)

// Profile describes one tracker's ready-made inputs.
type Profile struct {
	// Tracker is the upbrr registry name, e.g. "PTT".
	Tracker string
	// TMProfile is the Torrent Maker profile (manifest column and file infix), e.g. "ptt".
	TMProfile string
	// DescriptionSuffix names the ready description next to the MKV.
	DescriptionSuffix string
	// TorrentSuffix names the TM torrent in the TM output directory.
	TorrentSuffix string
	// Title selects the tracker title from the shared naming result.
	Title func(names.Result) string
}

// PTT is the PolishTorrent profile (decision D-3: the CinemaMovies description is used unchanged).
var PTT = Profile{
	Tracker:           "PTT",
	TMProfile:         "ptt",
	DescriptionSuffix: ".cinemamovies.txt",
	TorrentSuffix:     ".ptt.upload.torrent",
	Title:             func(r names.Result) string { return r.PTT },
}

// Profiles lists the profiles enabled in this build.
func Profiles() []Profile { return []Profile{PTT} }

// ProfileByTracker returns the profile for tracker (case-insensitive).
func ProfileByTracker(tracker string) (Profile, bool) {
	for _, p := range Profiles() {
		if strings.EqualFold(p.Tracker, strings.TrimSpace(tracker)) {
			return p, true
		}
	}
	return Profile{}, false
}

// Sources locates the Zulea inputs. Every path is read only.
type Sources struct {
	LibraryRoot  string
	FreezePath   string
	FreezeSHA256 string
	TitlesPath   string
	ManifestPath string
	TorrentDir   string
}

// Reader opens files for the adapter. The default reads the local filesystem;
// tests substitute a recording reader to prove which files are touched.
type Reader interface {
	ReadFile(path string, limit int64) ([]byte, error)
}

type osReader struct{}

func (osReader) ReadFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

// Adapter prepares releases from Sources.
type Adapter struct {
	sources  Sources
	reader   Reader
	freeze   *freeze.Freeze
	titles   names.Titles
	manifest *tm.Manifest
}

// Open loads the freeze (SHA-verified), titles file and TM manifest.
func Open(sources Sources) (*Adapter, error) {
	return OpenWithReader(sources, osReader{})
}

// OpenWithReader is Open with an explicit Reader for release files.
func OpenWithReader(sources Sources, reader Reader) (*Adapter, error) {
	for name, value := range map[string]string{
		"library root":      sources.LibraryRoot,
		"freeze":            sources.FreezePath,
		"titles":            sources.TitlesPath,
		"manifest":          sources.ManifestPath,
		"torrent directory": sources.TorrentDir,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("zulea: %s path is required", name)
		}
	}
	frozen, err := freeze.Load(sources.FreezePath, sources.FreezeSHA256)
	if err != nil {
		return nil, fmt.Errorf("zulea: freeze: %w", err)
	}
	titles, err := names.LoadTitles(sources.TitlesPath)
	if err != nil {
		return nil, fmt.Errorf("zulea: titles: %w", err)
	}
	manifest, err := tm.LoadManifest(sources.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("zulea: TM manifest: %w", err)
	}
	return &Adapter{
		sources:  sources,
		reader:   reader,
		freeze:   frozen,
		titles:   titles,
		manifest: manifest,
	}, nil
}

// Freeze exposes the loaded freeze for reports.
func (a *Adapter) Freeze() *freeze.Freeze { return a.freeze }

// ManifestMeta exposes manifest comment metadata (TRYB, FREEZE_SHA, ...).
func (a *Adapter) ManifestMeta() map[string]string { return a.manifest.Meta }

// FileEvidence is the identity of one library file.
type FileEvidence struct {
	Path   string
	Ino    uint64
	Size   int64
	Mtime  int64
	SHA256 string
}

// Prepared is one release ready for a tracker plan.
type Prepared struct {
	Release   string
	Profile   Profile
	Record    freeze.Record
	Parsed    names.Parsed
	Directory string
	MKV       FileEvidence
	NFO       FileEvidence
	// Description is the exact description text (UTF-8, LF, no BOM).
	Description       string
	DescriptionPath   string
	DescriptionSHA256 string
	// ImageURLs are the [img] links found in the description, in order.
	ImageURLs   []string
	Torrent     *tm.Torrent
	TorrentPath string
	Manifest    tm.ManifestRow
	Title       string
	Naming      names.Result
	// Warnings are non-blocking findings to show in the dry-run.
	Warnings []string
}

func stop(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrStop, fmt.Sprintf(format, args...))
}

func wrapStop(err error) error {
	if err == nil || errors.Is(err, ErrStop) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrStop, err)
}

// validReleaseName accepts a single path element.
func validReleaseName(rel string) bool {
	return rel != "" && rel != "." && rel != ".." && !strings.ContainsAny(rel, `/\`+"\x00") && filepath.Base(rel) == rel
}

var partialPattern = regexp.MustCompile(`(?i)(\.partial|\.journal$|\.lock$)`)

// Prepare verifies and collects every input for release on profile.
func (a *Adapter) Prepare(release string, profile Profile) (*Prepared, error) {
	if !validReleaseName(release) {
		return nil, stop("invalid release name")
	}
	if profile.Tracker == "" || profile.TMProfile == "" || profile.DescriptionSuffix == "" || profile.TorrentSuffix == "" || profile.Title == nil {
		return nil, stop("incomplete tracker profile")
	}
	parsed, err := names.Parse(release)
	if err != nil {
		return nil, stop("release name: %v", err)
	}
	rec, err := a.freeze.Eligible(release)
	if err != nil {
		return nil, wrapStop(err)
	}
	dir := filepath.Join(a.sources.LibraryRoot, release)
	if err := checkReleaseDirectory(dir, release); err != nil {
		return nil, err
	}
	mkvPath := filepath.Join(dir, release+".mkv")
	nfoPath := filepath.Join(dir, release+".nfo")
	mkvID, err := freeze.VerifyMKV(rec, mkvPath)
	if err != nil {
		return nil, wrapStop(err)
	}
	row, err := a.manifest.Proven(release, profile.TMProfile)
	if err != nil {
		return nil, wrapStop(err)
	}
	if row.MKVIno != rec.MKVIno || row.MKVSize != rec.MKVSize || row.MKVMtime != rec.MKVMtime ||
		row.TMDBID != rec.TMDBID || row.SourceID != rec.SourceID {
		return nil, stop("TM manifest row differs from freeze record (MKV identity, TMDB_ID or SOURCE_ID)")
	}

	nfo, nfoBytes, err := a.readNFO(nfoPath, release)
	if err != nil {
		return nil, err
	}
	if strconv.FormatUint(nfo.Ino, 10) != row.NFOIno || strconv.FormatInt(nfo.Size, 10) != row.NFOSize ||
		strconv.FormatInt(nfo.Mtime, 10) != row.NFOMtime || nfo.SHA256 != row.NFOSHA256 {
		return nil, stop("NFO differs from TM manifest (stat or SHA-256): torrent pieces would not match")
	}

	descPath := filepath.Join(dir, release+profile.DescriptionSuffix)
	description, descSHA, err := a.readDescription(descPath)
	if err != nil {
		return nil, err
	}

	torrentPath := filepath.Join(a.sources.TorrentDir, release+profile.TorrentSuffix)
	raw, err := tm.ReadFile(torrentPath)
	if err != nil {
		return nil, wrapStop(err)
	}
	mkvSize, _ := strconv.ParseInt(rec.MKVSize, 10, 64)
	torrent, err := tm.Parse(raw, tm.Expectation{
		Release: release,
		Sizes:   map[string]int64{release + ".mkv": mkvSize, release + ".nfo": int64(len(nfoBytes))},
	})
	if err != nil {
		return nil, wrapStop(err)
	}
	_, wantDigest, _ := row.ProfileStatus(profile.TMProfile)
	if torrent.SHA256 != wantDigest {
		return nil, stop("TM torrent SHA-256 differs from manifest")
	}
	if torrent.InfoHash != row.InfoHash {
		return nil, stop("TM torrent infohash differs from manifest")
	}

	naming := names.Build(release, a.titles.Lookup(rec.TMDBID))
	if naming.Error != "" {
		return nil, stop("tracker title: %s", naming.Error)
	}
	title := profile.Title(naming)
	if strings.TrimSpace(title) == "" {
		return nil, stop("empty tracker title")
	}

	// Post-check: the library must not have changed while inputs were collected.
	if _, err := freeze.VerifyMKV(rec, mkvPath); err != nil {
		return nil, stop("MKV changed during preparation: %v", err)
	}
	if after, _, err := a.readNFO(nfoPath, release); err != nil || after != nfo {
		return nil, stop("NFO changed during preparation")
	}

	warnings := append([]string(nil), naming.Warnings...)
	if meta := a.manifest.Meta["FREEZE_SHA"]; meta != "" && meta != a.freeze.SHA256 {
		warnings = append(warnings, "manifest TM pochodzi z innego pliku zamrożenia (rekord zgodny)")
	}
	images := imageURLs(description)
	if len(images) == 0 {
		warnings = append(warnings, "opis nie zawiera żadnego [img]")
	}
	return &Prepared{
		Release:   release,
		Profile:   profile,
		Record:    rec,
		Parsed:    parsed,
		Directory: dir,
		MKV: FileEvidence{
			Path:  mkvPath,
			Ino:   mkvID.Ino,
			Size:  mkvID.Size,
			Mtime: mkvID.Mtime,
		},
		NFO:               nfo,
		Description:       description,
		DescriptionPath:   descPath,
		DescriptionSHA256: descSHA,
		ImageURLs:         images,
		Torrent:           torrent,
		TorrentPath:       torrentPath,
		Manifest:          row,
		Title:             title,
		Naming:            naming,
		Warnings:          warnings,
	}, nil
}

// checkReleaseDirectory lists (never opens) the directory entries: exactly one
// MKV and one NFO, both the expected regular files, and no partial state.
func checkReleaseDirectory(dir string, release string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return stop("release directory: %v", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return stop("release directory missing or a symbolic link")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return stop("list release directory: %v", err)
	}
	mkv, nfo := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, ".mkv"):
			mkv++
		case strings.HasSuffix(lower, ".nfo"):
			nfo++
		}
		if partialPattern.MatchString(name) {
			return stop("active partial state in release directory")
		}
	}
	if mkv != 1 || nfo != 1 {
		return stop("release directory must hold exactly 1 MKV and 1 NFO (found %d/%d)", mkv, nfo)
	}
	for _, name := range []string{release + ".mkv", release + ".nfo"} {
		st, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || st.Mode()&fs.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return stop("%s is not a regular file", name)
		}
	}
	return nil
}

func (a *Adapter) readNFO(path string, release string) (FileEvidence, []byte, error) {
	id, err := freeze.StatIdentity(path)
	if err != nil {
		return FileEvidence{}, nil, stop("NFO stat: %v", err)
	}
	data, err := a.reader.ReadFile(path, MaxNFOBytes)
	if err != nil {
		return FileEvidence{}, nil, stop("NFO read: %v", err)
	}
	if len(data) == 0 {
		return FileEvidence{}, nil, stop("NFO is empty")
	}
	if int64(len(data)) != id.Size {
		return FileEvidence{}, nil, stop("NFO size changed while reading")
	}
	if !nfoReleaseMatches(data, release) {
		return FileEvidence{}, nil, stop("RELEASE field in NFO differs from the directory name")
	}
	sum := sha256.Sum256(data)
	return FileEvidence{
		Path:   path,
		Ino:    id.Ino,
		Size:   id.Size,
		Mtime:  id.Mtime,
		SHA256: hex.EncodeToString(sum[:]),
	}, data, nil
}

// nfoReleaseMatches mirrors the TM precheck: exactly one line whose
// left-trimmed text starts with "RELEASE.........:", equal to the expected
// field with an optional two-space indent.
func nfoReleaseMatches(data []byte, release string) bool {
	want := "RELEASE.........: " + release
	count := 0
	got := ""
	for _, raw := range bytes.Split(data, []byte("\n")) {
		line := string(bytes.TrimSuffix(raw, []byte("\r")))
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "RELEASE.........:") {
			count++
			got = line
		}
	}
	return count == 1 && (got == want || got == "  "+want)
}

func (a *Adapter) readDescription(path string) (string, string, error) {
	data, err := a.reader.ReadFile(path, MaxDescriptionBytes)
	if err != nil {
		return "", "", stop("description read: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", "", stop("description is empty")
	}
	if !utf8.Valid(data) {
		return "", "", stop("description is not valid UTF-8")
	}
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return "", "", stop("description starts with a BOM")
	}
	if bytes.ContainsRune(data, '\r') {
		return "", "", stop("description contains CR (LF line endings required)")
	}
	sum := sha256.Sum256(data)
	return string(data), hex.EncodeToString(sum[:]), nil
}

var imgPattern = regexp.MustCompile(`(?i)\[img(?:=[^\]]*)?\]\s*(https?://[^\s\[\]]+)\s*\[/img\]`)

func imageURLs(description string) []string {
	matches := imgPattern.FindAllStringSubmatch(description, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}
