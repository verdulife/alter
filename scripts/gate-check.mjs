#!/usr/bin/env node
/**
 * gate-check — the table that pins Alter's selective approval gate (T22).
 *
 * It loads the REAL extension (`internal/tools/extension/index.ts`) with Node's
 * built-in type stripping, registers its `tool_call` handler against a stub `pi`,
 * and runs one case per rule: what must run unasked, what must ask, and — for
 * every asking case — that answering "no" blocks the call.
 *
 * What it proves: the classifier's verdict for each command and each `write`
 * target, the denial path of the handler, and — for every case that asks — the
 * text the owner reads: one short human title, the consequence first, and the
 * command whole after the separator the bridge splits on. The extension emits
 * data, not markup, so this table never asserts a code fence: the bridge owns
 * the rendering.
 * What it cannot prove: Pi's runtime. It never spawns `pi`, never answers an
 * `extension_ui_request` over RPC, and never executes a command. A `bash` case
 * asserts the decision, not the effect.
 *
 * Dependency-free on purpose: only Node built-ins, no package install.
 *
 * Usage: node scripts/gate-check.mjs
 *        GATE_EXTENSION=/path/to/other/index.ts node scripts/gate-check.mjs
 *
 * The optional GATE_EXTENSION exists to run the same table against another
 * revision of the file; nothing in the repository depends on it.
 */

import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const extensionPath = process.env.GATE_EXTENSION || join(scriptDir, "..", "internal", "tools", "extension", "index.ts");

if (!existsSync(extensionPath)) {
	console.error(`gate-check: no encuentro la extensión en ${extensionPath}`);
	process.exit(2);
}

// Scratch outside the worktree: a fake $HOME plus a path outside it. Both are
// removed in the teardown at the end of this file.
const home = mkdtempSync(join(tmpdir(), "alter-gate-home-"));
const outside = mkdtempSync(join(tmpdir(), "alter-gate-outside-"));

function touch(path) {
	mkdirSync(dirname(path), { recursive: true });
	writeFileSync(path, "contenido previo\n");
}

touch(join(home, "existente.txt"));
touch(join(home, "a.txt"));
touch(join(home, "b.txt"));
touch(join(outside, "f"));
// An existing regular file outside home: the shape of `> /tmp/existente.txt`,
// built inside the scratch directory instead of a shared /tmp path.
touch(join(outside, "existente.txt"));

const previousHome = process.env.HOME;
process.env.HOME = home;

// The gate reads $HOME per call, so setting it before the cases is enough; the
// session's working directory is the fake home, as Pi's cwd is the owner's home.
const sessionCwd = home;

// --- stub `pi` -------------------------------------------------------------

const handlers = new Map();
const tools = [];

const pi = {
	on(event, handler) {
		const list = handlers.get(event) ?? [];
		list.push(handler);
		handlers.set(event, list);
	},
	registerTool(definition) {
		tools.push(definition);
	},
};

const extension = await import(pathToFileURL(extensionPath).href);
if (typeof extension.default !== "function") {
	console.error("gate-check: la extensión no exporta una función por defecto");
	process.exit(2);
}
extension.default(pi);

const gateHandlers = handlers.get("tool_call") ?? [];
if (gateHandlers.length !== 1) {
	console.error(`gate-check: esperaba 1 handler de tool_call, encontré ${gateHandlers.length}`);
	process.exit(2);
}
const gate = gateHandlers[0];

// --- harness ---------------------------------------------------------------

let questions = [];
let answer = true;
const logLines = [];

const ctx = {
	cwd: sessionCwd,
	mode: "rpc",
	hasUI: true,
	ui: {
		async confirm(title, message) {
			questions.push({ title, message });
			return answer;
		},
	},
};

const realError = console.error;
console.error = (...args) => logLines.push(args.map((value) => String(value)).join(" "));

async function decide(tool, input) {
	questions = [];
	const result = await gate({ toolName: tool, input, toolCallId: "check" }, ctx);
	return { asked: questions.length > 0, question: questions[0], result };
}

// --- the text the owner reads ----------------------------------------------

// One short question for every approval: naming the tool told the owner nothing
// he could judge on a phone, so the consequence is the first line of the message.
const APPROVAL_TITLE = "¿Lo permito?";

// The unit separator the extension puts between the reason and the command. The
// bridge splits on it and renders the command as a code block, so the extension
// itself carries no markup at all.
const APPROVAL_SEPARATOR = "\u001f";

