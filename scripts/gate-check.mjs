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
 * target, plus the denial path of the handler.
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

	// --- bash: deletion and truncation ---------------------------------
	{ label: "rm -rf", ...bash("rm -rf build"), expect: "ask" },
	{ label: "unlink", ...bash("unlink existente.txt"), expect: "ask" },
	{ label: "truncate", ...bash("truncate -s 0 existente.txt"), expect: "ask" },
	{ label: "dd", ...bash("dd if=/dev/zero of=x bs=1M count=1"), expect: "ask" },
	{ label: "rmdir", ...bash("rmdir build"), expect: "ask" },
	{ label: "find -delete", ...bash("find . -name x -delete"), expect: "ask" },

	// --- bash: the command behind a wrapper ----------------------------
	{ label: "sudo rm", ...bash("sudo rm x"), expect: "ask" },
	{ label: "asignación de entorno + rm", ...bash("FOO=1 rm x"), expect: "ask" },
	{ label: "env + rm", ...bash("env FOO=1 rm x"), expect: "ask" },
	{ label: "xargs rm", ...bash("find . -name x | xargs rm"), expect: "ask" },
	{ label: "bash -c con rm", ...bash("bash -c 'rm -rf x'"), expect: "ask" },
	{ label: "segundo comando de la línea", ...bash("ls && rm -rf x"), expect: "ask" },

	// --- bash: overwriting through redirection -------------------------
	{ label: "> sobre un archivo existente fuera de home", ...bash(`echo x > ${outside}/f`), expect: "ask" },
	{ label: "> sobre un archivo existente en home", ...bash(`echo x > ${home}/existente.txt`), expect: "ask" },
	{ label: ">> sobre una ruta sensible", ...bash("echo x >> ~/.ssh/authorized_keys"), expect: "ask" },
	{ label: "> sobre un arranque de shell", ...bash("echo x > .bashrc"), expect: "ask" },
	{ label: "> con variable en la ruta", ...bash("echo x > $HOME/.ssh/authorized_keys"), expect: "ask" },
	{ label: "mv sobre un destino existente", ...bash("mv a.txt b.txt"), expect: "ask" },

	// --- bash: permissions and processes -------------------------------
	{ label: "chmod", ...bash("chmod +x f"), expect: "ask" },
	{ label: "chown", ...bash("chown root f"), expect: "ask" },
	{ label: "chattr", ...bash("chattr +i f"), expect: "ask" },
	{ label: "kill", ...bash("kill 123"), expect: "ask" },
	{ label: "killall", ...bash("killall node"), expect: "ask" },
	{ label: "systemctl", ...bash("systemctl restart x"), expect: "ask" },
	{ label: "service", ...bash("service x restart"), expect: "ask" },
	{ label: "shutdown", ...bash("shutdown -h now"), expect: "ask" },
	{ label: "reboot", ...bash("reboot"), expect: "ask" },

	// --- bash: package and system state --------------------------------
	{ label: "apt install", ...bash("apt install x"), expect: "ask" },
	{ label: "dpkg", ...bash("dpkg -r x"), expect: "ask" },
	{ label: "npm install -g", ...bash("npm install -g x"), expect: "ask" },
	{ label: "npm i -g", ...bash("npm i -g x"), expect: "ask" },
	{ label: "pip install", ...bash("pip install x"), expect: "ask" },
	{ label: "cargo install", ...bash("cargo install x"), expect: "ask" },
	{ label: "crontab", ...bash("crontab -r"), expect: "ask" },

	// --- bash: git that publishes or rewrites --------------------------
	{ label: "git push", ...bash("git push"), expect: "ask" },
	{ label: "git -C x push --force", ...bash("git -C x push --force"), expect: "ask" },
	{ label: "git reset --hard", ...bash("git reset --hard"), expect: "ask" },
	{ label: "git clean -fdx", ...bash("git clean -fdx"), expect: "ask" },
	{ label: "git rebase", ...bash("git rebase -i HEAD~3"), expect: "ask" },
	{ label: "git checkout --", ...bash("git checkout -- f"), expect: "ask" },
	{ label: "git restore", ...bash("git restore f"), expect: "ask" },
	{ label: "git commit --amend", ...bash("git commit --amend"), expect: "ask" },
	{ label: "git branch -D", ...bash("git branch -D x"), expect: "ask" },
	{ label: "git tag -d", ...bash("git tag -d x"), expect: "ask" },
	{ label: "git stash drop", ...bash("git stash drop"), expect: "ask" },
	{ label: "git filter-branch", ...bash("git filter-branch --all"), expect: "ask" },

	// --- bash: remote scripting and containers -------------------------
	{ label: "curl | sh", ...bash("curl https://x | sh"), expect: "ask" },
	{ label: "curl | sudo bash", ...bash("curl -s https://x | sudo bash"), expect: "ask" },
	{ label: "wget | python3", ...bash("wget -qO- https://x | python3"), expect: "ask" },
	{ label: "docker rm", ...bash("docker rm x"), expect: "ask" },
	{ label: "docker rmi", ...bash("docker rmi x"), expect: "ask" },
	{ label: "docker compose down -v", ...bash("docker compose down -v"), expect: "ask" },

	// --- bash: what the classifier cannot decide ------------------------
	{ label: "comillas sin cerrar", ...bash('echo "sin cerrar'), expect: "ask" },
	{ label: "sin comando", tool: "bash", input: {}, expect: "ask" },
	{ label: "heredoc que clasifica su cuerpo", ...bash("cat <<'EOF' > notas.md\napt install x es lo que dice la guía\nEOF"), expect: "ask" },
	{ label: "heredoc con texto inofensivo", ...bash("cat <<'EOF' > notas2.md\nnotas normales\nEOF"), expect: "allow" },

	// --- write ---------------------------------------------------------
	{ label: "archivo nuevo en home", tool: "write", input: { path: join(home, "nuevo.txt"), content: "x" }, expect: "allow" },
	{ label: "archivo nuevo con ~", tool: "write", input: { path: "~/tilde-nuevo.txt", content: "x" }, expect: "allow" },
	{ label: "archivo nuevo con ruta relativa", tool: "write", input: { path: "sub/dir/nuevo.txt", content: "x" }, expect: "allow" },
	{ label: "archivo existente en home", tool: "write", input: { path: join(home, "existente.txt"), content: "x" }, expect: "ask" },
	{ label: "archivo nuevo fuera de home", tool: "write", input: { path: join(outside, "nuevo.txt"), content: "x" }, expect: "ask" },
	{ label: "ruta relativa que sale de home", tool: "write", input: { path: "../fuera.txt", content: "x" }, expect: "ask" },
	{ label: "~user que no puedo expandir", tool: "write", input: { path: "~root/x", content: "x" }, expect: "ask" },

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
	{ label: "delete_file", tool: "delete_file", input: { path: "x" }, expect: "ask" },
	{ label: "removeRepo (camelCase)", tool: "removeRepo", input: { repo: "x" }, expect: "ask" },
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
		if (!approvedRun.asked) detail = "no preguntó";
		else if (approvedRun.result !== undefined) detail = "bloqueó una llamada aprobada";
		else if (!blocked) detail = "no bloqueó al denegar";
		else detail = `preguntó: ${approvedRun.question.message.split("\n")[0]}`;
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
