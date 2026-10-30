package main

// Tests of the remind slice: `alter remind sync`, `alter remind fire` and
// `alter notify`. Every test runs against a temporary vault and a Fake scheduler,
// so nothing here touches systemd, Pi or Telegram.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/verdu/alter/internal/config"
	"github.com/verdu/alter/internal/reminder"
	"github.com/verdu/alter/internal/vault"
)

// remindTestBinary is the executable the fake runner pretends the timers call.
const remindTestBinary = "/opt/alter/bin/alter"

// remindNow is the fixed instant every reminder command test runs at.
func remindNow() time.Time {
	return time.Date(2026, 11, 3, 12, 0, 0, 0, time.FixedZone("CET", 3600))
}

// reminderSpec describes one reminder document. Each test varies only the fields
// it is about; the rest keeps the contract's field order and a valid value. The
// slug is also the id and the file name, as the contract requires.
type reminderSpec struct {
	slug       string
	status     string
	title      string
	remindAt   string
	recurrence string
	lastFired  string
}

// reminderDoc renders one valid reminder document.
func reminderDoc(spec reminderSpec) string {
	if spec.status == "" {
		spec.status = openStatus
	}
	if spec.title == "" {
		spec.title = "Llamar a mamá"
	}
	if spec.remindAt == "" {
		spec.remindAt = "2026-11-03T18:00:00+01:00"
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", spec.slug)
	b.WriteString("type: reminder\n")
	fmt.Fprintf(&b, "title: %q\n", spec.title)
	fmt.Fprintf(&b, "status: %s\n", spec.status)
	b.WriteString("priority: normal\n")
	// The id's date and created's date must agree, so one field feeds both.
	fmt.Fprintf(&b, "created: %sT09:15:00+01:00\n", spec.slug[:10])
	fmt.Fprintf(&b, "remind_at: %s\n", spec.remindAt)
	if spec.recurrence != "" {
		fmt.Fprintf(&b, "recurrence: %s\n", spec.recurrence)
	}
	if spec.lastFired != "" {
		fmt.Fprintf(&b, "last_fired: %s\n", spec.lastFired)
	}
	b.WriteString("tags: \"familia\"\n")
	b.WriteString("---\n\n# Recordatorio\n\nPreguntar por la revisión del médico.\n")
	return b.String()
}

// mustRemindTime parses a stamp of the contract's shape and fails the test when it
// is not one, so a fixture typo never hides behind a zero time.
func mustRemindTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("instante de prueba %q: %v", value, err)
	}
	return parsed
}

// remindHarness is one runner with every external effect faked: the machine's
// timers, the clock, the agent, the chat and both output streams.
type remindHarness struct {
	t       *testing.T
	root    string
	runner  *remindRunner
	fake    *reminder.Fake
	out     strings.Builder
	err     strings.Builder
	prompts []string
	notices []string
}

// newRemindHarness builds a runner over a temporary vault.
func newRemindHarness(t *testing.T) *remindHarness {
	t.Helper()
	h := &remindHarness{t: t, root: t.TempDir(), fake: reminder.NewFake()}
	h.runner = &remindRunner{
		vaultDir:  h.root,
		binary:    remindTestBinary,
		scheduler: h.fake,
		stdout:    &h.out,
		stderr:    &h.err,
		now:       remindNow,
		wake: func(_ context.Context, prompt string) error {
			h.prompts = append(h.prompts, prompt)
			return nil
		},
		notify: func(_ context.Context, text string) error {
			h.notices = append(h.notices, text)
			return nil
		},
	}
	return h
}

// path returns where the reminder of slug lives.
func (h *remindHarness) path(slug string) string {
	return filepath.Join(h.root, remindersFolder, slug+remindersExt)
}

// write creates one reminder file in the harness vault.
func (h *remindHarness) write(spec reminderSpec) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Join(h.root, remindersFolder), 0o755); err != nil {
		h.t.Fatalf("crear la carpeta de recordatorios: %v", err)
	}
	if err := os.WriteFile(h.path(spec.slug), []byte(reminderDoc(spec)), 0o644); err != nil {
		h.t.Fatalf("escribir %s: %v", spec.slug, err)
	}
}

