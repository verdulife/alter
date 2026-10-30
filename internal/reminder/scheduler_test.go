package reminder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The tests below never run a command and never touch the system clock: the
// systemd implementation is built around an injected runner, and every instant
// is a literal fixture.

// fixedAt is the instant the Create tests schedule for: 12:00:00 wall clock in
// the host zone, which is what systemd must receive.
func fixedAt() time.Time {
	return time.Date(2026, 11, 3, 12, 0, 0, 0, time.Local)
}

// command is one command the runner was asked to run.
type command struct {
	name string
	args []string
}

// argv renders a command for an assertion failure message.
func (c command) argv() string { return c.name + " " + fmt.Sprintf("%q", c.args) }

// recorder is the injected runner. It records every command and answers with the
// payload the test set, so no test ever executes systemd.
type recorder struct {
	calls []command
	// reply, when set, answers a call and wins over the fields below.
	reply func(c command) ([]byte, error)
	// stdout is returned on success.
	stdout string
	// stderr is returned with err on failure, the shape the real runner uses.
	stderr string
	err    error
}

func (r *recorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	c := command{name: name, args: append([]string(nil), args...)}
	r.calls = append(r.calls, c)
	if r.reply != nil {
		return r.reply(c)
	}
	if r.err != nil {
		return []byte(r.stderr), r.err
	}
	return []byte(r.stdout), nil
}

// onlyCall returns the single command that was run, and fails the test when the
// runner was called a different number of times.
func (r *recorder) onlyCall(t *testing.T) command {
	t.Helper()
	if len(r.calls) != 1 {
		t.Fatalf("ran %d commands (%v), want exactly 1", len(r.calls), r.calls)
	}
	return r.calls[0]
}

// assertCommand fails the test unless got is the exact argv expected.
func assertCommand(t *testing.T, got command, wantName string, wantArgs ...string) {
	t.Helper()
	if got.name != wantName {
		t.Errorf("command name = %q, want %q", got.name, wantName)
	}
	if !slices.Equal(got.args, wantArgs) {
		t.Errorf("argv = %s, want %s %q", got.argv(), wantName, wantArgs)
	}
}

// bareSystemd builds a scheduler whose transient units carry no extra
// environment, for the tests that only look at the systemctl side.
func bareSystemd(binary string, run RunFunc) *Systemd {
	return newSystemd(binary, "", "", "", run)
}

