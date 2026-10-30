package main

// The remind slice of Fase 3: `alter remind sync` reconciles the vault with the
// machine's timers, `alter remind fire <slug>` is what a timer runs when it goes
// off, and `alter notify <text>` is the only way a reminder reaches the owner's
// chat. The markdown file is the source of truth; the timers are derived from it
// and rebuilt from it, so a machine restart loses nothing.
//
// Like `alter vault check`, these are subcommands and not a second runtime: they
// read or write the vault, talk to systemd and exit. None of them starts the
// bridge.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/config"
	"github.com/verdu/alter/internal/reminder"
	"github.com/verdu/alter/internal/vault"
)

const (
	// remindersFolder is the vault folder reminders live in, and remindersExt the
	// only file shape the reconciler considers.
	remindersFolder = "reminders"
	remindersExt    = ".md"

	// openStatus is the one status the reconciler arms timers for: a done or
	// cancelled reminder keeps its file and loses its timer.
	openStatus = "open"

	// remindStampLayout is the shape of an instant in the command's output: the
	// same RFC3339 with a numeric offset the vault contract writes.
	remindStampLayout = "2006-01-02T15:04:05-07:00"

	// reminderAgentTimeout bounds one wake-up. The agent only has to redact one
	// short line and send it, so a longer ceiling would only delay the fallback
	// notice that keeps the reminder from being lost.
	reminderAgentTimeout = 2 * time.Minute

	// reminderBodyLimit bounds how much of a reminder body reaches the agent: a
	// long note is context, not the reminder itself.
	reminderBodyLimit = 2000

	// remindErrorLimit bounds an error text taken from a child process, so one
	// noisy stderr never fills the journal.
	remindErrorLimit = 300

	// remindEnvFileVar names the variable that overrides where the dotenv file is
	// read from. Unset, the file is the one the service unit loads: <WorkDir>/.env.
	remindEnvFileVar = "ALTER_ENV_FILE"
)

// Exit codes of the remind and notify subcommands. They match `alter vault check`:
// 0 clean, 1 something needs attention, 2 the command itself was called wrong.
const (
	remindExitOK      = 0
	remindExitFailed  = 1
	remindExitBadArgs = 2
)

// remindUsage is the help text of the subcommands of this slice.
const remindUsage = `uso: alter remind sync [--dry-run]
     alter remind fire <slug>
     alter notify <texto>

sync      reconcilia el vault con los timers de systemd: crea los que faltan,
          borra los huérfanos y dispara los recordatorios vencidos sin disparar
          (la puesta al día tras un reinicio). --dry-run imprime el mismo plan sin
          tocar systemd ni el vault.
fire      dispara un recordatorio: lo marca y despierta al agente para que avise.
          Es idempotente; si no está vencido o no existe, sale sin hacer nada. Lo
          llama el timer.
notify    envía un texto al chat del Dueño usando TELEGRAM_TOKEN y
          TELEGRAM_CHAT_ID. Nunca imprime el token.

fire y notify leen antes el fichero de entorno (ALTER_ENV_FILE, o <WorkDir>/.env)
para el token: un timer arranca sin el entorno del servicio. Si el fichero no
está, se dice en una línea y se usan las variables del proceso.

Sin ALTER_VAULT_DIR, el vault es $HOME/vault. Imprime una línea por acción
(creado, borrado, disparado, sin cambios) y una de resumen. Sale con 0 si todo
quedó reconciliado, 1 si algún recordatorio no es válido o alguna acción falló, y
2 si la llamada no es correcta.
`

// reRemindSlug is the alphabet a slug may use: the same one the vault contract
// gives file names.
var reRemindSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// runRemindCommand dispatches `alter remind` and `alter notify`. handled is false
// when the arguments are neither, so main starts the bridge exactly as before.
func runRemindCommand(args []string, stdout, stderr io.Writer) (handled bool, code int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "remind":
		return true, runRemind(args[1:], stdout, stderr)
	case "notify":
		return true, runNotify(args[1:], stdout, stderr)
	}
	return false, 0
}