// read returns the bytes of one reminder file.
func (h *remindHarness) read(slug string) string {
	h.t.Helper()
	data, err := os.ReadFile(h.path(slug))
	if err != nil {
		h.t.Fatalf("leer %s: %v", slug, err)
	}
	return string(data)
}

// TestRemindSyncArmsFutureAndFiresPast pins the two halves of one pass: a reminder
// that is still ahead gets a timer and is left untouched, and a reminder that came
// due while nothing was watching is fired on the spot instead of being armed.
func TestRemindSyncArmsFutureAndFiresPast(t *testing.T) {
	h := newRemindHarness(t)
	future := reminderSpec{slug: "2026-11-03-llamar-a-mama", remindAt: "2026-11-03T18:00:00+01:00"}
	past := reminderSpec{
		slug:      "2026-11-03-renovar-dni",
		title:     "Renovar el DNI",
		remindAt:  "2026-11-03T09:00:00+01:00",
		lastFired: "2026-11-02T09:00:00+01:00",
	}
	h.write(future)
	h.write(past)
	futureBefore := h.read(future.slug)
	pastBefore := h.read(past.slug)

	if code := h.runner.sync(context.Background(), false); code != remindExitOK {
		t.Fatalf("sync = %d, want %d (stdout=%q stderr=%q)", code, remindExitOK, h.out.String(), h.err.String())
	}

	created := h.fake.Created()
	if len(created) != 1 {
		t.Fatalf("creados = %v, want only the future reminder armed", created)
	}
	if created[0].Slug != future.slug {
		t.Errorf("creado %q, want %q: the past reminder is fired, not armed", created[0].Slug, future.slug)
	}
	if want := mustRemindTime(t, future.remindAt); !created[0].At.Equal(want) {
		t.Errorf("timer armado para %v, want %v", created[0].At, want)
	}
	if got := h.read(future.slug); got != futureBefore {
		t.Errorf("sync rewrote a reminder it only armed:\n%q\nwant:\n%q", got, futureBefore)
	}
	fired := h.read(past.slug)
	if fired == pastBefore {
		t.Fatal("the due reminder kept its last_fired: sync never fired it")
	}
	if !strings.Contains(fired, "last_fired: 2026-11-03T12:00:00+01:00") {
		t.Errorf("last_fired no quedó marcado con el instante del disparo:\n%s", fired)
	}
	if len(h.prompts) != 1 || !strings.Contains(h.prompts[0], "Renovar el DNI") {
		t.Errorf("prompts al agente = %q, want the fired reminder handed to the agent", h.prompts)
	}
	if len(h.notices) != 0 {
		t.Errorf("avisos de reserva = %q, want none when the agent answers", h.notices)
	}
	out := h.out.String()
	if !strings.Contains(out, "creado "+future.slug) {
		t.Errorf("salida %q sin la línea de creado", out)
	}
	if !strings.Contains(out, "disparado "+past.slug) {
		t.Errorf("salida %q sin la línea de disparado", out)
	}
}

// TestRemindSyncRemovesOrphanAndCancelledTimers pins the deletion half: a timer
// whose file is gone and a timer whose reminder was cancelled are both removed, and
// neither is recreated.
func TestRemindSyncRemovesOrphanAndCancelledTimers(t *testing.T) {
	h := newRemindHarness(t)
	const orphan = "2026-11-01-ya-no-esta"
	const cancelled = "2026-11-02-cancelado"
	h.fake.SetUnits(orphan, cancelled)
	h.write(reminderSpec{slug: cancelled, status: "cancelled", remindAt: "2026-11-03T18:00:00+01:00"})

	if code := h.runner.sync(context.Background(), false); code != remindExitOK {
		t.Fatalf("sync = %d, want %d (stdout=%q stderr=%q)", code, remindExitOK, h.out.String(), h.err.String())
	}

	removed := h.fake.Removed()
	if len(removed) != 2 || !containsSlug(removed, orphan) || !containsSlug(removed, cancelled) {
		t.Errorf("borrados = %v, want the orphan and the cancelled one", removed)
	}
	if created := h.fake.Created(); len(created) != 0 {
		t.Errorf("creados = %v, want nothing recreated", created)
	}
	if out := h.out.String(); strings.Contains(out, "creado") {
		t.Errorf("salida %q anuncia un timer que no debía crearse", out)
	}
	if got := h.fake.Removed(); len(got) != 2 {
		t.Fatalf("borrados = %v, want exactly two", got)
	}
}

