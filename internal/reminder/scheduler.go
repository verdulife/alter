// Package reminder schedules reminder firings on the machine. The vault holds
// the source of truth; this package only talks to the alarms that wake the
// program up, and it does so behind Scheduler, so no other part of the program
// depends on systemd. The timers are transient on purpose: a machine restart
// wipes them and the next sync rebuilds them from the vault.
package reminder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scheduler is the whole of what the rest of the program knows about the
// machine's alarms: create one, remove one, and list the ones that exist. The
// interface is the seam that keeps `sync` testable without touching the machine.
type Scheduler interface {
	// Create schedules one firing of the reminder named slug at at.
	Create(ctx context.Context, slug string, at time.Time) error
	// Remove stops the alarm for slug. An alarm that is not there is not an
	// error, because sync removes the ones whose reminder file is gone and a
	// restart has already deleted every transient timer.
	Remove(ctx context.Context, slug string) error
	// List returns the slugs whose alarms currently exist.
	List(ctx context.Context) ([]string, error)
}

const (
	// unitPrefix names every transient unit this program owns, so a unit that
	// does not carry it is never listed nor removed.
	unitPrefix = "alter-remind-"
	// calendarLayout is the wall-clock shape systemd accepts in OnCalendar=: a
	// plain local timestamp, with no zone and no offset.
	calendarLayout = "2006-01-02 15:04:05"
)

// unitName returns the unit base name of slug, without a unit suffix.
func unitName(slug string) string { return unitPrefix + slug }

// timerUnit returns the timer unit of slug.
func timerUnit(slug string) string { return unitName(slug) + ".timer" }

// RunFunc runs one command and returns the standard output it wrote. When the
// command fails it returns the standard error it wrote, if there was one,
// alongside the error, so the caller can put the reason into what it reports.
// It exists so tests inspect argv without executing anything.
type RunFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Systemd schedules reminders with transient user timers: systemd-run creates
// the timer and systemctl stops it. Nothing is written to the unit folders, so
// there is no fichero per reminder, no daemon-reload and no enable; the price is
// that a machine restart wipes them, which the next sync covers.
//
// A transient unit does not inherit the service's environment: it starts from
// what the systemd user manager has, which is nearly nothing. A firing has to
// find the binary it runs and the token it notifies with, so both travel with
// the unit: the PATH through --setenv and the dotenv file through
// EnvironmentFile=. It also does not inherit the service's working directory, so
// the directory the bridge works in travels with the unit too: the dotenv file
// the firing loads on its own is the one relative to it.
type Systemd struct {
	binary string
	// path is the PATH the transient unit receives. It is a constructor input so
	// the caller (and a test) decides it instead of this package guessing.
	path string
	// envFile is the dotenv file the transient unit loads, empty when the caller
	// has none.
	envFile string
	// workDir is the directory the transient unit runs in, empty when the caller
	// has none. It is the bridge's own working directory, which is what makes the
	// fallback that reads <WorkDir>/.env find the file.
	workDir string
	run     RunFunc
}

// NewSystemd returns a Scheduler that starts binary when a timer fires. binary,
// path, envFile and workDir are passed in rather than guessed, so the caller
// decides which executable systemd runs and where, and with which environment. An
// empty envFile is a scheduler whose timers carry only path, and an empty workDir
// is one whose timers keep the systemd default directory.
func NewSystemd(binary, path, envFile, workDir string) *Systemd {
	return newSystemd(binary, path, envFile, workDir, execCommand)
}

// newSystemd builds the implementation around a command runner. Tests use it to
// assert the exact argv without running a command.
func newSystemd(binary, path, envFile, workDir string, run RunFunc) *Systemd {
	return &Systemd{binary: binary, path: path, envFile: envFile, workDir: workDir, run: run}
}