// runRemind dispatches the remind subcommands. The runner is composed only after
// the arguments are known to be valid, so a typo never reads the configuration
// nor touches the vault.
func runRemind(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpFlag(args[0]) {
		fmt.Fprint(stdout, remindUsage)
		return remindExitOK
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, remindUsage)
		return remindExitBadArgs
	}

	switch args[0] {
	case "sync":
		dryRun := false
		for _, arg := range args[1:] {
			if arg == "--dry-run" {
				dryRun = true
				continue
			}
			fmt.Fprintf(stderr, "remind sync: opción desconocida %q\n\n%s", arg, remindUsage)
			return remindExitBadArgs
		}
		return newRemindRunner(config.Load(), stdout, stderr).sync(context.Background(), dryRun)

	case "fire":
		if len(args) != 2 {
			fmt.Fprintf(stderr, "remind fire: hace falta exactamente un slug\n\n%s", remindUsage)
			return remindExitBadArgs
		}
		return newRemindRunner(config.Load(), stdout, stderr).fire(context.Background(), args[1])
	}

	fmt.Fprintf(stderr, "remind: subcomando desconocido %q\n\n%s", args[0], remindUsage)
	return remindExitBadArgs
}

// runNotify sends one text to the owner's chat. It is the transport the agent uses
// to speak when no bridge turn is running, so it is deliberately plain: one text
// in, one message out.
func runNotify(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && isHelpFlag(args[0]) {
		fmt.Fprint(stdout, remindUsage)
		return remindExitOK
	}
	text := strings.TrimSpace(strings.Join(args, " "))
	if text == "" {
		fmt.Fprintf(stderr, "notify: falta el texto del aviso\n\n%s", remindUsage)
		return remindExitBadArgs
	}
	if err := newRemindRunner(config.Load(), stdout, stderr).sendNotice(context.Background(), text); err != nil {
		// The error never carries the token: the client redacts it and every message
		// built here names the variable, not its value.
		fmt.Fprintf(stderr, "notify: %v\n", err)
		return remindExitFailed
	}
	fmt.Fprintln(stdout, "notify: enviado al chat del Dueño")
	return remindExitOK
}

// isHelpFlag reports whether arg asks for the help text.
func isHelpFlag(arg string) bool { return arg == "-h" || arg == "--help" }

// remindRunner executes the remind and notify subcommands for one vault. Every
// external effect goes through a field: the machine's timers, the clock, the agent
// and the chat. That is what lets a test drive the whole slice with a fake
// scheduler and a fake agent and never touch systemd, Pi or Telegram.
type remindRunner struct {
	vaultDir string
	// binary is the executable the timers run: `<binary> remind fire <slug>`.
	binary    string
	scheduler reminder.Scheduler
	stdout    io.Writer
	stderr    io.Writer
	now       func() time.Time
	// wake runs the agent for one reminder and returns when it is done.
	wake func(ctx context.Context, prompt string) error
	// notify delivers one plain text to the owner's chat.
	notify func(ctx context.Context, text string) error
	// envFile is the dotenv file read before the credentials are looked for, and
	// loadEnv is what reads it. A timer starts with no environment, so the file is
	// the only place the token can come from.
	envFile string
	loadEnv func(path string) error
}

// newRemindRunner composes the runner from the configuration: the real clock, the
// real systemd scheduler, the real Pi wake-up and the real Bot API.
func newRemindRunner(cfg config.Config, stdout, stderr io.Writer) *remindRunner {
	binary := selfBinary()
	envFile := remindEnvFile(cfg)
	return &remindRunner{
		vaultDir: cfg.VaultDir,
		binary:   binary,
		// The transient unit does not inherit this environment, so the PATH and the
		// dotenv file the firing needs travel with it.
		scheduler: reminder.NewSystemd(binary, os.Getenv("PATH"), envFile),
		stdout:    stdout,
		stderr:    stderr,
		now:       time.Now,
		wake:      piReminderRunner(cfg),
		notify:    telegramNotifier(cfg),
		envFile:   envFile,
		loadEnv:   config.LoadDotEnv,
	}
}

// remindEnvFile returns the dotenv file the firing reads: the explicit override in
// ALTER_ENV_FILE, or <WorkDir>/.env, which is the file the service unit loads.
func remindEnvFile(cfg config.Config) string {
	if path := strings.TrimSpace(os.Getenv(remindEnvFileVar)); path != "" {
		return path
	}
	return filepath.Join(cfg.WorkDir, ".env")
}