// TestRemindSyncDryRunTouchesNothingAndPrintsTheSamePlan pins --dry-run: no timer
// is created or removed, no file changes, and the lines it prints are the ones a
// real run prints a moment later.
func TestRemindSyncDryRunTouchesNothingAndPrintsTheSamePlan(t *testing.T) {
	h := newRemindHarness(t)
	const armed = "2026-11-03-llamar-a-mama"
	const cancelled = "2026-11-02-cancelado"
	const orphan = "2026-11-01-ya-no-esta"
	h.fake.SetUnits(armed, cancelled, orphan)
	h.write(reminderSpec{slug: armed, remindAt: "2026-11-03T18:00:00+01:00"})
	h.write(reminderSpec{slug: cancelled, status: "cancelled", remindAt: "2026-11-03T18:00:00+01:00"})
	before := h.read(armed)

	if code := h.runner.sync(context.Background(), true); code != remindExitOK {
		t.Fatalf("sync --dry-run = %d, want %d (stderr=%q)", code, remindExitOK, h.err.String())
	}
	plan := h.out.String()
	if len(h.fake.Created()) != 0 || len(h.fake.Removed()) != 0 {
		t.Fatalf("dry run tocó systemd: creados=%v borrados=%v", h.fake.Created(), h.fake.Removed())
	}
	if got := h.read(armed); got != before {
		t.Errorf("dry run tocó el vault:\n%q\nwant:\n%q", got, before)
	}
	if !strings.Contains(plan, "creado "+armed) {
		t.Errorf("plan %q sin la creación del timer que falta", plan)
	}

	h.out.Reset()
	if code := h.runner.sync(context.Background(), false); code != remindExitOK {
		t.Fatalf("sync = %d, want %d (stderr=%q)", code, remindExitOK, h.err.String())
	}
	if real := h.out.String(); real != plan {
		t.Errorf("el plan del dry run no coincide con lo que hizo el sync real:\ndry:\n%q\nreal:\n%q", plan, real)
	}
}

// TestRemindFireMarksBeforeWakingTheAgent pins the order the plan insists on: the
// state is written first, so a failure after it loses that one notice instead of
// repeating it on every timer tick.
func TestRemindFireMarksBeforeWakingTheAgent(t *testing.T) {
	h := newRemindHarness(t)
	due := reminderSpec{
		slug:      "2026-11-03-llamar-a-mama",
		title:     "Llamar a mamá",
		remindAt:  "2026-11-03T09:00:00+01:00",
		lastFired: "2026-11-02T09:00:00+01:00",
	}
	h.write(due)

	var stateAtWake string
	h.runner.wake = func(_ context.Context, prompt string) error {
		stateAtWake = h.read(due.slug)
		h.prompts = append(h.prompts, prompt)
		return nil
	}

	if code := h.runner.fire(context.Background(), due.slug); code != remindExitOK {
		t.Fatalf("fire = %d, want %d (stdout=%q stderr=%q)", code, remindExitOK, h.out.String(), h.err.String())
	}
	if !strings.Contains(stateAtWake, "last_fired: 2026-11-03T12:00:00+01:00") {
		t.Errorf("el agente se despertó antes de marcar el recordatorio:\n%s", stateAtWake)
	}
	if len(h.prompts) != 1 {
		t.Fatalf("prompts = %d, want one", len(h.prompts))
	}
	prompt := h.prompts[0]
	if !strings.Contains(prompt, "Llamar a mamá") {
		t.Errorf("el prompt %q no lleva el título del recordatorio", prompt)
	}
	if !strings.Contains(prompt, remindTestBinary+" notify") {
		t.Errorf("el prompt %q no dice cómo avisar al Dueño con alter notify", prompt)
	}
	if !strings.Contains(prompt, "una o dos frases") {
		t.Errorf("el prompt %q no pide un mensaje corto", prompt)
	}
}