// Create schedules one firing of the reminder at at with
// `systemd-run --user --on-calendar=<when> --unit=alter-remind-<slug>`. The
// calendar value is the reminder's wall-clock time in the host zone, because
// systemd reads OnCalendar= as local time: sending the instant's own offset
// would move the alarm by the offset difference. The unit also carries the PATH
// it needs to find the binary, the dotenv file it needs to notify when one is
// configured and exists, and the directory it runs in.
func (s *Systemd) Create(ctx context.Context, slug string, at time.Time) error {
	when := at.In(time.Local).Format(calendarLayout)
	args := []string{
		"--user",
		"--on-calendar=" + when,
		"--unit=" + unitName(slug),
	}
	// Without this the unit inherits the manager's minimal PATH and cannot find the
	// binary it is told to run, nor the interpreter that binary wraps. An empty one
	// is not passed: setting PATH to nothing would leave the unit worse off than the
	// systemd default.
	if s.path != "" {
		args = append(args, "--setenv=PATH="+s.path)
	}
	// systemd-run refuses to create a unit whose EnvironmentFile= it cannot read, so
	// a file that is not there is not passed: the process environment may already
	// carry the credentials.
	if s.envFile != "" && fileIsReadable(s.envFile) {
		args = append(args, "--property=EnvironmentFile="+s.envFile)
	}
	// Without this the unit runs in $HOME, not in the directory the service works
	// in, so the second layer that reads <WorkDir>/.env on its own would never find
	// the file. An empty directory is not passed: an empty WorkingDirectory= would
	// make systemd-run refuse the unit.
	if s.workDir != "" {
		args = append(args, "--property=WorkingDirectory="+s.workDir)
	}
	args = append(args, s.binary, "remind", "fire", slug)
	_, err := s.runCommand(ctx, "systemd-run", args...)
	return err
}

// fileIsReadable reports whether path is a readable regular file, which is what
// systemd needs to accept it as an EnvironmentFile=.
func fileIsReadable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	return file.Close() == nil
}

// Remove stops the timer of slug with `systemctl --user stop`. A unit systemd
// does not know is not a failure: sync removes the timers of deleted reminders
// and a restart already deleted every transient one.
func (s *Systemd) Remove(ctx context.Context, slug string) error {
	_, err := s.runCommand(ctx, "systemctl", "--user", "stop", timerUnit(slug))
	if err == nil || isMissingUnit(err) {
		return nil
	}
	return err
}

// List returns the slugs whose timers exist. --all includes the timers that are
// not running, so one scheduled far ahead is still reported, and every unit that
// is not ours is dropped while parsing.
func (s *Systemd) List(ctx context.Context) ([]string, error) {
	out, err := s.runCommand(ctx, "systemctl", "--user", "list-timers", "--all", "--output=json")
	if err == nil {
		if slugs, jsonErr := slugsFromJSON(out); jsonErr == nil {
			return slugs, nil
		}
		// systemctl succeeded but the payload was not that JSON shape: read the
		// plain table in front of us instead of failing a successful call.
		return slugsFromTable(out), nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	// An older systemd rejects --output=json outright, so ask for the table.
	plain, plainErr := s.runCommand(ctx, "systemctl", "--user", "list-timers", "--all")
	if plainErr != nil {
		return nil, plainErr
	}
	return slugsFromTable(plain), nil
}

// runCommand runs one command and wraps its stderr into the error, so a failure
// explains itself instead of being a bare "exit status 1".
func (s *Systemd) runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", line, err)
	}
	out, err := s.run(ctx, name, args...)
	if err == nil {
		return out, nil
	}
	// A command killed by a cancelled context fails with "signal: killed"; the
	// context error is the reason worth reporting.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("%s: %w", line, ctxErr)
	}
	if reason := strings.TrimSpace(string(out)); reason != "" {
		return nil, fmt.Errorf("%s: %w: %s", line, err, reason)
	}
	return nil, fmt.Errorf("%s: %w", line, err)
}

// isMissingUnit reports whether a failed systemctl call failed only because the
// unit is unknown. The exit code does not say so (stop on a unit that is not
// loaded exits 5, like other "no such unit" failures), so the message is what is
// left; execCommand pins the C locale to keep the wording matchable. A cancelled
// call is never read as a missing unit.
func isMissingUnit(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for _, marker := range []string{"not loaded", "not found", "does not exist", "No such file or directory"} {
		if strings.Contains(err.Error(), marker) {
			return true
		}
	}
	return false
}

// execCommand is the real runner. It forces a C locale so the tool messages it
// captures stay in English: Remove recognises the wording systemd uses for a
// unit it does not know, and a translated message would hide it.
func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = withCLocale(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Stdout is dropped on failure: stderr is what explains it, and mixing
		// both would blur the two in the error message.
		return stderr.Bytes(), err
	}
	return out, nil
}

// withCLocale returns env with every locale variable replaced by the C locale,
// so a duplicated key cannot win over the value set here.
func withCLocale(env []string) []string {
	cleaned := make([]string, 0, len(env)+2)
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, "LC_ALL="),
			strings.HasPrefix(entry, "LC_MESSAGES="),
			strings.HasPrefix(entry, "LANG="):
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return append(cleaned, "LC_ALL=C", "LANG=C")
}

// timerEntry is the part of one list-timers JSON object that is used. Every
// other key (next, left, last, passed, activates) is ignored, so a systemd that
// adds or renames a column does not break the parse.
type timerEntry struct {
	Unit string `json:"unit"`
}