// loadCredentials reads the dotenv file before anything looks for the Bot API
// credentials. A missing file is one line, never a failure: the variables may
// already be in the environment, and a reminder must not be lost over it.
func (r *remindRunner) loadCredentials() {
	if r.envFile == "" || r.loadEnv == nil {
		return
	}
	if info, err := os.Stat(r.envFile); err != nil || !info.Mode().IsRegular() {
		r.line(fmt.Sprintf("remind: no hay fichero de entorno en %s; se usan las variables del proceso", r.envFile))
		return
	}
	if err := r.loadEnv(r.envFile); err != nil {
		fmt.Fprintf(r.stderr, "remind: no se pudo leer %s: %v\n", r.envFile, err)
	}
}

// selfBinary is the executable a timer starts: the running binary, so a timer
// always wakes the same build that created it. The fallback keeps the runner
// usable when the path cannot be resolved.
func selfBinary() string {
	if path, err := os.Executable(); err == nil && path != "" {
		return path
	}
	return "alter"
}

// sendNotice reads the dotenv file and then delivers one text to the owner. It is
// the only path the notify subcommand uses, so the token is looked for where a
// timer's environment can actually have it: the file.
func (r *remindRunner) sendNotice(ctx context.Context, text string) error {
	r.loadCredentials()
	return r.notify(ctx, text)
}

// line writes one line of the command's own output.
func (r *remindRunner) line(text string) { fmt.Fprintln(r.stdout, text) }

// sync reconciles the vault with the machine's timers and returns the exit code.
// One pass, in this order: report and drop the invalid entries, fire what came due
// while nothing was watching, then make every remaining timer match its reminder,
// and finally remove the timers that no reminder claims.
func (r *remindRunner) sync(ctx context.Context, dryRun bool) int {
	now := r.now()
	folder := filepath.Join(r.vaultDir, remindersFolder)
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		// The vault is created lazily, so a fresh one has no reminders folder. There is
		// nothing to reconcile, and asking systemd would make the machine's tools a
		// dependency of a vault that does not exist yet.
		r.line("remind sync: sin cambios")
		return remindExitOK
	}

	report, err := vault.ScanReminders(r.vaultDir, now)
	if err != nil {
		fmt.Fprintf(r.stderr, "remind sync: %v\n", err)
		return remindExitFailed
	}
	slugs, err := reminderSlugs(folder)
	if err != nil {
		fmt.Fprintf(r.stderr, "remind sync: %v\n", err)
		return remindExitFailed
	}
	timers, err := r.scheduler.List(ctx)
	if err != nil {
		fmt.Fprintf(r.stderr, "remind sync: no se pudo leer los timers: %v\n", err)
		return remindExitFailed
	}
	scheduled := make(map[string]bool, len(timers))
	for _, slug := range timers {
		scheduled[slug] = true
	}
	invalid := make(map[string][]vault.Problem, len(report.Invalid))
	for _, entry := range report.Invalid {
		invalid[entry.Path] = entry.Problems
	}
	due := make(map[string]vault.Due, len(report.Due))
	for _, entry := range report.Due {
		due[entry.Path] = entry
	}

	var totals remindTotals
	failed := false

	for _, slug := range slugs {
		path := filepath.Join(folder, slug+remindersExt)
		// The timer is consumed here, so whatever is left in scheduled afterwards is
		// an orphan.
		exists := scheduled[slug]
		delete(scheduled, slug)

		if problems, ok := invalid[path]; ok {
			totals.invalid++
			for _, problem := range problems {
				fmt.Fprintln(r.stderr, problem.String())
			}
			// An invalid reminder is neither fired nor armed: its timer could only wake a
			// `fire` that refuses. It goes, and the owner fixes the file and runs sync.
			result := r.applyTimer(ctx, slug, time.Time{}, exists, dryRun)
			totals.add(result)
			failed = failed || !result.ok
			continue
		}

		if fired, ok := due[path]; ok {
			totals.fired++
			if dryRun {
				// The plan says the reminder would fire and its spent timer would go. Where
				// the recurrence lands is not part of it: advancing one rewrites the vault,
				// and a dry run writes nothing.
				r.line(fmt.Sprintf("disparado %s", slug))
				if exists {
					totals.removed++
					r.line(fmt.Sprintf("borrado %s", slug))
				}
				continue
			}
			result, code := r.fireDue(ctx, slug, path, fired.Entry, now, exists)
			totals.add(result)
			failed = failed || code != remindExitOK
			continue
		}

		// Not due: the reminder either still has a future occurrence to arm, or it is
		// spent (a fired one-off, a done task, a cancelled one) and needs no timer.
		entry, problems := vault.ParseFile(path)
		if len(problems) > 0 {
			// The scan accepted this file a moment ago, so this only happens when it
			// changed under us. It is reported, never scheduled.
			totals.invalid++
			for _, problem := range problems {
				fmt.Fprintln(r.stderr, problem.String())
			}
			result := r.applyTimer(ctx, slug, time.Time{}, exists, dryRun)
			totals.add(result)
			failed = failed || !result.ok
			continue
		}
		target, problem := reminderTimerTarget(entry, now)
		if problem != "" {
			totals.invalid++
			fmt.Fprintf(r.stderr, "remind sync: %s: %s\n", path, problem)
		}
		result := r.applyTimer(ctx, slug, target, exists, dryRun)
		totals.add(result)
		failed = failed || !result.ok
	}

	// Whatever the machine still schedules for a reminder that has no file is an
	// orphan: the markdown is the truth and sync never creates one.
	orphans := make([]string, 0, len(scheduled))
	for slug := range scheduled {
		orphans = append(orphans, slug)
	}
	sort.Strings(orphans)
	for _, slug := range orphans {
		result := r.applyTimer(ctx, slug, time.Time{}, true, dryRun)
		totals.add(result)
		failed = failed || !result.ok
	}

	if totals.empty() {
		r.line("remind sync: sin cambios")
	} else {
		r.line("remind sync: " + totals.String())
	}
	if failed || totals.invalid > 0 {
		return remindExitFailed
	}
	return remindExitOK
}