// TestRemindFireAdvancesRecurringAndIgnoresTheSecondCall pins recurrence and
// idempotence: a recurring reminder moves remind_at instead of finishing, its timer
// is reprogrammed, and a second firing in the same instant does nothing.
func TestRemindFireAdvancesRecurringAndIgnoresTheSecondCall(t *testing.T) {
	h := newRemindHarness(t)
	rec := reminderSpec{
		slug:       "2026-11-03-gimnasio",
		title:      "Gimnasio",
		remindAt:   "2026-11-03T11:00:00+01:00",
		recurrence: "weekly",
	}
	h.write(rec)

	if code := h.runner.fire(context.Background(), rec.slug); code != remindExitOK {
		t.Fatalf("fire = %d, want %d (stdout=%q stderr=%q)", code, remindExitOK, h.out.String(), h.err.String())
	}
	entry, problems := vault.ParseFile(h.path(rec.slug))
	if len(problems) > 0 {
		t.Fatalf("el recordatorio quedó inválido tras dispararse: %v", problems)
	}
	want := mustRemindTime(t, rec.remindAt).AddDate(0, 0, 7)
	if !entry.RemindAt.Equal(want) {
		t.Errorf("remind_at = %v, want %v (siete días después)", entry.RemindAt, want)
	}
	created := h.fake.Created()
	if len(created) != 1 || created[0].Slug != rec.slug {
		t.Fatalf("creados = %v, want the next occurrence armed", created)
	}
	if !created[0].At.Equal(entry.RemindAt) {
		t.Errorf("timer armado para %v, want %v", created[0].At, entry.RemindAt)
	}
	if len(h.prompts) != 1 {
		t.Fatalf("prompts = %d, want one wake-up", len(h.prompts))
	}

	afterFire := h.read(rec.slug)
	h.out.Reset()
	if code := h.runner.fire(context.Background(), rec.slug); code != remindExitOK {
		t.Fatalf("segundo fire = %d, want %d", code, remindExitOK)
	}
	if len(h.prompts) != 1 {
		t.Errorf("el agente se despertó dos veces: %q", h.prompts)
	}
	if got := h.read(rec.slug); got != afterFire {
		t.Errorf("el segundo fire tocó el fichero:\n%q\nwant:\n%q", got, afterFire)
	}
	if len(h.fake.Created()) != 1 {
		t.Errorf("el segundo fire reprogramó el timer: %v", h.fake.Created())
	}
	if out := h.out.String(); !strings.Contains(out, "no está vencido") {
		t.Errorf("salida %q sin la línea que explica que no había nada que disparar", out)
	}
}