// slugsFromJSON reads the array shape of `list-timers --output=json`.
func slugsFromJSON(data []byte) ([]string, error) {
	var entries []timerEntry
	if err := json.Unmarshal(bytes.TrimSpace(data), &entries); err != nil {
		return nil, fmt.Errorf("list-timers no devolvió JSON: %w", err)
	}
	var slugs []string
	for _, entry := range entries {
		if slug, ok := slugFromUnit(entry.Unit); ok {
			slugs = append(slugs, slug)
		}
	}
	return slugs, nil
}

// slugsFromTable reads the plain table of `list-timers`, the shape the versions
// without --output=json print. The columns are fixed but their widths are not,
// so a unit is found by its name rather than by its position.
func slugsFromTable(data []byte) []string {
	var slugs []string
	for _, line := range strings.Split(string(data), "\n") {
		for _, field := range strings.Fields(line) {
			if slug, ok := slugFromUnit(field); ok {
				slugs = append(slugs, slug)
			}
		}
	}
	return slugs
}

// slugFromUnit extracts the slug of a unit this program owns and rejects every
// other name, including our own prefix with an empty slug.
func slugFromUnit(name string) (string, bool) {
	if !strings.HasPrefix(name, unitPrefix) || !strings.HasSuffix(name, ".timer") {
		return "", false
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(name, unitPrefix), ".timer")
	return slug, slug != ""
}

// The operations FailOp can be told to fail.
const (
	OpCreate = "create"
	OpRemove = "remove"
	OpList   = "list"
)

// FakeCreate is one recorded Create call.
type FakeCreate struct {
	// Slug is the reminder the call was about.
	Slug string
	// At is the instant it was scheduled for.
	At time.Time
}

// Fake is an in-memory Scheduler: it records every call it receives and answers
// with the state it keeps, so a caller such as sync can be tested without
// systemd. It is safe for concurrent use and its zero value is ready to use.
type Fake struct {
	mu sync.Mutex
	// units is what List returns and what Create and Remove keep in step, so
	// create-then-list behaves like the real scheduler.
	units    []string
	created  []FakeCreate
	removed  []string
	lists    int
	failures map[string]error
}

var (
	_ Scheduler = (*Systemd)(nil)
	_ Scheduler = (*Fake)(nil)
)

// NewFake returns an empty Fake.
func NewFake() *Fake { return &Fake{} }

// Create records the call and schedules slug, so a later List reports it.
func (f *Fake) Create(ctx context.Context, slug string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Recorded before a failure is reported: the call did happen.
	f.created = append(f.created, FakeCreate{Slug: slug, At: at})
	if err := f.failures[OpCreate]; err != nil {
		return err
	}
	if !f.hasLocked(slug) {
		f.units = append(f.units, slug)
	}
	return nil
}

// Remove records the call and unschedules slug.
func (f *Fake) Remove(ctx context.Context, slug string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, slug)
	if err := f.failures[OpRemove]; err != nil {
		return err
	}
	kept := make([]string, 0, len(f.units))
	for _, unit := range f.units {
		if unit != slug {
			kept = append(kept, unit)
		}
	}
	f.units = kept
	return nil
}

// List records the call and returns the scheduled slugs sorted, so an assertion
// never depends on the order the caller created them in.
func (f *Fake) List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if err := f.failures[OpList]; err != nil {
		return nil, err
	}
	slugs := append([]string(nil), f.units...)
	sort.Strings(slugs)
	return slugs, nil
}

// Created returns the recorded Create calls in call order.
func (f *Fake) Created() []FakeCreate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeCreate(nil), f.created...)
}

// Removed returns the slugs Remove was called with, in call order, including the
// calls that were told to fail.
func (f *Fake) Removed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

// Lists returns how many times List was called.
func (f *Fake) Lists() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists
}

// SetUnits replaces what List reports, which is how a test pretends the machine
// already has some timers.
func (f *Fake) SetUnits(slugs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.units = append([]string(nil), slugs...)
}

// Fail makes every operation fail with err.
func (f *Fake) Fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = map[string]error{OpCreate: err, OpRemove: err, OpList: err}
}

// FailOp makes only op fail with err, so one failing step of a sequence can be
// exercised while the others still answer normally.
func (f *Fake) FailOp(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures == nil {
		f.failures = make(map[string]error, 1)
	}
	f.failures[op] = err
}

// hasLocked reports whether slug is scheduled. The caller holds the lock.
func (f *Fake) hasLocked(slug string) bool {
	for _, unit := range f.units {
		if unit == slug {
			return true
		}
	}
	return false
}