// fire runs one reminder, idempotently. A timer may go off a second after a sync
// already handled the same reminder, and it may go off for a reminder that was
// deleted, cancelled or rescheduled in between. None of that is an error, so the
// command says what it found and exits 0.
func (r *remindRunner) fire(ctx context.Context, slug string) int {
	if !reRemindSlug.MatchString(slug) {
		fmt.Fprintf(r.stderr, "remind fire: %q no es un slug de recordatorio\n", slug)
		return remindExitBadArgs
	}
	path := filepath.Join(r.vaultDir, remindersFolder, slug+remindersExt)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A timer outlives the file it was created for; sync removes it, and until
			// then a firing with nothing to fire is not a failure.
			r.line(fmt.Sprintf("remind fire: %s ya no existe; no hay nada que disparar", slug))
			return remindExitOK
		}
		fmt.Fprintf(r.stderr, "remind fire: %v\n", err)
		return remindExitFailed
	}

	entry, problems := vault.ParseFile(path)
	if len(problems) > 0 || entry.Type != vault.EntryReminder {
		for _, problem := range problems {
			fmt.Fprintln(r.stderr, problem.String())
		}
		// An invalid reminder is never fired: the report says what to fix, and the
		// command stays quiet for the timer that woke it.
		r.line(fmt.Sprintf("remind fire: %s no se dispara: no cumple el contrato del vault", slug))
		return remindExitOK
	}

	now := r.now()
	if !vault.DueAt(entry, now) {
		r.line(fmt.Sprintf("remind fire: %s no está vencido; no hay nada que disparar", slug))
		return remindExitOK
	}

	timers, listErr := r.scheduler.List(ctx)
	if listErr != nil {
		// The scheduling state cannot be read, but the owner's notice matters more than
		// the bookkeeping: the reminder is fired anyway, and the old timer is removed
		// blindly (removing a unit that is not there is not an error).
		fmt.Fprintf(r.stderr, "remind fire: no se pudo leer los timers: %v\n", listErr)
	}
	exists := listErr != nil || containsSlug(timers, slug)

	_, code := r.fireDue(ctx, slug, path, entry, now, exists)
	if listErr != nil && code == remindExitOK {
		code = remindExitFailed
	}
	return code
}