// TestRemindFireRefusesInvalidAndNotDue pins the quiet refusals: a timer can go off
// for a reminder that sync already handled or for a file the agent broke, and both
// cases end with a message and nothing touched.
func TestRemindFireRefusesInvalidAndNotDue(t *testing.T) {
	t.Run("invalid reminder", func(t *testing.T) {
		h := newRemindHarness(t)
		// No remind_at: the contract's one required key a reminder cannot live without.
		if err := os.MkdirAll(filepath.Join(h.root, remindersFolder), 0o755); err != nil {
			t.Fatalf("crear la carpeta: %v", err)
		}
		broken := "---\nid: 2026-11-03-llamar-a-mama\ntype: reminder\ntitle: \"Llamar a mamá\"\nstatus: open\ncreated: 2026-11-03T09:15:00+01:00\ntags: \"familia\"\n---\n\n# Llamar a mamá\n"
		const slug = "2026-11-03-llamar-a-mama"
		if err := os.WriteFile(h.path(slug), []byte(broken), 0o644); err != nil {
			t.Fatalf("escribir el recordatorio roto: %v", err)
		}

		if code := h.runner.fire(context.Background(), slug); code != remindExitOK {
			t.Fatalf("fire de un recordatorio inválido = %d, want %d", code, remindExitOK)
		}
		if len(h.prompts) != 0 {
			t.Errorf("el agente se despertó por un recordatorio inválido: %q", h.prompts)
		}
		if len(h.fake.Created()) != 0 || len(h.fake.Removed()) != 0 {
			t.Errorf("un recordatorio inválido tocó los timers: %v %v", h.fake.Created(), h.fake.Removed())
		}
		if got := h.read(slug); got != broken {
			t.Errorf("el fichero inválido cambió:\n%q", got)
		}
		if !strings.Contains(h.err.String(), "remind_at") {
			t.Errorf("stderr %q sin el problema que hay que arreglar", h.err.String())
		}
		if !strings.Contains(h.out.String(), "no cumple el contrato") {
			t.Errorf("salida %q sin la línea que explica el rechazo", h.out.String())
		}
	})

	t.Run("not due", func(t *testing.T) {
		h := newRemindHarness(t)
		future := reminderSpec{slug: "2026-11-03-llamar-a-mama", remindAt: "2026-11-03T18:00:00+01:00"}
		h.write(future)
		before := h.read(future.slug)

		if code := h.runner.fire(context.Background(), future.slug); code != remindExitOK {
			t.Fatalf("fire de uno no vencido = %d, want %d", code, remindExitOK)
		}
		if len(h.prompts) != 0 {
			t.Errorf("el agente se despertó por un recordatorio que no vencía")
		}
		if got := h.read(future.slug); got != before {
			t.Errorf("fire tocó un recordatorio que no vencía:\n%q", got)
		}
		if !strings.Contains(h.out.String(), "no está vencido") {
			t.Errorf("salida %q sin la línea que explica que no había nada que hacer", h.out.String())
		}
	})
}

// TestRemindFireFallsBackToNotifyWhenTheAgentFails pins the guarantee that a
// reminder is never silently lost: when the agent cannot speak, the title still
// reaches the chat and the failure is logged.
func TestRemindFireFallsBackToNotifyWhenTheAgentFails(t *testing.T) {
	h := newRemindHarness(t)
	due := reminderSpec{
		slug:      "2026-11-03-llamar-a-mama",
		title:     "Llamar a mamá",
		remindAt:  "2026-11-03T09:00:00+01:00",
		lastFired: "2026-11-02T09:00:00+01:00",
	}
	h.write(due)
	h.runner.wake = func(context.Context, string) error { return errors.New("pi no arrancó") }

	if code := h.runner.fire(context.Background(), due.slug); code != remindExitOK {
		t.Fatalf("fire = %d, want %d: el aviso de reserva salvó el recordatorio (stderr=%q)", code, remindExitOK, h.err.String())
	}
	if len(h.notices) != 1 || h.notices[0] != due.title {
		t.Errorf("avisos = %q, want the title as the fallback notice", h.notices)
	}
	if !strings.Contains(h.err.String(), "el agente falló") {
		t.Errorf("stderr %q sin el fallo del agente", h.err.String())
	}
	if !strings.Contains(h.read(due.slug), "last_fired: 2026-11-03T12:00:00+01:00") {
		t.Error("el fallback dejó el recordatorio sin marcar: volvería a dispararse")
	}
}

// botAPIRecorder is a Bot API stand-in that records what was sent to it, so a
// test can prove a notice really went out.
type botAPIRecorder struct {
	mu    sync.Mutex
	calls []string
}

// server starts the fake Bot API and returns it with the recorder behind it.
func (r *botAPIRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.calls = append(r.calls, string(body))
		r.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":4242}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// received returns the bodies the fake Bot API was sent.
func (r *botAPIRecorder) received() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// writeDotEnv writes a dotenv file into a fresh temporary directory and returns
// its path.
func writeDotEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("escribir %s: %v", path, err)
	}
	return path
}