// A typical multi-line script. The approval must show it whole and with its line
// breaks, after the separator and without a fence; `shows` below asserts exactly
// that.
const multiLineCommand = [
	"cd ~/alter",
	"export TMPDIR=/tmp",
	"rm -rf ~/dist",
	"mkdir -p ~/dist",
	"gofmt -l cmd internal",
	"go vet ./...",
	"go test ./... -race -count=1",
	"go build -o ~/dist/alter ./cmd/alter",
	"cp ~/dist/alter ~/bin/alter",
	"chmod +x ~/bin/alter",
	"go test ./internal/tools/... -run TestTools -count=1",
	"go test ./internal/bridge/... -run TestSession -count=1",
	"echo listo",
].join("\n");

/**
 * Problems with the approval text of one asking case, or null when it reads right.
 * It checks the three things the owner judges: the title, the consequence first,
 * and — when the case says so — the whole command after the separator, in the
 * wire format the bridge splits on. Every problem is reported, not only the first,
 * so a run shows all the wording that still has to change.
 */
function textProblems(testCase, question) {
	const problems = [];
	if (question.title !== APPROVAL_TITLE) {
		problems.push(`título ${JSON.stringify(question.title)}, esperaba ${JSON.stringify(APPROVAL_TITLE)}`);
	}
	if (testCase.reason !== undefined && !question.message.startsWith(`${testCase.reason}${APPROVAL_SEPARATOR}`)) {
		problems.push(`mensaje ${JSON.stringify(firstPart(question.message))}, esperaba ${JSON.stringify(testCase.reason)} seguido del separador`);
	}
	if (testCase.shows !== undefined) {
		const want = `${testCase.reason}${APPROVAL_SEPARATOR}${testCase.shows}`;
		if (question.message !== want) {
			problems.push("el mensaje no lleva la razón, el separador y el comando entero, sin bloques de código");
		}
	}
	return problems.length > 0 ? problems.join("; ") : null;
}

/** Text before the separator: the part the owner reads as the consequence. */
function firstPart(message) {
	const cut = message.indexOf(APPROVAL_SEPARATOR);
	return cut < 0 ? message : message.slice(0, cut);
}

// --- cases ----------------------------------------------------------------

const bash = (command) => ({ tool: "bash", input: { command } });