// fireDue marks the reminder as fired and only then wakes the agent, in that order:
// a failure after the mark loses that notice (it is logged) instead of repeating it
// on every timer tick. The timer is reconciled before the agent runs, so the next
// occurrence is scheduled even when the agent takes minutes or never answers.
func (r *remindRunner) fireDue(ctx context.Context, slug, path string, entry vault.Entry, now time.Time, exists bool) (remindTimerResult, int) {
	next, err := markReminderFired(path, entry, now)
	if err != nil {
		// Without the mark the reminder would fire again on the next sync, so the agent
		// is not woken: the report is the only honest outcome.
		fmt.Fprintf(r.stderr, "remind fire: %s: %v\n", slug, err)
		return remindTimerResult{}, remindExitFailed
	}
	r.line(fmt.Sprintf("disparado %s", slug))

	result := r.applyTimer(ctx, slug, next, exists, false)
	code := remindExitOK
	if !result.ok {
		code = remindExitFailed
	}

	// The dotenv file is read before the agent runs, so the wake-up and the notice
	// both see the credentials: a timer starts with almost no environment.
	r.loadCredentials()
	if err := r.wake(ctx, reminderPrompt(r.binary, entry)); err != nil {
		// The agent is the voice of the reminder, so its failure must not swallow the
		// reminder: the title goes to the chat as the last resort.
		fmt.Fprintf(r.stderr, "remind fire: %s: el agente falló: %v\n", slug, err)
		if notifyErr := r.notify(ctx, entry.Title); notifyErr != nil {
			fmt.Fprintf(r.stderr, "remind fire: %s: el aviso de reserva tampoco salió: %v\n", slug, notifyErr)
			code = remindExitFailed
		}
	}
	return result, code
}

// markReminderFired writes the new state of a fired reminder and returns the
// instant its timer must point at, or the zero time when it needs no timer again. A
// one-off records last_fired; a recurring one moves remind_at forward and never
// ends, which is also what makes a second call in the same second a no-op.
func markReminderFired(path string, entry vault.Entry, now time.Time) (time.Time, error) {
	if entry.Recurrence != "" {
		return vault.AdvanceRecurrence(path, now)
	}
	if err := vault.MarkFired(path, now); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, nil
}

// reminderTimerTarget returns the instant the timer of entry must point at, or the
// zero time when the reminder needs no timer. The second result is a problem worth
// reporting: a recurring reminder whose remind_at is already past without being due
// has no next occurrence that can be armed from here, and dropping it silently
// would lose the reminder.
func reminderTimerTarget(entry vault.Entry, now time.Time) (time.Time, string) {
	if entry.Status != openStatus || entry.RemindAt.IsZero() {
		return time.Time{}, ""
	}
	if entry.RemindAt.After(now) {
		return entry.RemindAt, ""
	}
	if entry.Recurrence != "" {
		return time.Time{}, "recurrence con remind_at ya pasado y last_fired posterior: no hay próxima ocurrencia que programar"
	}
	// A one-off that already fired has nothing left to arm.
	return time.Time{}, ""
}

// remindTimerResult is what reconciling one timer did, so the caller can count it
// and know whether an action failed.
type remindTimerResult struct {
	created bool
	removed bool
	ok      bool
}

// applyTimer makes the machine's timer of slug be what target says: gone when
// target is zero, pointing at target otherwise. An existing timer is always removed
// first, because Scheduler.List reports the unit names and never the calendar, so
// the calendar of an armed timer cannot be verified from here: the vault wins, and
// a remind_at that was edited takes effect on the next sync instead of waiting for
// a machine restart. exists is what the machine reported, so a timer that is
// already gone is not removed twice.
func (r *remindRunner) applyTimer(ctx context.Context, slug string, target time.Time, exists, dryRun bool) remindTimerResult {
	result := remindTimerResult{ok: true}
	if exists {
		result.removed = true
		r.line(fmt.Sprintf("borrado %s", slug))
		if !dryRun {
			if err := r.scheduler.Remove(ctx, slug); err != nil {
				fmt.Fprintf(r.stderr, "remind sync: %s: no se pudo borrar el timer: %v\n", slug, err)
				result.ok = false
			}
		}
	}
	if target.IsZero() {
		return result
	}
	result.created = true
	r.line(fmt.Sprintf("creado %s (%s)", slug, target.In(time.Local).Format(remindStampLayout)))
	if !dryRun {
		if err := r.scheduler.Create(ctx, slug, target); err != nil {
			fmt.Fprintf(r.stderr, "remind sync: %s: no se pudo crear el timer: %v\n", slug, err)
			result.ok = false
		}
	}
	return result
}

