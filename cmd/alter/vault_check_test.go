package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures for the command tests. They are valid per the alter-vault contract; a
// case that needs a broken entry spells the broken line itself.
const (
	checkTaskDoc = `---
id: 2026-10-29-renovar-dni
type: task
title: "Renovar el DNI"
status: open
priority: high
created: 2026-10-29T09:12:00+01:00
due: 2026-11-15T10:00:00+01:00
tags: "personal,documentos"
---

# Renovar el DNI

Pedir cita en la comisaría.
`

	checkReminderDoc = `---
id: 2026-10-29-llamar-a-mama
type: reminder
title: "Llamar a mamá"
status: open
created: 2026-10-29T09:15:00+01:00
remind_at: 2026-10-30T19:00:00+01:00
tags: "familia"
---

# Llamar a mamá

Preguntar por la revisión.
`

	checkEventDoc = `---
id: 2026-10-29-viaje-a-sevilla
type: event
title: "Viaje a Sevilla"
status: open
created: 2026-10-29T10:00:00+01:00
due: 2026-11-02T08:30:00+01:00
tags: "viajes"
---

# Viaje a Sevilla

Salida a las 08:30.
`

	checkNoteDoc = `---
id: 2026-10-29-lista-de-libros
type: note
title: "Lista de libros"
created: 2026-10-29T11:00:00+01:00
tags: ""
---

# Lista de libros

- El nombre de la rosa
`

	// brokenTaskDoc has an out-of-contract status on line 5.
	brokenTaskDoc = `---
id: 2026-10-29-tarea-rota
type: task
title: "Tarea rota"
status: pending
created: 2026-10-29T09:12:00+01:00
tags: ""
---

# Tarea rota
`

	// brokenReminderDoc has no remind_at, so the entry is invalid as a whole and
	// the problem belongs to the frontmatter block (line 1).
	brokenReminderDoc = `---
id: 2026-10-29-recordatorio-roto
type: reminder
title: "Recordatorio roto"
status: open
created: 2026-10-29T09:15:00+01:00
tags: ""
---

# Recordatorio roto
`
)