// TestRemindFireNotifiesWithTheDotEnvFile pins the second net under the timers: a
// firing that starts without TELEGRAM_TOKEN in its environment still reaches the
// owner, because the token is read from the dotenv file before the notice is sent.
func TestRemindFireNotifiesWithTheDotEnvFile(t *testing.T) {
	const token = "123456:TOKEN-QUE-SOLO-VIVE-EN-EL-FICHERO"
	var bot botAPIRecorder
	srv := bot.server(t)

	h := newRemindHarness(t)
	h.runner.envFile = writeDotEnv(t, "TELEGRAM_TOKEN="+token+"\nTELEGRAM_CHAT_ID=4242\n")
	h.runner.loadEnv = config.LoadDotEnv
	h.runner.notify = telegramNotifier(config.Config{TelegramAPIBase: srv.URL})
	h.runner.wake = func(context.Context, string) error { return errors.New("pi no arrancó") }
	// The transient unit starts with no credentials at all; only the file has them.
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("ALTER_TELEGRAM_API_BASE", srv.URL)

	due := reminderSpec{
		slug:      "2026-11-03-llamar-a-mama",
		title:     "Llamar a mamá",
		remindAt:  "2026-11-03T09:00:00+01:00",
		lastFired: "2026-11-02T09:00:00+01:00",
	}
	h.write(due)

	if code := h.runner.fire(context.Background(), due.slug); code != remindExitOK {
		t.Fatalf("fire = %d, want %d (stderr=%q)", code, remindExitOK, h.err.String())
	}

	received := bot.received()
	if len(received) != 1 {
		t.Fatalf("avisos enviados = %d, want one: el token del fichero no llegó al chat", len(received))
	}
	if !strings.Contains(received[0], "Llamar a mamá") {
		t.Errorf("aviso %q sin el título del recordatorio", received[0])
	}
	printed := h.out.String() + h.err.String()
	if strings.Contains(printed, "falta TELEGRAM_TOKEN") {
		t.Errorf("salida %q: se leyó el token antes que el fichero de entorno", printed)
	}
	if strings.Contains(printed, token) {
		t.Errorf("la salida %q imprime el token", printed)
	}
}

// TestNotifyLoadsTheDotEnvFile pins the same net on the plain `notify` path, with
// the default file location: <WorkDir>/.env.
func TestNotifyLoadsTheDotEnvFile(t *testing.T) {
	const token = "123456:TOKEN-DEL-FICHERO-POR-DEFECTO"
	var bot botAPIRecorder
	srv := bot.server(t)

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("TELEGRAM_TOKEN="+token+"\nTELEGRAM_CHAT_ID=4242\n"), 0o600); err != nil {
		t.Fatalf("escribir el .env del workdir: %v", err)
	}
	t.Setenv("ALTER_BRIDGE_WORKDIR", workDir)
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("ALTER_TELEGRAM_API_BASE", srv.URL)

	var out, errOut strings.Builder
	if code := runNotify([]string{"Llamar", "a", "mamá"}, &out, &errOut); code != remindExitOK {
		t.Fatalf("notify = %d, want %d (stderr=%q)", code, remindExitOK, errOut.String())
	}
	received := bot.received()
	if len(received) != 1 {
		t.Fatalf("avisos enviados = %d, want one: el token del .env no se cargó", len(received))
	}
	if !strings.Contains(received[0], "Llamar a mamá") {
		t.Errorf("aviso %q sin el texto", received[0])
	}
	if printed := out.String() + errOut.String(); strings.Contains(printed, token) {
		t.Errorf("la salida %q imprime el token", printed)
	}
}

// TestNotifyRunsWithoutTheDotEnvFile pins the other side of the second net: the
// file is optional. A missing file is one reported line, and an exported token
// still sends the notice.
func TestNotifyRunsWithoutTheDotEnvFile(t *testing.T) {
	const token = "123456:TOKEN-DEL-ENTORNO"
	var bot botAPIRecorder
	srv := bot.server(t)

	missing := filepath.Join(t.TempDir(), "no-existe.env")
	t.Setenv("ALTER_ENV_FILE", missing)
	t.Setenv("TELEGRAM_TOKEN", token)
	t.Setenv("TELEGRAM_CHAT_ID", "4242")
	t.Setenv("ALTER_TELEGRAM_API_BASE", srv.URL)

	var out, errOut strings.Builder
	if code := runNotify([]string{"Llamar a mamá"}, &out, &errOut); code != remindExitOK {
		t.Fatalf("notify = %d, want %d: que falte el .env no puede romper el aviso (stderr=%q)", code, remindExitOK, errOut.String())
	}
	if got := len(bot.received()); got != 1 {
		t.Fatalf("avisos enviados = %d, want one", got)
	}
	printed := out.String() + errOut.String()
	if !strings.Contains(printed, missing) {
		t.Errorf("salida %q sin la línea que dice que falta el fichero de entorno", printed)
	}
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n"); lines != 1 {
		t.Errorf("salida %q: el fichero que falta es una línea más el envío", out.String())
	}
}