// remindTotals counts what one reconciliation did, for the closing line.
type remindTotals struct {
	created int
	removed int
	fired   int
	invalid int
}

// add counts the timers one reconciliation step touched.
func (t *remindTotals) add(result remindTimerResult) {
	if result.created {
		t.created++
	}
	if result.removed {
		t.removed++
	}
}

// empty reports whether the reconciliation had nothing to do at all.
func (t remindTotals) empty() bool { return t.created+t.removed+t.fired+t.invalid == 0 }

// String renders the closing counts as "1 creado, 2 disparados".
func (t remindTotals) String() string {
	parts := make([]string, 0, 4)
	if t.created > 0 {
		parts = append(parts, vaultCount(t.created, "creado", "creados"))
	}
	if t.removed > 0 {
		parts = append(parts, vaultCount(t.removed, "borrado", "borrados"))
	}
	if t.fired > 0 {
		parts = append(parts, vaultCount(t.fired, "disparado", "disparados"))
	}
	if t.invalid > 0 {
		parts = append(parts, vaultCount(t.invalid, "inválido", "inválidos"))
	}
	return strings.Join(parts, ", ")
}

// reminderSlugs lists the slugs of the reminder files in folder, in the order the
// folder lists them, so a sync always reports the same order.
func reminderSlugs(folder string) ([]string, error) {
	items, err := os.ReadDir(folder)
	if err != nil {
		return nil, fmt.Errorf("no se puede leer la carpeta %q: %w", folder, err)
	}
	slugs := make([]string, 0, len(items))
	for _, item := range items {
		name := item.Name()
		if item.IsDir() || !item.Type().IsRegular() || !strings.HasSuffix(name, remindersExt) {
			continue
		}
		slugs = append(slugs, strings.TrimSuffix(name, remindersExt))
	}
	return slugs, nil
}

// containsSlug reports whether slugs holds slug.
func containsSlug(slugs []string, slug string) bool {
	for _, candidate := range slugs {
		if candidate == slug {
			return true
		}
	}
	return false
}

// reminderPrompt is everything the agent receives when a reminder fires. It is
// written for the model, not for the owner: the only thing that reaches the owner
// is the `alter notify` call, so the prompt says so and forbids anything else.
func reminderPrompt(binary string, entry vault.Entry) string {
	var b strings.Builder
	b.WriteString("Se ha disparado un recordatorio del vault de Alter.\n\n")
	fmt.Fprintf(&b, "Título: %s\n", entry.Title)
	if !entry.RemindAt.IsZero() {
		fmt.Fprintf(&b, "Cuándo: %s\n", entry.RemindAt.In(time.Local).Format(remindStampLayout))
	}
	if body := strings.TrimSpace(entry.Body); body != "" {
		b.WriteString("\nContenido del recordatorio:\n")
		b.WriteString(clipReminderText(body, reminderBodyLimit))
		b.WriteString("\n")
	}
	b.WriteString("\nEscribe al Dueño un único mensaje corto, de una o dos frases y en español, recordándole esto. Nadie lee tu respuesta: lo único que el Dueño recibe es lo que envíes ejecutando\n\n")
	fmt.Fprintf(&b, "    %s notify \"<mensaje>\"\n\n", binary)
	b.WriteString("No hagas nada más: ni preguntas, ni ficheros, ni otros comandos. Si el comando falla, no lo reintentes ni busques otra vía.\n")
	return b.String()
}

