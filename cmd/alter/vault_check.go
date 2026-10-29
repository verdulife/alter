package main

// The vault validator: `alter vault check [dir]`. It is a subcommand, not a
// second runtime. It reads the vault and exits, so it can be run by hand today
// and by the future cron/daemon before trusting an entry. The contract it
// enforces is .agents/skills/alter-vault/SKILL.md.

import (
	"fmt"
	"io"
	"strings"

	"github.com/verdu/alter/internal/config"
	"github.com/verdu/alter/internal/vault"
)

// vaultUsage is the help text for the only subcommand of the binary.
const vaultUsage = `uso: alter vault check [dir]

Valida el frontmatter de las entradas del vault (tasks, reminders, events y
notes) contra el contrato de .agents/skills/alter-vault/SKILL.md.

Sin dir, usa ALTER_VAULT_DIR y, si no está definido, $HOME/vault. Ignora todo lo
que no sea markdown de esas cuatro carpetas.

Imprime un problema por línea con el formato <ruta>:<línea>: <mensaje> en stderr,
y una línea de resumen en stdout. Sale con 0 si todas las entradas son válidas,
1 si alguna no lo es y 2 si no puede leer el vault.
`

// runVaultCommand dispatches the vault subcommand. handled is false when the
// arguments are not a vault command: main then starts the bridge exactly as
// before.
func runVaultCommand(args []string, stdout, stderr io.Writer) (handled bool, code int) {
	if len(args) == 0 || args[0] != "vault" {
		return false, 0
	}
	rest := args[1:]
	if len(rest) == 1 && (rest[0] == "-h" || rest[0] == "--help") {
		fmt.Fprint(stdout, vaultUsage)
		return true, 0
	}
	if len(rest) == 0 || rest[0] != "check" || len(rest) > 2 {
		fmt.Fprint(stderr, vaultUsage)
		return true, 2
	}

	dir := ""
	if len(rest) == 2 {
		dir = strings.TrimSpace(rest[1])
	}
	if dir == "" {
		dir = config.Load().VaultDir
	}

	report, err := vault.CheckVault(dir)
	if err != nil {
		fmt.Fprintf(stderr, "vault check: %v\n", err)
		return true, 2
	}
	for _, problem := range report.Problems {
		fmt.Fprintln(stderr, problem.String())
	}
	fmt.Fprintf(stdout, "vault check: %s, %s\n",
		vaultCount(report.Entries, "entrada", "entradas"),
		vaultCount(len(report.Problems), "problema", "problemas"))
	if len(report.Problems) > 0 {
		return true, 1
	}
	return true, 0
}

// vaultCount renders "1 entrada" and "2 entradas" instead of a bare number.
func vaultCount(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}