// writeEnvFile creates a dotenv file the scheduler can point a transient unit at.
func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestCreateBuildsSystemdRunArgv pins the exact argv of a timer creation: a
// transient user timer that runs this binary's `remind fire <slug>` and carries
// the environment a firing needs, because a transient unit does not inherit the
// service's. The working directory travels with it too: the dotenv file the
// firing loads is the one relative to the bridge's directory, and a transient
// unit would otherwise run in $HOME.
func TestCreateBuildsSystemdRunArgv(t *testing.T) {
	envFile := writeEnvFile(t, "TELEGRAM_TOKEN=no-es-un-token-real\n")
	rec := &recorder{}
	scheduler := newSystemd("/home/owner/bin/alter", "/home/owner/.local/bin:/usr/bin", envFile, "/home/owner/alter", rec.run)

	if err := scheduler.Create(context.Background(), "llamar-a-mama", fixedAt()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	assertCommand(t, rec.onlyCall(t), "systemd-run",
		"--user",
		"--on-calendar=2026-11-03 12:00:00",
		"--unit=alter-remind-llamar-a-mama",
		"--setenv=PATH=/home/owner/.local/bin:/usr/bin",
		"--property=EnvironmentFile="+envFile,
		"--property=WorkingDirectory=/home/owner/alter",
		"/home/owner/bin/alter",
		"remind", "fire", "llamar-a-mama",
	)
}

// TestCreateSkipsAnEmptyWorkingDirectory pins the guard around the working
// directory: a scheduler built without one emits exactly the argv it emitted
// before the property existed, so a caller with no directory to pass keeps the
// previous behaviour instead of sending systemd an empty path.
func TestCreateSkipsAnEmptyWorkingDirectory(t *testing.T) {
	rec := &recorder{}
	scheduler := newSystemd("/bin/alter", "/bin", "", "", rec.run)

	if err := scheduler.Create(context.Background(), "regar-las-plantas", fixedAt()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, arg := range rec.onlyCall(t).args {
		if strings.Contains(arg, "WorkingDirectory") {
			t.Errorf("argv = %q, want no WorkingDirectory= when there is no directory to pass", rec.calls[0].args)
		}
	}
	assertCommand(t, rec.calls[0], "systemd-run",
		"--user",
		"--on-calendar=2026-11-03 12:00:00",
		"--unit=alter-remind-regar-las-plantas",
		"--setenv=PATH=/bin",
		"/bin/alter",
		"remind", "fire", "regar-las-plantas",
	)
}

// TestCreatePassesThePathWithoutAnEnvFile pins the half of the environment that
// is always there: the timer's PATH is the one the bridge itself has, and no
// EnvironmentFile= argument appears when none is configured.
func TestCreatePassesThePathWithoutAnEnvFile(t *testing.T) {
	rec := &recorder{}
	scheduler := newSystemd("/bin/alter", "/home/owner/.local/bin:/bin", "", "", rec.run)

	if err := scheduler.Create(context.Background(), "regar-las-plantas", fixedAt()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	args := rec.onlyCall(t).args
	if !slices.Contains(args, "--setenv=PATH=/home/owner/.local/bin:/bin") {
		t.Errorf("argv = %q, want the bridge's PATH for the transient unit", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "EnvironmentFile") {
			t.Errorf("argv = %q, want no EnvironmentFile= when none is configured", args)
		}
	}
}

// TestCreateKeepsTheSystemdPATHWhenThereIsNoneToPass pins the guard around an
// empty PATH: setting it to nothing would leave the unit worse off than systemd's
// own default, so nothing is passed instead.
func TestCreateKeepsTheSystemdPATHWhenThereIsNoneToPass(t *testing.T) {
	rec := &recorder{}
	scheduler := newSystemd("/bin/alter", "", "", "", rec.run)

	if err := scheduler.Create(context.Background(), "regar-las-plantas", fixedAt()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	args := rec.onlyCall(t).args
	for _, arg := range args {
		if strings.HasPrefix(arg, "--setenv=") {
			t.Errorf("argv = %q, want no --setenv= when there is no PATH to pass", args)
		}
	}
	assertCommand(t, rec.calls[0], "systemd-run",
		"--user",
		"--on-calendar=2026-11-03 12:00:00",
		"--unit=alter-remind-regar-las-plantas",
		"/bin/alter",
		"remind", "fire", "regar-las-plantas",
	)
}

// TestCreateSkipsAnEnvFileThatIsNotThere pins the guard around the dotenv file:
// systemd-run would fail the whole creation over a file it cannot read, so a
// configured path that does not exist is simply not passed.
func TestCreateSkipsAnEnvFileThatIsNotThere(t *testing.T) {
	rec := &recorder{}
	missing := filepath.Join(t.TempDir(), "no-existe.env")
	scheduler := newSystemd("/bin/alter", "/bin", missing, "", rec.run)

	if err := scheduler.Create(context.Background(), "regar-las-plantas", fixedAt()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	args := rec.onlyCall(t).args
	if !slices.Contains(args, "--setenv=PATH=/bin") {
		t.Errorf("argv = %q, want the PATH argument kept", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "EnvironmentFile") {
			t.Errorf("argv = %q, want no EnvironmentFile= for a file that is not there", args)
		}
	}
}

// TestCreateSendsLocalWallClockCalendar checks the calendar value is the local
// wall-clock time of the reminder in the shape systemd accepts, and not the
// instant with an offset: OnCalendar= is read as local time, so an RFC3339 value
// with a foreign offset would move the alarm.
func TestCreateSendsLocalWallClockCalendar(t *testing.T) {
	rec := &recorder{}
	scheduler := bareSystemd("/bin/alter", rec.run)
	// An instant written in a Tokyo offset: what matters is the host wall clock.
	at := time.Date(2026, 7, 1, 10, 30, 0, 0, time.FixedZone("TKY", 9*3600))

	if err := scheduler.Create(context.Background(), "regar-las-plantas", at); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := rec.onlyCall(t).args[1]
	if want := "--on-calendar=" + at.In(time.Local).Format("2006-01-02 15:04:05"); got != want {
		t.Errorf("calendar argument = %q, want %q", got, want)
	}
	// No zone suffix and no offset: exactly "YYYY-MM-DD HH:MM:SS".
	plain := regexp.MustCompile(`^--on-calendar=\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)
	if !plain.MatchString(got) {
		t.Errorf("calendar argument = %q, want the systemd OnCalendar shape", got)
	}
}

// TestRemoveBuildsSystemctlArgv pins the exact argv of a timer removal, and that
// a successful removal runs nothing else.
func TestRemoveBuildsSystemctlArgv(t *testing.T) {
	rec := &recorder{}
	scheduler := bareSystemd("/bin/alter", rec.run)

	if err := scheduler.Remove(context.Background(), "llamar-a-mama"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	assertCommand(t, rec.onlyCall(t), "systemctl",
		"--user", "stop", "alter-remind-llamar-a-mama.timer",
	)
}

// TestFailureSurfacesStderrAndReason checks a failing command is explained: the
// error names the command and carries the stderr it wrote, and still wraps the
// underlying error.
func TestFailureSurfacesStderrAndReason(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		reason := errors.New("exit status 1")
		rec := &recorder{
			stderr: "Failed to start transient timer unit: Unit alter-remind-llamar-a-mama.timer already exists.\n",
			err:    reason,
		}
		scheduler := bareSystemd("/bin/alter", rec.run)

		err := scheduler.Create(context.Background(), "llamar-a-mama", fixedAt())
		if err == nil {
			t.Fatal("Create on a failing command = nil, want an error")
		}
		if !errors.Is(err, reason) {
			t.Errorf("error = %v, want it to wrap %v", err, reason)
		}
		for _, want := range []string{
			"systemd-run",
			"--unit=alter-remind-llamar-a-mama",
			"Failed to start transient timer unit",
			"already exists",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("remove", func(t *testing.T) {
		// The leniency for a missing unit must not swallow a real failure.
		rec := &recorder{
			stderr: "Failed to stop alter-remind-x.timer: Access denied\n",
			err:    errors.New("exit status 1"),
		}
		scheduler := bareSystemd("/bin/alter", rec.run)

		err := scheduler.Remove(context.Background(), "x")
		if err == nil {
			t.Fatal("Remove on a denied command = nil, want an error")
		}
		if !strings.Contains(err.Error(), "Access denied") {
			t.Errorf("error %q does not mention the stderr systemctl wrote", err)
		}
	})
}

// TestRemoveOfMissingUnitIsNotAnError checks the removal of a timer that does
// not exist succeeds: sync removes the timers of deleted reminders, and a
// restart has already deleted every transient one.
func TestRemoveOfMissingUnitIsNotAnError(t *testing.T) {
	rec := &recorder{
		stderr: "Failed to stop alter-remind-llamar-a-mama.timer: Unit alter-remind-llamar-a-mama.timer not loaded.\n",
		err:    errors.New("exit status 5"),
	}
	scheduler := bareSystemd("/bin/alter", rec.run)

	if err := scheduler.Remove(context.Background(), "llamar-a-mama"); err != nil {
		t.Fatalf("Remove of a missing unit = %v, want nil", err)
	}
	// It is not silently skipped either: the stop was attempted once.
	assertCommand(t, rec.onlyCall(t), "systemctl",
		"--user", "stop", "alter-remind-llamar-a-mama.timer",
	)
}

// TestListParsesJSONTimers feeds a realistic `list-timers --output=json` payload
// and checks only the units of this program become slugs.
func TestListParsesJSONTimers(t *testing.T) {
	payload := `[
  {
    "next": 1793707200000000,
    "left": 3596000000,
    "last": 1793617200000000,
    "passed": 6400000000,
    "unit": "alter-remind-llamar-a-mama.timer",
    "activates": "alter-remind-llamar-a-mama.service"
  },
  {
    "next": 1794312000000000,
    "left": 604800000000,
    "last": null,
    "passed": null,
    "unit": "alter-remind-regar-las-plantas.timer",
    "activates": "alter-remind-regar-las-plantas.service"
  },
  {
    "next": 1793707200000000,
    "left": 3596000000,
    "last": 1793617200000000,
    "passed": 6400000000,
    "unit": "apt-daily-upgrade.timer",
    "activates": "apt-daily-upgrade.service"
  },
  {
    "next": 1793707200000000,
    "left": 3596000000,
    "last": null,
    "passed": null,
    "unit": "alter-reminder-not-ours.timer",
    "activates": "alter-reminder-not-ours.service"
  },
  {
    "next": null,
    "left": null,
    "last": null,
    "passed": null,
    "unit": "alter-remind-.timer",
    "activates": "alter-remind-.service"
  }
]`
	rec := &recorder{stdout: payload}
	scheduler := bareSystemd("/bin/alter", rec.run)

	slugs, err := scheduler.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"llamar-a-mama", "regar-las-plantas"}; !slices.Equal(slugs, want) {
		t.Errorf("slugs = %q, want %q", slugs, want)
	}
	assertCommand(t, rec.onlyCall(t), "systemctl",
		"--user", "list-timers", "--all", "--output=json",
	)
}

// TestListFallsBackToThePlainTable checks a systemctl without --output=json is
// still usable: the flag is refused, the plain table is asked for, and the units
// of this program are read from it by name.
func TestListFallsBackToThePlainTable(t *testing.T) {
	table := strings.Join([]string{
		"NEXT                          LEFT        LAST                          PASSED     UNIT                                ACTIVATES",
		"Tue 2026-11-03 18:00:00 CET   5h 58min    Tue 2026-11-03 11:00:00 CET   1h ago     alter-remind-llamar-a-mama.timer    alter-remind-llamar-a-mama.service",
		"Tue 2026-11-10 08:00:00 CET   6 days      n/a                           n/a        alter-remind-regar-las-plantas.timer alter-remind-regar-las-plantas.service",
		"-                             -           Tue 2026-11-03 09:00:00 CET   3h ago     apt-daily.timer                     apt-daily.service",
		"",
		"3 timers listed.",
		"",
	}, "\n")
	rec := &recorder{reply: func(c command) ([]byte, error) {
		if slices.Contains(c.args, "--output=json") {
			return []byte("systemctl: unrecognized option '--output=json'\n"), errors.New("exit status 1")
		}
		return []byte(table), nil
	}}
	scheduler := bareSystemd("/bin/alter", rec.run)

	slugs, err := scheduler.List(context.Background())
	if err != nil {
		t.Fatalf("List with a systemctl that has no JSON output: %v", err)
	}
	if want := []string{"llamar-a-mama", "regar-las-plantas"}; !slices.Equal(slugs, want) {
		t.Errorf("slugs = %q, want %q", slugs, want)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("ran %d commands (%v), want the JSON attempt and the plain one", len(rec.calls), rec.calls)
	}
	assertCommand(t, rec.calls[1], "systemctl", "--user", "list-timers", "--all")
}

// TestCommandsHonourCancellation checks a cancelled context stops every
// operation and is the error the caller sees, both when it is already cancelled
// and when it is cancelled while the command runs.
func TestCommandsHonourCancellation(t *testing.T) {
	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := &recorder{}
		scheduler := bareSystemd("/bin/alter", rec.run)

		errs := map[string]error{
			"create": scheduler.Create(ctx, "llamar-a-mama", fixedAt()),
			"remove": scheduler.Remove(ctx, "llamar-a-mama"),
		}
		_, listErr := scheduler.List(ctx)
		errs["list"] = listErr

		for name, err := range errs {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s with a cancelled context = %v, want it to wrap context.Canceled", name, err)
			}
		}
		if len(rec.calls) != 0 {
			t.Errorf("ran %d commands (%v) after cancellation, want none", len(rec.calls), rec.calls)
		}
	})

	t.Run("cancelled while running", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		rec := &recorder{reply: func(command) ([]byte, error) {
			cancel()
			return []byte("signal: killed\n"), errors.New("signal: killed")
		}}
		scheduler := bareSystemd("/bin/alter", rec.run)

		// A kill caused by cancellation is reported as the cancellation, and it
		// is not mistaken for the leniency Remove grants a missing unit.
		err := scheduler.Remove(ctx, "llamar-a-mama")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Remove interrupted by cancellation = %v, want it to wrap context.Canceled", err)
		}
	})
}

// TestFakeRecordsCalls checks the fake used by the next task actually records
// what it was told and keeps the state a caller can observe.
func TestFakeRecordsCalls(t *testing.T) {
	fake := NewFake()
	at := fixedAt()

	if err := fake.Create(context.Background(), "llamar-a-mama", at); err != nil {
		t.Fatalf("fake Create: %v", err)
	}
	if err := fake.Create(context.Background(), "regar-las-plantas", at.Add(24*time.Hour)); err != nil {
		t.Fatalf("fake Create: %v", err)
	}
	if err := fake.Remove(context.Background(), "llamar-a-mama"); err != nil {
		t.Fatalf("fake Remove: %v", err)
	}
	slugs, err := fake.List(context.Background())
	if err != nil {
		t.Fatalf("fake List: %v", err)
	}

	created := fake.Created()
	if len(created) != 2 {
		t.Fatalf("recorded %d Create calls, want 2", len(created))
	}
	if created[0].Slug != "llamar-a-mama" || !created[0].At.Equal(at) {
		t.Errorf("first Create = %+v, want llamar-a-mama at %v", created[0], at)
	}
	if created[1].Slug != "regar-las-plantas" || !created[1].At.Equal(at.Add(24*time.Hour)) {
		t.Errorf("second Create = %+v, want regar-las-plantas at %v", created[1], at.Add(24*time.Hour))
	}
	if want := []string{"llamar-a-mama"}; !slices.Equal(fake.Removed(), want) {
		t.Errorf("recorded Remove = %q, want %q", fake.Removed(), want)
	}
	if fake.Lists() != 1 {
		t.Errorf("List calls = %d, want 1", fake.Lists())
	}
	if want := []string{"regar-las-plantas"}; !slices.Equal(slugs, want) {
		t.Errorf("scheduled slugs = %q, want %q", slugs, want)
	}
}

// TestFakeCanBeToldToFail checks the fake can fail one operation while the rest
// keep working, and that a failed call is still recorded.
func TestFakeCanBeToldToFail(t *testing.T) {
	boom := errors.New("boom")
	var fake Fake
	fake.FailOp(OpRemove, boom)

	if err := fake.Remove(context.Background(), "llamar-a-mama"); !errors.Is(err, boom) {
		t.Errorf("Remove told to fail = %v, want %v", err, boom)
	}
	if want := []string{"llamar-a-mama"}; !slices.Equal(fake.Removed(), want) {
		t.Errorf("recorded Remove = %q, want %q", fake.Removed(), want)
	}
	if err := fake.Create(context.Background(), "regar-las-plantas", fixedAt()); err != nil {
		t.Errorf("Create while only Remove fails = %v, want nil", err)
	}

	fake.Fail(boom)
	if _, err := fake.List(context.Background()); !errors.Is(err, boom) {
		t.Errorf("List after Fail = %v, want %v", err, boom)
	}

	// A preset list is what List reports until a create or remove changes it.
	fake = Fake{}
	fake.SetUnits("regar-las-plantas")
	slugs, err := fake.List(context.Background())
	if err != nil {
		t.Fatalf("List on a preset fake: %v", err)
	}
	if want := []string{"regar-las-plantas"}; !slices.Equal(slugs, want) {
		t.Errorf("preset slugs = %q, want %q", slugs, want)
	}
}

// TestWithCLocaleReplacesExistingLocale checks the runner does not leave a
// duplicated locale variable behind: systemctl's "unit not loaded" message is
// matched in English, so the C locale has to win over the session's.
func TestWithCLocaleReplacesExistingLocale(t *testing.T) {
	env := withCLocale([]string{
		"PATH=/usr/bin",
		"LANG=es_ES.UTF-8",
		"LC_ALL=es_ES.UTF-8",
		"LC_MESSAGES=es_ES.UTF-8",
		"HOME=/home/owner",
	})

	count := map[string]int{}
	for _, entry := range env {
		count[entry]++
	}
	for _, want := range []string{"LC_ALL=C", "LANG=C"} {
		if count[want] != 1 {
			t.Errorf("%q appears %d times in %q, want exactly 1", want, count[want], env)
		}
	}
	for _, unwanted := range []string{"LANG=es_ES.UTF-8", "LC_ALL=es_ES.UTF-8", "LC_MESSAGES=es_ES.UTF-8"} {
		if count[unwanted] != 0 {
			t.Errorf("%q survived in %q, want it replaced", unwanted, env)
		}
	}
	for _, kept := range []string{"PATH=/usr/bin", "HOME=/home/owner"} {
		if count[kept] != 1 {
			t.Errorf("%q is missing from %q, want it kept", kept, env)
		}
	}
}