// clipReminderText shortens text to limit runes, marking the cut with an ellipsis,
// so a long body never becomes the whole prompt.
func clipReminderText(text string, limit int) string {
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if limit <= 0 || len(runes) <= limit {
		return trimmed
	}
	return string(runes[:limit]) + "…"
}

// piReminderRunner is the real wake-up: one ephemeral `pi -p --no-session` run that
// reads the prompt, redacts a line and sends it with `alter notify`. The session is
// not persisted because a reminder is not a conversation, and the timeout bounds
// how long the fallback notice can be delayed.
func piReminderRunner(cfg config.Config) func(context.Context, string) error {
	return func(ctx context.Context, prompt string) error {
		ctx, cancel := context.WithTimeout(ctx, reminderAgentTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, cfg.PiBin, "-p", "--no-session", prompt)
		// Pi discovers the project skills from its working directory, and a timer
		// starts with whatever directory systemd has: without this the agent would run
		// where the skills and the persona are not.
		cmd.Dir = cfg.WorkDir
		var stderr bytes.Buffer
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("el agente no terminó en %s: %w", reminderAgentTimeout, ctxErr)
		}
		return fmt.Errorf("%w: %s", err, clipReminderText(stderr.String(), remindErrorLimit))
	}
}

// telegramNotifier delivers one text to the owner's chat through the same Bot API
// client the bridge uses. The token comes from the environment and is never
// printed: the client redacts it from transport errors, and the errors built here
// name the variable, not its value. The credentials are read at call time, not
// when the runner was built, because fire and notify load the dotenv file just
// before sending.
func telegramNotifier(cfg config.Config) func(context.Context, string) error {
	return func(ctx context.Context, text string) error {
		token, chatID := noticeCredentials(cfg)
		if token == "" {
			return fmt.Errorf("falta TELEGRAM_TOKEN: sin token no hay forma de avisar al Dueño")
		}
		if chatID == 0 {
			return fmt.Errorf("falta TELEGRAM_CHAT_ID: no se sabe a qué chat avisar")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return fmt.Errorf("el aviso está vacío")
		}
		// SendFormatted is the client's own text path: it escapes the text, keeps every
		// request under Telegram's documented limits and splits what does not fit.
		client := telegram.NewClientWithBase(token, cfg.TelegramAPIBase)
		return client.SendFormatted(ctx, chatID, text)
	}
}

// noticeCredentials resolves what the notice needs. The process environment wins,
// because the dotenv file has just been loaded into it; cfg is the fallback for a
// caller that injected the values directly.
func noticeCredentials(cfg config.Config) (token string, chatID int64) {
	token = os.Getenv("TELEGRAM_TOKEN")
	if token == "" {
		token = cfg.TelegramToken
	}
	chatID = cfg.TelegramChatID
	if raw := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed != 0 {
			chatID = parsed
		}
	}
	return token, chatID
}

// remindLogWriter sends every line a reminder command prints to the log. The
// start-up reconciliation inside the bridge has no terminal, so without this its
// report would go nowhere the owner can read.
type remindLogWriter struct{ logger *log.Logger }

// Write logs the lines in p, dropping the empty ones.
func (w remindLogWriter) Write(p []byte) (int, error) {
	if w.logger != nil {
		for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				w.logger.Printf("remind: %s", line)
			}
		}
	}
	return len(p), nil
}

// startupReminderSync reconciles the vault with the machine's timers once, when the
// bridge starts. It runs off the critical path: it may have to wake the agent for a
// reminder that came due while the machine was off, and that must neither delay nor
// break the bridge. A failure is logged and never returned.
func startupReminderSync(ctx context.Context, cfg config.Config, logger *log.Logger) {
	if ctx.Err() != nil {
		return
	}
	if info, err := os.Stat(filepath.Join(cfg.VaultDir, remindersFolder)); err != nil || !info.IsDir() {
		// No reminders folder means nothing to reconcile: a fresh vault must not make
		// systemd a start-up dependency, and must not write a line at every start.
		return
	}
	out := remindLogWriter{logger: logger}
	if code := newRemindRunner(cfg, out, out).sync(ctx, false); code != remindExitOK {
		if logger != nil {
			logger.Printf("remind: la reconciliación de arranque terminó con errores")
		}
	}
}