const cases = [
	// --- bash: allowed, no question ------------------------------------
	{ label: "listar", ...bash("ls -la"), expect: "allow" },
	{ label: "leer", ...bash("cat existente.txt"), expect: "allow" },
	{ label: "buscar", ...bash("grep -r x ."), expect: "allow" },
	{ label: "test de go", ...bash("go test ./..."), expect: "allow" },
	{ label: "git status", ...bash("git status"), expect: "allow" },
	{ label: "git add", ...bash("git add -A"), expect: "allow" },
	{ label: "git commit", ...bash("git commit -m x"), expect: "allow" },
	{ label: "mkdir", ...bash("mkdir nuevo"), expect: "allow" },
	{ label: "echo", ...bash("echo hola"), expect: "allow" },
	{ label: "git clean en seco", ...bash("git clean -n"), expect: "allow" },
	{ label: "docker compose down sin volúmenes", ...bash("docker compose down"), expect: "allow" },
	{ label: "npm install local", ...bash("npm install x"), expect: "allow" },
	{ label: "mv a destino nuevo", ...bash("mv a.txt c.txt"), expect: "allow" },
	{ label: "curl a un archivo", ...bash("curl -s https://x -o out.txt"), expect: "allow" },
	{ label: "find sin borrar", ...bash("find . -name '*.go'"), expect: "allow" },
	{ label: "echo > archivo nuevo fuera de home", ...bash(`echo x > ${outside}/nuevo.txt`), expect: "allow" },
	{ label: "echo >> archivo existente no sensible", ...bash(`echo x >> ${home}/existente.txt`), expect: "allow" },
	{ label: "dos comandos permitidos", ...bash("ls -la && git status"), expect: "allow" },

	// --- bash: redirigir a un dispositivo que descarta o repite la salida ---
	// `2>/dev/null` es como todo script calla el stderr. El clasificador veía un
	// `>` hacia `/dev` (ruta sensible) y preguntaba siempre: 6 de las 8 preguntas
	// de la mañana eran esta, y el canal dejaba de servir mientras el dueño no
	// pudiera contestar. Un dispositivo que descarta o repite no destruye nada.
	{ label: "ls con stderr a /dev/null", ...bash("ls 2>/dev/null"), expect: "allow" },
	{
		label: "journalctl con stderr a /dev/null y tubería",
		...bash("journalctl --user -u alter.service --since '19:30' --no-pager 2>/dev/null | tail -5"),
		expect: "allow",
	},
	{ label: "echo a /dev/null", ...bash("echo x > /dev/null"), expect: "allow" },
	{ label: ">> a /dev/null", ...bash("echo x >> /dev/null"), expect: "allow" },
	{ label: "1> a /dev/null", ...bash("echo x 1>/dev/null"), expect: "allow" },
	{ label: "2>&1 seguido de >/dev/null", ...bash("echo x 2>&1 >/dev/null"), expect: "allow" },
	{ label: ">/dev/null seguido de 2>&1", ...bash("echo x >/dev/null 2>&1"), expect: "allow" },
	{ label: "&> a /dev/null", ...bash("command &>/dev/null"), expect: "allow" },
	{ label: "a /dev/stdout", ...bash("echo x > /dev/stdout"), expect: "allow" },
	{ label: "a /dev/stderr", ...bash("echo x 2> /dev/stderr"), expect: "allow" },
	{ label: "a /dev/tty", ...bash("echo x > /dev/tty"), expect: "allow" },
	// La regresión que dejaba pasar el journal: tubería, 2>/dev/null y un
	// programa de solo lectura en el mismo comando.
	{
		label: "regresión: tubería + 2>/dev/null + programa de solo lectura",
		...bash("find ~/.pi -name '*.jsonl' -newermt '-3 hours' -printf '%T@ %p\\n' 2>/dev/null | sort -rn | head -8"),
		expect: "allow",
	},

	// --- bash: deletion and truncation ---------------------------------
	{ label: "rm -rf", ...bash("rm -rf build"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "unlink", ...bash("unlink existente.txt"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "truncate", ...bash("truncate -s 0 existente.txt"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "dd", ...bash("dd if=/dev/zero of=x bs=1M count=1"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "rmdir", ...bash("rmdir build"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "find -delete", ...bash("find . -name x -delete"), expect: "ask", reason: "Va a borrar archivos." },

	// --- bash: the command behind a wrapper ----------------------------
	{ label: "sudo rm", ...bash("sudo rm x"), expect: "ask", reason: "Va a pedir permisos de administrador." },
	{ label: "asignación de entorno + rm", ...bash("FOO=1 rm x"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "env + rm", ...bash("env FOO=1 rm x"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "xargs rm", ...bash("find . -name x | xargs rm"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "bash -c con rm", ...bash("bash -c 'rm -rf x'"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "segundo comando de la línea", ...bash("ls && rm -rf x"), expect: "ask", reason: "Va a borrar archivos." },

	// --- bash: overwriting through redirection -------------------------
	{ label: "> sobre un archivo existente fuera de home", ...bash(`echo x > ${outside}/f`), expect: "ask", reason: `Va a sobrescribir ${outside}/f.` },
	{
		label: "> sobre un archivo existente tipo /tmp/existente.txt",
		...bash(`echo x > ${outside}/existente.txt`),
		expect: "ask",
		reason: `Va a sobrescribir ${outside}/existente.txt.`,
	},
	{ label: "> sobre ~/.bashrc", ...bash("echo x > ~/.bashrc"), expect: "ask", reason: "Va a sobrescribir ~/.bashrc." },
	{ label: "> sobre /etc/hosts", ...bash("echo x > /etc/hosts"), expect: "ask", reason: "Va a sobrescribir /etc/hosts." },
	{ label: "> con variable entrecomillada en la ruta", ...bash('echo x > "$VAR"'), expect: "ask", reason: "Va a escribir en una ruta que no puedo comprobar." },
	// La lista de dispositivos es cerrada: otro `/dev` sigue preguntando, y una
	// variable que no es uno de ellos también.
	{ label: "> sobre otro dispositivo de /dev", ...bash("echo x > /dev/sda"), expect: "ask", reason: "Va a sobrescribir /dev/sda." },
	{ label: "> sobre $SINK entrecomillado", ...bash('echo x > "$SINK"'), expect: "ask", reason: "Va a escribir en una ruta que no puedo comprobar." },
	{ label: "> sobre un archivo existente en home", ...bash(`echo x > ${home}/existente.txt`), expect: "ask", reason: "Va a sobrescribir ~/existente.txt." },
	{ label: ">> sobre una ruta sensible", ...bash("echo x >> ~/.ssh/authorized_keys"), expect: "ask", reason: "Va a añadir contenido a ~/.ssh/authorized_keys." },
	{ label: "> sobre un arranque de shell", ...bash("echo x > .bashrc"), expect: "ask", reason: "Va a sobrescribir ~/.bashrc." },
	{ label: "> con variable en la ruta", ...bash("echo x > $HOME/.ssh/authorized_keys"), expect: "ask", reason: "Va a escribir en una ruta que no puedo comprobar." },
	{ label: "mv sobre un destino existente", ...bash("mv a.txt b.txt"), expect: "ask", reason: "Va a sobrescribir un archivo." },

	// --- bash: permissions and processes -------------------------------
	{ label: "chmod", ...bash("chmod +x f"), expect: "ask", reason: "Va a cambiar permisos de archivos." },
	{ label: "chown", ...bash("chown root f"), expect: "ask", reason: "Va a cambiar permisos de archivos." },
	{ label: "chattr", ...bash("chattr +i f"), expect: "ask", reason: "Va a cambiar atributos de archivos." },
	{ label: "kill", ...bash("kill 123"), expect: "ask", reason: "Va a cambiar el estado del sistema." },
	{ label: "killall", ...bash("killall node"), expect: "ask", reason: "Va a cambiar el estado del sistema." },
	{ label: "systemctl", ...bash("systemctl restart x"), expect: "ask", reason: "Va a cambiar el estado del sistema." },
	{ label: "service", ...bash("service x restart"), expect: "ask", reason: "Va a cambiar el estado del sistema." },
	{ label: "shutdown", ...bash("shutdown -h now"), expect: "ask", reason: "Va a cambiar el estado del sistema." },
	{ label: "reboot", ...bash("reboot"), expect: "ask", reason: "Va a cambiar el estado del sistema." },

	// --- bash: package and system state --------------------------------
	{ label: "apt install", ...bash("apt install x"), expect: "ask", reason: "Va a instalar software en el sistema." },
	{ label: "dpkg", ...bash("dpkg -r x"), expect: "ask", reason: "Va a instalar software en el sistema." },
	{ label: "npm install -g", ...bash("npm install -g x"), expect: "ask", reason: "Va a instalar software en el sistema." },
	{ label: "npm i -g", ...bash("npm i -g x"), expect: "ask", reason: "Va a instalar software en el sistema." },
	{ label: "pip install", ...bash("pip install x"), expect: "ask", reason: "Va a instalar software en el sistema." },
	{ label: "cargo install", ...bash("cargo install x"), expect: "ask", reason: "Va a instalar software en el sistema." },
	{ label: "crontab", ...bash("crontab -r"), expect: "ask", reason: "Va a programar una tarea automática." },

	// --- bash: git that publishes or rewrites --------------------------
	{ label: "git push", ...bash("git push"), expect: "ask", reason: "Va a publicar o reescribir el historial del repositorio." },
	{ label: "git -C x push --force", ...bash("git -C x push --force"), expect: "ask", reason: "Va a publicar o reescribir el historial del repositorio." },
	{ label: "git reset --hard", ...bash("git reset --hard"), expect: "ask", reason: "Va a descartar cambios locales." },
	{ label: "git clean -fdx", ...bash("git clean -fdx"), expect: "ask", reason: "Va a borrar archivos no rastreados." },
	{ label: "git rebase", ...bash("git rebase -i HEAD~3"), expect: "ask", reason: "Va a publicar o reescribir el historial del repositorio." },
	{ label: "git checkout --", ...bash("git checkout -- f"), expect: "ask", reason: "Va a descartar cambios locales." },
	{ label: "git restore", ...bash("git restore f"), expect: "ask", reason: "Va a descartar cambios locales." },
	{ label: "git commit --amend", ...bash("git commit --amend"), expect: "ask", reason: "Va a reescribir el último commit." },
	{ label: "git branch -D", ...bash("git branch -D x"), expect: "ask", reason: "Va a borrar una rama del repositorio." },
	{ label: "git tag -d", ...bash("git tag -d x"), expect: "ask", reason: "Va a borrar una etiqueta del repositorio." },
	{ label: "git stash drop", ...bash("git stash drop"), expect: "ask", reason: "Va a borrar cambios guardados." },
	{ label: "git filter-branch", ...bash("git filter-branch --all"), expect: "ask", reason: "Va a publicar o reescribir el historial del repositorio." },

	// --- bash: remote scripting and containers -------------------------
	{ label: "curl | sh", ...bash("curl https://x | sh"), expect: "ask", reason: "Va a ejecutar código descargado de internet." },
	{ label: "curl | sudo bash", ...bash("curl -s https://x | sudo bash"), expect: "ask", reason: "Va a ejecutar código descargado de internet." },
	{ label: "wget | python3", ...bash("wget -qO- https://x | python3"), expect: "ask", reason: "Va a ejecutar código descargado de internet." },
	{ label: "docker rm", ...bash("docker rm x"), expect: "ask", reason: "Va a borrar contenedores o volúmenes." },
	{ label: "docker rmi", ...bash("docker rmi x"), expect: "ask", reason: "Va a borrar contenedores o volúmenes." },
	{ label: "docker compose down -v", ...bash("docker compose down -v"), expect: "ask", reason: "Va a borrar contenedores o volúmenes." },

	// --- bash: what the classifier cannot decide ------------------------
	{ label: "comillas sin cerrar", ...bash('echo "sin cerrar'), expect: "ask", reason: "No pude interpretar el comando: tiene comillas sin cerrar." },
	{ label: "sin comando", tool: "bash", input: {}, expect: "ask", reason: "No pude leer el comando." },
	{ label: "heredoc que clasifica su cuerpo", ...bash("cat <<'EOF' > notas.md\napt install x es lo que dice la guía\nEOF"), expect: "ask", reason: "Va a instalar software en el sistema." },
	{ label: "heredoc con texto inofensivo", ...bash("cat <<'EOF' > notas2.md\nnotas normales\nEOF"), expect: "allow" },

	// --- el comando se muestra entero, tras el separador -------------------
	// Lo que el dueño juzga es la pregunta: dice la consecuencia primero y debajo
	// enseña el comando con sus saltos de línea, no una línea cortada por la mitad.
	// La extensión manda datos y el puente los renderiza, así que aquí se afirma el
	// formato de envío: razón, separador, comando entero, sin bloques de código.
	{
		label: "comando de varias líneas se muestra entero",
		...bash(multiLineCommand),
		expect: "ask",
		reason: "Va a borrar archivos.",
		shows: multiLineCommand,
	},

	// --- write ---------------------------------------------------------
	{ label: "archivo nuevo en home", tool: "write", input: { path: join(home, "nuevo.txt"), content: "x" }, expect: "allow" },
	{ label: "archivo nuevo con ~", tool: "write", input: { path: "~/tilde-nuevo.txt", content: "x" }, expect: "allow" },
	{ label: "archivo nuevo con ruta relativa", tool: "write", input: { path: "sub/dir/nuevo.txt", content: "x" }, expect: "allow" },
	{ label: "archivo existente en home", tool: "write", input: { path: join(home, "existente.txt"), content: "x" }, expect: "ask", reason: "Va a sobrescribir ~/existente.txt." },
	{ label: "archivo nuevo fuera de home", tool: "write", input: { path: join(outside, "nuevo.txt"), content: "x" }, expect: "ask", reason: `Va a escribir fuera de tu carpeta personal: ${outside}/nuevo.txt.` },
	{ label: "ruta relativa que sale de home", tool: "write", input: { path: "../fuera.txt", content: "x" }, expect: "ask" },
	{ label: "~user que no puedo expandir", tool: "write", input: { path: "~root/x", content: "x" }, expect: "ask", reason: "No puedo comprobar la ruta del archivo." },

	// --- edit: never asks ----------------------------------------------
	{ label: "edit sobre un archivo existente", tool: "edit", input: { path: join(home, "existente.txt"), oldText: "contenido", newText: "otro" }, expect: "allow" },
	{ label: "edit fuera de home", tool: "edit", input: { path: join(outside, "otro.md"), oldText: "a", newText: "b" }, expect: "allow" },

	// --- read-only tools -----------------------------------------------
	{ label: "read", tool: "read", input: { path: join(home, "existente.txt") }, expect: "allow" },
	{ label: "grep herramienta", tool: "grep", input: { pattern: "x", path: "." }, expect: "allow" },

	// --- tools de paquete: sin regla, sin destrucción en el nombre ------
	// La carga global de Pi trae estas herramientas; pedir botón por ellas
	// era el mismo peaje, una capa más afuera.
	{ label: "web_search", tool: "web_search", input: { query: "x" }, expect: "allow" },
	{ label: "fetch_content", tool: "fetch_content", input: { url: "https://x" }, expect: "allow" },
	{ label: "memory_save", tool: "memory_save", input: { title: "x" }, expect: "allow" },
	{ label: "mem_search", tool: "mem_search", input: { query: "x" }, expect: "allow" },
	{ label: "subagent_run", tool: "subagent_run", input: { prompt: "x" }, expect: "allow" },
	{ label: "address_lookup no casa con rm", tool: "address_lookup", input: { address: "x" }, expect: "allow" },

	// --- un nombre que dice que destruye pregunta, aunque no haya regla --
	{ label: "delete_file", tool: "delete_file", input: { path: "x" }, expect: "ask", reason: "La herramienta delete_file puede borrar datos." },
	{ label: "removeRepo (camelCase)", tool: "removeRepo", input: { repo: "x" }, expect: "ask", reason: "La herramienta removeRepo puede borrar datos." },
	{ label: "reset_session", tool: "reset_session", input: { id: "x" }, expect: "ask" },
	{ label: "truncate_table", tool: "truncate_table", input: { table: "x" }, expect: "ask" },
	{ label: "rm_home (rm como componente)", tool: "rm_home", input: { path: "x" }, expect: "ask" },
	{ label: "DELETE_FILE en mayúsculas", tool: "DELETE_FILE", input: { path: "x" }, expect: "ask" },
];

// --- run ------------------------------------------------------------------

const rows = [];
let failures = 0;
let gateCalls = 0;

for (const testCase of cases) {
	answer = true;
	gateCalls += 1;
	const approvedRun = await decide(testCase.tool, testCase.input);

	let passed;
	let detail;
	if (testCase.expect === "allow") {
		passed = approvedRun.asked === false && approvedRun.result === undefined;
		detail = approvedRun.asked ? `preguntó: ${approvedRun.question.title}` : "sin pregunta";
	} else {
		answer = false;
		gateCalls += 1;
		const deniedRun = await decide(testCase.tool, testCase.input);
		const blocked =
			typeof deniedRun.result === "object" &&
			deniedRun.result !== null &&
			deniedRun.result.block === true &&
			typeof deniedRun.result.reason === "string" &&
			deniedRun.result.reason.length > 0;
		const askedSanely = approvedRun.asked && approvedRun.result === undefined;
		passed = askedSanely && blocked;
		const problem = approvedRun.asked ? textProblems(testCase, approvedRun.question) : "no preguntó";
		if (problem !== null) detail = problem;
		else if (approvedRun.result !== undefined) detail = "bloqueó una llamada aprobada";
		else if (!blocked) detail = "no bloqueó al denegar";
		else detail = `preguntó: ${firstPart(approvedRun.question.message)}`;
		if (problem !== null) passed = false;
	}

	if (!passed) failures += 1;
	rows.push({ ...testCase, passed, detail });
}

console.error = realError;

// --- report ---------------------------------------------------------------

const width = Math.max(...rows.map((row) => row.label.length));
console.log(`gate-check: ${extensionPath}`);
console.log(`gate-check: HOME=${home} fuera=${outside} cwd=${sessionCwd}`);
console.log("");
console.log(`${"caso".padEnd(width)}  herramienta  esperado  resultado  decisión`);
console.log(`${"-".repeat(width)}  -----------  --------  ---------  ${"-".repeat(40)}`);
for (const row of rows) {
	const status = row.passed ? "PASS" : "FAIL";
	const expected = row.expect === "allow" ? "permitir" : "preguntar";
	console.log(
		`${row.label.padEnd(width)}  ${row.tool.padEnd(11)}  ${expected.padEnd(8)}  ${status.padEnd(9)}  ${row.detail}`,
	);
}

const passed = rows.length - failures;
console.log("");
console.log(`gate-check: ${passed}/${rows.length} casos correctos, ${failures} fallidos`);
console.log(`gate-check: ${logLines.length} líneas de auditoría para ${gateCalls} decisiones`);

const loggingOk = logLines.length === gateCalls && logLines.every((line) => line.startsWith("alter-gate: "));
if (!loggingOk) {
	console.log(`gate-check: FALLA la auditoría: esperaba ${gateCalls} líneas "alter-gate: …", encontré ${logLines.length}`);
	failures += 1;
} else {
	console.log(`gate-check: ejemplo de auditoría: ${logLines[0]}`);
}

// --- teardown -------------------------------------------------------------

if (previousHome === undefined) delete process.env.HOME;
else process.env.HOME = previousHome;
rmSync(home, { recursive: true, force: true });
rmSync(outside, { recursive: true, force: true });

if (failures > 0) {
	console.log(`gate-check: ${failures} fallo(s)`);
	process.exit(1);
}
console.log("gate-check: OK");