// TestNotifyPostsTheTextAndNeverLogsTheToken pins the transport: the text reaches
// the Bot API for the owner chat, and neither a success nor a transport failure
// writes the token anywhere the owner or the journal can read it.
func TestNotifyPostsTheTextAndNeverLogsTheToken(t *testing.T) {
	const token = "123456:TOKEN-QUE-NO-DEBE-SALIR"

	type call struct {
		method string
		body   string
	}
	var (
		mu    sync.Mutex
		calls []call
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, call{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], body: string(body)})
		mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":4242}}}`)
	}))
	defer srv.Close()

	t.Run("delivers the text", func(t *testing.T) {
		t.Setenv("TELEGRAM_TOKEN", token)
		t.Setenv("TELEGRAM_CHAT_ID", "4242")
		t.Setenv("ALTER_TELEGRAM_API_BASE", srv.URL)

		var out, errOut strings.Builder
		if code := runNotify([]string{"Llamar", "a", "mamá"}, &out, &errOut); code != remindExitOK {
			t.Fatalf("notify = %d, want %d (stderr=%q)", code, remindExitOK, errOut.String())
		}

		mu.Lock()
		got := append([]call(nil), calls...)
		mu.Unlock()
		if len(got) != 1 {
			t.Fatalf("llamadas al Bot API = %d, want one", len(got))
		}
		if got[0].method != "sendRichMessage" {
			t.Errorf("método = %q, want the client's own text path", got[0].method)
		}
		if !strings.Contains(got[0].body, "Llamar a mamá") {
			t.Errorf("cuerpo %q sin el texto del aviso", got[0].body)
		}
		if printed := out.String() + errOut.String(); strings.Contains(printed, token) {
			t.Errorf("la salida %q imprime el token", printed)
		}
	})

	t.Run("never prints the token on failure", func(t *testing.T) {
		t.Setenv("TELEGRAM_TOKEN", token)
		t.Setenv("TELEGRAM_CHAT_ID", "4242")
		// Nothing listens on port 1, so the transport fails with an error that embeds
		// the request URL — the one place the token could leak.
		t.Setenv("ALTER_TELEGRAM_API_BASE", "http://127.0.0.1:1")

		var out, errOut strings.Builder
		if code := runNotify([]string{"Llamar a mamá"}, &out, &errOut); code != remindExitFailed {
			t.Fatalf("notify = %d, want %d porque el envío no puede funcionar", code, remindExitFailed)
		}
		printed := out.String() + errOut.String()
		if strings.Contains(printed, token) {
			t.Errorf("la salida %q imprime el token", printed)
		}
		if !strings.Contains(printed, "<redacted>") {
			t.Errorf("salida %q sin el token redactado: el error de transporte lo lleva en la URL", printed)
		}
	})
}