// writeVaultEntry writes a file, creating its parent directory.
func writeVaultEntry(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestVaultCheckCleanVault pins the clean run: the four entry folders are
// walked, other files and folders are ignored, the summary is one line and the
// exit code is 0.
func TestVaultCheckCleanVault(t *testing.T) {
	root := t.TempDir()
	writeVaultEntry(t, filepath.Join(root, "tasks", "2026-10-29-renovar-dni.md"), checkTaskDoc)
	writeVaultEntry(t, filepath.Join(root, "reminders", "2026-10-29-llamar-a-mama.md"), checkReminderDoc)
	writeVaultEntry(t, filepath.Join(root, "events", "2026-10-29-viaje-a-sevilla.md"), checkEventDoc)
	writeVaultEntry(t, filepath.Join(root, "notes", "2026-10-29-lista-de-libros.md"), checkNoteDoc)
	// Anything outside the four entry folders is not an entry.
	writeVaultEntry(t, filepath.Join(root, "inbox", "2026-10-29-renovar-dni.md"), checkTaskDoc)
	writeVaultEntry(t, filepath.Join(root, "tasks", "notas.txt"), "no es markdown")
	writeVaultEntry(t, filepath.Join(root, "tasks", "borrador.md.bak"), checkTaskDoc)

	var stdout, stderr strings.Builder
	handled, code := runVaultCommand([]string{"vault", "check", root}, &stdout, &stderr)

	if !handled {
		t.Fatal("runVaultCommand = not handled, want the vault subcommand handled")
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0 for a clean vault (stderr %q)", code, stderr.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want nothing on a clean vault", stderr.String())
	}
	if got, want := stdout.String(), "vault check: 4 entradas, 0 problemas\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestVaultCheckReportsProblemsAndExitsOne pins the failing run: one line per
// problem as <path>:<line>: <message> on stderr, the entry counted, the summary
// on stdout and exit code 1.
func TestVaultCheckReportsProblemsAndExitsOne(t *testing.T) {
	root := t.TempDir()
	taskPath := filepath.Join(root, "tasks", "2026-10-29-tarea-rota.md")
	reminderPath := filepath.Join(root, "reminders", "2026-10-29-recordatorio-roto.md")
	writeVaultEntry(t, taskPath, brokenTaskDoc)
	writeVaultEntry(t, reminderPath, brokenReminderDoc)

	var stdout, stderr strings.Builder
	handled, code := runVaultCommand([]string{"vault", "check", root}, &stdout, &stderr)

	if !handled {
		t.Fatal("runVaultCommand = not handled, want the vault subcommand handled")
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1 when an entry is invalid", code)
	}
	wantStderr := taskPath + `:5: "status" debe ser open, done o cancelled (es "pending")` + "\n" +
		reminderPath + `:1: falta la clave obligatoria "remind_at" para type=reminder: un recordatorio sin remind_at es inválido` + "\n"
	if got := stderr.String(); got != wantStderr {
		t.Errorf("stderr = %q, want %q", got, wantStderr)
	}
	if got, want := stdout.String(), "vault check: 2 entradas, 2 problemas\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestVaultCheckDefaultsToTheVaultDir pins the directory argument: without one,
// the command validates ALTER_VAULT_DIR, the same variable the bridge uses.
func TestVaultCheckDefaultsToTheVaultDir(t *testing.T) {
	root := t.TempDir()
	writeVaultEntry(t, filepath.Join(root, "notes", "2026-10-29-lista-de-libros.md"), checkNoteDoc)
	t.Setenv("ALTER_VAULT_DIR", root)

	var stdout, stderr strings.Builder
	handled, code := runVaultCommand([]string{"vault", "check"}, &stdout, &stderr)

	if !handled {
		t.Fatal("runVaultCommand = not handled, want the vault subcommand handled")
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if got, want := stdout.String(), "vault check: 1 entrada, 0 problemas\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestVaultCheckRejectsAnEntryInTheWrongFolder pins the folder-to-type rule from
// the contract: the folder decides the type, so a task inside reminders/ is a
// problem pointing at the type line.
func TestVaultCheckRejectsAnEntryInTheWrongFolder(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "reminders", "2026-10-29-renovar-dni.md")
	writeVaultEntry(t, path, checkTaskDoc)

	var stdout, stderr strings.Builder
	handled, code := runVaultCommand([]string{"vault", "check", root}, &stdout, &stderr)

	if !handled {
		t.Fatal("runVaultCommand = not handled, want the vault subcommand handled")
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	wantStderr := path + `:3: type "task" no corresponde a la carpeta "reminders"` + "\n"
	if got := stderr.String(); got != wantStderr {
		t.Errorf("stderr = %q, want %q", got, wantStderr)
	}
}

// TestVaultCheckFailsWhenTheVaultIsMissing pins that a wrong directory is a
// usage error (exit 2), not a clean run: reporting "0 problemas" for a vault
// that does not exist would hide a misconfigured ALTER_VAULT_DIR.
func TestVaultCheckFailsWhenTheVaultIsMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-existe")

	var stdout, stderr strings.Builder
	handled, code := runVaultCommand([]string{"vault", "check", missing}, &stdout, &stderr)

	if !handled {
		t.Fatal("runVaultCommand = not handled, want the vault subcommand handled")
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for an unreadable vault", code)
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want no summary when the vault cannot be read", stdout.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("stderr = %q, want it to name %q", stderr.String(), missing)
	}
}

// TestVaultCommandDispatch pins the boundary between the subcommand and the
// runtime: only "vault ..." is taken over, anything else (including no
// arguments at all) keeps reaching main's bridge path exactly as before.
func TestVaultCommandDispatch(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantHandled bool
		wantCode    int
		wantOut     string
		wantErr     string
	}{
		{name: "no arguments", args: nil, wantHandled: false},
		{name: "unrelated argument still starts the bridge", args: []string{"bridge"}, wantHandled: false},
		{name: "vault without subcommand", args: []string{"vault"}, wantHandled: true, wantCode: 2, wantErr: "uso: alter vault check"},
		{name: "vault with unknown subcommand", args: []string{"vault", "revisar"}, wantHandled: true, wantCode: 2, wantErr: "uso: alter vault check"},
		{name: "vault check with two directories", args: []string{"vault", "check", "a", "b"}, wantHandled: true, wantCode: 2, wantErr: "uso: alter vault check"},
		{name: "vault help", args: []string{"vault", "-h"}, wantHandled: true, wantCode: 0, wantOut: "uso: alter vault check"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			handled, code := runVaultCommand(tc.args, &stdout, &stderr)

			if handled != tc.wantHandled {
				t.Fatalf("handled = %v, want %v", handled, tc.wantHandled)
			}
			if !handled {
				if stdout.Len() != 0 || stderr.Len() != 0 {
					t.Errorf("output = %q/%q, want nothing when the runtime keeps the process", stdout.String(), stderr.String())
				}
				return
			}
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if !strings.Contains(stdout.String(), tc.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.wantOut)
			}
			if !strings.Contains(stderr.String(), tc.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.wantErr)
			}
		})
	}
}