// TestRemindSyncReportsInvalidAndFailedActions pins the exit code contract: a
// reminder that breaks the contract, or an action the machine refuses, makes the
// command exit 1 and print the reason, while the pass still finishes the rest.
func TestRemindSyncReportsInvalidAndFailedActions(t *testing.T) {
	t.Run("invalid reminder", func(t *testing.T) {
		h := newRemindHarness(t)
		const slug = "2026-11-03-llamar-a-mama"
		h.fake.SetUnits(slug)
		if err := os.MkdirAll(filepath.Join(h.root, remindersFolder), 0o755); err != nil {
			t.Fatalf("crear la carpeta: %v", err)
		}
		broken := "---\nid: 2026-11-03-llamar-a-mama\ntype: reminder\ntitle: \"Llamar a mamá\"\nstatus: open\ncreated: 2026-11-03T09:15:00+01:00\ntags: \"familia\"\n---\n\n# Llamar a mamá\n"
		if err := os.WriteFile(h.path(slug), []byte(broken), 0o644); err != nil {
			t.Fatalf("escribir el recordatorio roto: %v", err)
		}

		if code := h.runner.sync(context.Background(), false); code != remindExitFailed {
			t.Fatalf("sync = %d, want %d: un recordatorio inválido no puede salir bien", code, remindExitFailed)
		}
		if !strings.Contains(h.err.String(), "remind_at") {
			t.Errorf("stderr %q sin el problema que hay que arreglar", h.err.String())
		}
		// The stale timer of an invalid reminder goes: it could only wake a `fire` that
		// refuses.
		if removed := h.fake.Removed(); len(removed) != 1 || removed[0] != slug {
			t.Errorf("borrados = %v, want the timer of the invalid reminder", removed)
		}
		if created := h.fake.Created(); len(created) != 0 {
			t.Errorf("creados = %v, want nothing armed for an invalid reminder", created)
		}
	})

	t.Run("failing scheduler", func(t *testing.T) {
		h := newRemindHarness(t)
		future := reminderSpec{slug: "2026-11-03-llamar-a-mama", remindAt: "2026-11-03T18:00:00+01:00"}
		h.write(future)
		h.fake.FailOp(reminder.OpCreate, errors.New("systemd-run no está disponible"))

		if code := h.runner.sync(context.Background(), false); code != remindExitFailed {
			t.Fatalf("sync = %d, want %d cuando el timer no se puede crear", code, remindExitFailed)
		}
		if !strings.Contains(h.err.String(), "no se pudo crear el timer") {
			t.Errorf("stderr %q sin la acción que falló", h.err.String())
		}
	})
}

// TestStartupReminderSyncStaysOutOfTheWayOnAFreshVault pins the start-up hook: a
// vault without reminders is a silent no-op, so a bridge start neither depends on
// systemd nor writes a line at every restart.
func TestStartupReminderSyncStaysOutOfTheWayOnAFreshVault(t *testing.T) {
	var logged strings.Builder
	cfg := config.Config{VaultDir: t.TempDir()}

	startupReminderSync(context.Background(), cfg, log.New(&logged, "", 0))
	if logged.String() != "" {
		t.Errorf("log %q, want silence on a vault without reminders", logged.String())
	}

	// A cancelled context is the normal shutdown, not a reconciliation.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startupReminderSync(ctx, cfg, log.New(&logged, "", 0))
	if logged.String() != "" {
		t.Errorf("log %q, want nothing after a cancelled start", logged.String())
	}
}

// TestRemindCommandRejectsBadCalls pins the dispatch: a typo is answered with the
// usage text and exit 2, before any configuration is read or any vault touched.
func TestRemindCommandRejectsBadCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{name: "remind without a subcommand", args: []string{"remind"}, code: remindExitBadArgs},
		{name: "unknown subcommand", args: []string{"remind", "later"}, code: remindExitBadArgs},
		{name: "unknown sync flag", args: []string{"remind", "sync", "--yes"}, code: remindExitBadArgs},
		{name: "fire without a slug", args: []string{"remind", "fire"}, code: remindExitBadArgs},
		{name: "fire with two slugs", args: []string{"remind", "fire", "a", "b"}, code: remindExitBadArgs},
		{name: "notify without text", args: []string{"notify"}, code: remindExitBadArgs},
		{name: "remind help", args: []string{"remind", "--help"}, code: remindExitOK},
		{name: "notify help", args: []string{"notify", "-h"}, code: remindExitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut strings.Builder
			handled, code := runRemindCommand(tc.args, &out, &errOut)
			if !handled {
				t.Fatal("runRemindCommand no reconoció el subcomando")
			}
			if code != tc.code {
				t.Errorf("code = %d, want %d", code, tc.code)
			}
			if !strings.Contains(out.String()+errOut.String(), "uso:") {
				t.Errorf("salida %q sin el texto de uso", out.String()+errOut.String())
			}
		})
	}

	var out, errOut strings.Builder
	if handled, _ := runRemindCommand([]string{"vault"}, &out, &errOut); handled {
		t.Error("runRemindCommand reclamó un subcomando que no es suyo")
	}
}
