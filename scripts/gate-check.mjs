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
 *
 * The table also keeps the rows of the rules the owner removed — permissions,
 * services, installers, interpreters, downloaded code and every redirection — now
 * expecting silence, so the history of the decision stays visible instead of
 * disappearing from the file.
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
touch(join(home, "datos.json"));
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

// La frase de la escalada: `sudo` y `doas` alcanzan los permisos de otro usuario
// aunque el comando de dentro no pierda nada por sí mismo.
const REASON_ESCALATION = "Va a escalar a los permisos de otro usuario.";

// La frase de la escritura en `.ssh`: ahí viven las claves que entran a los
// servidores del dueño, y una clave propia abierta de más es acceso ajeno.
const REASON_SSH_WRITE = "Va a escribir en tu carpeta .ssh, donde las claves dan acceso a tus servidores.";

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
	// `sudo` y `doas` volvieron a preguntar por sí mismos: alcanzar los permisos de
	// otro usuario no se deshace, y `sudo bash -c '…'` llegaba a un shell de root
	// sin ninguna familia que preguntara. El comando de dentro se sigue
	// clasificando, así que `sudo rm` pregunta por el borrado antes que por la
	// escalada.
	{ label: "sudo rm", ...bash("sudo rm x"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "doas rm", ...bash("doas rm x"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "sudo chmod +x (escala privilegios)", ...bash("sudo chmod +x f"), expect: "ask", reason: REASON_ESCALATION },
	{ label: "sudo ls", ...bash("sudo ls"), expect: "ask", reason: REASON_ESCALATION },
	{ label: "sudo -i", ...bash("sudo -i"), expect: "ask", reason: REASON_ESCALATION },
	{ label: "sudo bash -c", ...bash("sudo bash -c 'x'"), expect: "ask", reason: REASON_ESCALATION },
	{ label: "sudo apt install", ...bash("sudo apt install x"), expect: "ask", reason: REASON_ESCALATION },
	{ label: "doas -u root ls", ...bash("doas -u root ls"), expect: "ask", reason: REASON_ESCALATION },
	{ label: "asignación de entorno + rm", ...bash("FOO=1 rm x"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "env + rm", ...bash("env FOO=1 rm x"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "xargs rm", ...bash("find . -name x | xargs rm"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "bash -c con rm", ...bash("bash -c 'rm -rf x'"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "segundo comando de la línea", ...bash("ls && rm -rf x"), expect: "ask", reason: "Va a borrar archivos." },

	// --- bash: redirections, which no longer ask -----------------------
	// La decisión del dueño borró la familia entera: `>` y `>>` dejaron de preguntar,
	// también sobre un archivo que ya existe y sobre una ruta sensible. Lo único que
	// queda de la familia es el `mv` que sobrescribe, que sí pierde datos.
	{ label: "> sobre un archivo existente fuera de home", ...bash(`echo x > ${outside}/f`), expect: "allow" },
	{ label: "> sobre un archivo existente tipo /tmp/existente.txt", ...bash(`echo x > ${outside}/existente.txt`), expect: "allow" },
	{ label: "> sobre ~/.bashrc", ...bash("echo x > ~/.bashrc"), expect: "allow" },
	{ label: "> sobre /etc/hosts", ...bash("echo x > /etc/hosts"), expect: "allow" },
	{ label: "> con variable entrecomillada en la ruta", ...bash('echo x > "$VAR"'), expect: "allow" },
	{ label: "> sobre otro dispositivo de /dev", ...bash("echo x > /dev/sda"), expect: "allow" },
	{ label: "> sobre $SINK entrecomillado", ...bash('echo x > "$SINK"'), expect: "allow" },
	{ label: "echo x > existente (ruta relativa)", ...bash("echo x > existente.txt"), expect: "allow" },
	{ label: "> sobre un archivo existente en home", ...bash(`echo x > ${home}/existente.txt`), expect: "allow" },
	{ label: ">> sobre una ruta sensible", ...bash("echo x >> ~/.ssh/authorized_keys"), expect: "allow" },
	{ label: "> sobre un arranque de shell", ...bash("echo x > .bashrc"), expect: "allow" },
	{ label: "> con variable en la ruta", ...bash("echo x > $HOME/.ssh/authorized_keys"), expect: "allow" },
	{ label: "mv sobre un destino existente", ...bash("mv a.txt b.txt"), expect: "ask", reason: "Va a sobrescribir un archivo." },

	// --- bash: permissions, services and processes, which no longer ask ---
	// Segunda familia borrada por el dueño: los permisos, la propiedad, los procesos
	// y los servicios dejan de tener botón. Las filas quedan para que se vea que la
	// decisión fue explícita y cuál era la cobertura anterior.
	{ label: "chmod +x", ...bash("chmod +x f"), expect: "allow" },
	{ label: "chown", ...bash("chown root f"), expect: "allow" },
	{ label: "chattr", ...bash("chattr +i f"), expect: "allow" },
	{ label: "kill", ...bash("kill 123"), expect: "allow" },
	{ label: "killall", ...bash("killall node"), expect: "allow" },
	{ label: "systemctl", ...bash("systemctl restart x"), expect: "allow" },
	{ label: "service", ...bash("service x restart"), expect: "allow" },
	{ label: "shutdown", ...bash("shutdown -h now"), expect: "allow" },
	{ label: "reboot", ...bash("reboot"), expect: "allow" },

	// --- bash: package and system state, which no longer ask --------------
	// Tercera familia borrada: los instaladores del sistema y los lanzadores de
	// paquetes dejan de preguntar. La publicación detrás de un lanzador sigue
	// preguntando más abajo, porque eso sí es irreversible.
	{ label: "apt install", ...bash("apt install x"), expect: "allow" },
	{ label: "dpkg", ...bash("dpkg -r x"), expect: "allow" },
	{ label: "npm install -g", ...bash("npm install -g x"), expect: "allow" },
	{ label: "npm i -g", ...bash("npm i -g x"), expect: "allow" },
	{ label: "pip install", ...bash("pip install x"), expect: "allow" },
	{ label: "cargo install", ...bash("cargo install x"), expect: "allow" },
	{ label: "crontab", ...bash("crontab -r"), expect: "allow" },

	// --- bash: interpreters and package runners, which no longer ask -----
	// Cuarta familia borrada: el intérprete dejó de leerse. `python3 -m pip install`,
	// `python3 -c`, `node -e` y los lanzadores de paquetes ya no preguntan, porque el
	// dueño no gatea la instalación ni el código descargado. Las filas del diario de
	// producción se quedan con la expectativa nueva: son la prueba de la decisión.
	{
		label: "python3 -m pip install del diario de producción",
		...bash("cd /tmp && python3 -m pip install --user --quiet pypdf"),
		expect: "allow",
	},
	{
		label: "python3.11 -m pip install",
		...bash("python3.11 -m pip install pypdf"),
		expect: "allow",
	},
	{
		label: "python -m pip uninstall",
		...bash("python -m pip uninstall pypdf"),
		expect: "allow",
	},
	{ label: "python3 -m ensurepip", ...bash("python3 -m ensurepip"), expect: "allow" },
	{ label: "python3 -m pip install", ...bash("python3 -m pip install x"), expect: "allow" },
	{ label: "python3 -m http.server", ...bash("python3 -m http.server 8000"), expect: "allow" },
	{ label: "python3 -m venv", ...bash("python3 -m venv .venv"), expect: "allow" },
	{ label: "python3 -m pip list", ...bash("python3 -m pip list"), expect: "allow" },
	{ label: "python3 -m sin módulo", ...bash("python3 -m"), expect: "allow" },
	{ label: "python3 -c", ...bash("python3 -c 'print(1)'"), expect: "allow" },
	{ label: "python3 -c pegado a las comillas", ...bash("python3 -c'print(1)'"), expect: "allow" },
	{ label: "python3 -W ignore -c", ...bash("python3 -W ignore -c 'print(1)'"), expect: "allow" },
	{ label: "python3 -X utf8 -m pip install", ...bash("python3 -X utf8 -m pip install pypdf"), expect: "allow" },
	{ label: "node -e", ...bash("node -e 'console.log(1)'"), expect: "allow" },
	{ label: "node --eval", ...bash("node --eval 'console.log(1)'"), expect: "allow" },
	{ label: "perl -e", ...bash("perl -e 'print 1'"), expect: "allow" },
	{ label: "ruby -e", ...bash("ruby -e 'puts 1'"), expect: "allow" },
	{ label: "npx algo", ...bash("npx cowsay hola"), expect: "allow" },
	{ label: "npm exec", ...bash("npm exec cowsay"), expect: "allow" },
	{ label: "pnpm dlx", ...bash("pnpm dlx cowsay"), expect: "allow" },
	{ label: "yarn dlx", ...bash("yarn dlx cowsay"), expect: "allow" },
	{ label: "bunx", ...bash("bunx cowsay"), expect: "allow" },

	// --- bash: módulos que solo transforman datos ----------------------
	// La lista cerrada de módulos de Python desapareción con la regla de los
	// intérpretes: lo que queda es la prueba de que un módulo con archivo de salida
	// tampoco pregunta ya.
	{ label: "python3 -m json.tool de un archivo", ...bash("python3 -m json.tool < datos.json"), expect: "allow" },
	{ label: "python3 -m base64 -d", ...bash("python3 -m base64 -d datos.json"), expect: "allow" },
	{
		label: "python3 -m json.tool con archivo de salida",
		...bash("python3 -m json.tool datos.json salida.json"),
		expect: "allow",
	},

	// --- bash: lo que un intérprete no cambia ---------------------------
	// Ejecutar un script que ya existe es la misma clase que `go test`, y así se
	// decidió: la regla nueva mira lo que el intérprete recibe, no su nombre.
	{ label: "python3 script.py sigue permitido", ...bash("python3 script.py"), expect: "allow" },
	{ label: "node script.js sigue permitido", ...bash("node script.js"), expect: "allow" },
	{ label: "python3 --version", ...bash("python3 --version"), expect: "allow" },
	{ label: "node --version", ...bash("node --version"), expect: "allow" },
	{ label: "yarn install local", ...bash("yarn install"), expect: "allow" },

	// --- bash: reads that no longer need a list --------------------------
	// Con las familias de permisos, servicios e instaladores fuera, estas filas ya no
	// prueban una lista cerrada de lecturas: prueban que el gate dejó de mirar esos
	// programas. Se quedan para que la cobertura anterior siga visible.
	{ label: "find -exec ls +", ...bash("find . -name '*.go' -exec ls -l {} +"), expect: "allow" },
	{ label: "find -exec grep cerrado con ;", ...bash("find . -name '*.go' -exec grep -l x {} \\;"), expect: "allow" },
	{ label: "find -ok stat", ...bash("find . -type f -ok stat {} \\;"), expect: "allow" },
	{ label: "find -exec rm", ...bash("find . -name x -exec rm {} \\;"), expect: "ask", reason: "Va a borrar archivos." },
	// `find -exec` se clasifica por el programa que ejecuta, no por una lista: `rm`
	// pregunta y `chmod` no.
	{ label: "find -execdir chmod", ...bash("find . -name x -execdir chmod 777 {} +"), expect: "allow" },
	{ label: "find -exec sh", ...bash("find . -name x -exec sh -c 'rm x' {} \\;"), expect: "ask", reason: "Va a borrar archivos." },
	{ label: "systemctl --user status", ...bash("systemctl --user status alter.service"), expect: "allow" },
	{ label: "systemctl --no-pager list-units", ...bash("systemctl --no-pager list-units"), expect: "allow" },
	{ label: "systemctl is-active", ...bash("systemctl is-active alter.service"), expect: "allow" },
	{ label: "systemctl --version", ...bash("systemctl --version"), expect: "allow" },
	{ label: "systemctl --user start", ...bash("systemctl --user start alter.service"), expect: "allow" },
	{ label: "systemctl daemon-reload", ...bash("systemctl daemon-reload"), expect: "allow" },
	{ label: "crontab -l", ...bash("crontab -l"), expect: "allow" },
	{ label: "crontab -u root -l", ...bash("crontab -u root -l"), expect: "allow" },
	{ label: "crontab -e", ...bash("crontab -e"), expect: "allow" },
	{ label: "crontab con archivo", ...bash("crontab tareas.txt"), expect: "allow" },
	{ label: "apt list", ...bash("apt list --installed"), expect: "allow" },
	{ label: "apt search", ...bash("apt search ripgrep"), expect: "allow" },
	{ label: "apt show", ...bash("apt show alter"), expect: "allow" },
	{ label: "apt-get policy", ...bash("apt-get policy x"), expect: "allow" },
	{ label: "apt-get -y install", ...bash("apt-get -y install x"), expect: "allow" },
	{ label: "apt-get -o ... remove", ...bash("apt-get -o Debug::NoLocking=1 remove x"), expect: "allow" },
	{ label: "apt-get dist-upgrade", ...bash("apt-get dist-upgrade"), expect: "allow" },
	{ label: "dpkg -l", ...bash("dpkg -l"), expect: "allow" },
	{ label: "dpkg -L con paquete", ...bash("dpkg -L alter"), expect: "allow" },
	{ label: "dpkg --list", ...bash("dpkg --list"), expect: "allow" },
	{ label: "dpkg -s", ...bash("dpkg -s alter"), expect: "allow" },
	{ label: "dpkg -i", ...bash("dpkg -i x.deb"), expect: "allow" },
	{ label: "kill -0 como sonda", ...bash("kill -0 123"), expect: "allow" },
	{ label: "kill -l", ...bash("kill -l"), expect: "allow" },
	{ label: "kill -s 0", ...bash("kill -s 0 123"), expect: "allow" },
	{ label: "kill -9", ...bash("kill -9 123"), expect: "allow" },
	{ label: "kill -s TERM", ...bash("kill -s TERM 123"), expect: "allow" },
	{ label: "pkill", ...bash("pkill node"), expect: "allow" },

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

	// --- bash: publicar es irreversible ---------------------------------
	// Publicar un paquete, una imagen o una versión no se deshace: el número
	// queda reclamado en el registro para siempre. El gate pedía botón para
	// borrar, sobrescribir y escalar, pero no para publicar, así que `npm
	// publish` pasaba sin pregunta. Cada familia pregunta con su consecuencia, y
	// las lecturas de la misma familia siguen pasando por lista cerrada.
	{ label: "npm publish", ...bash("npm publish"), expect: "ask", reason: "Va a publicar un paquete de forma irreversible." },
	{
		label: "npm publish --access public",
		...bash("npm publish --access public"),
		expect: "ask",
		reason: "Va a publicar un paquete de forma irreversible.",
	},
	// Las formas del lanzador siguen preguntando porque lo que ejecutan es una
	// publicación: el botón ya no es por descargar el paquete, sino por publicarlo.
	{
		label: "npx npm publish",
		...bash("npx npm publish"),
		expect: "ask",
		reason: "Va a publicar un paquete de forma irreversible.",
	},
	{
		label: "npm exec npm publish",
		...bash("npm exec npm publish"),
		expect: "ask",
		reason: "Va a publicar un paquete de forma irreversible.",
	},
	{ label: "yarn publish", ...bash("yarn publish"), expect: "ask", reason: "Va a publicar un paquete de forma irreversible." },
	{ label: "pnpm publish", ...bash("pnpm publish"), expect: "ask", reason: "Va a publicar un paquete de forma irreversible." },
	{ label: "bun publish", ...bash("bun publish"), expect: "ask", reason: "Va a publicar un paquete de forma irreversible." },
	{ label: "cargo publish", ...bash("cargo publish"), expect: "ask", reason: "Va a publicar un paquete de forma irreversible." },
	{
		label: "gem push",
		...bash("gem push alter-0.1.0.gem"),
		expect: "ask",
		reason: "Va a publicar un paquete de forma irreversible.",
	},
	{
		label: "twine upload",
		...bash("twine upload dist/alter-0.1.0.tar.gz"),
		expect: "ask",
		reason: "Va a publicar un paquete de forma irreversible.",
	},
	{
		label: "helm push",
		...bash("helm push alter-0.1.0.tgz oci://registry.example.com/charts"),
		expect: "ask",
		reason: "Va a publicar un paquete de forma irreversible.",
	},
	{
		label: "gh release create v1.0",
		...bash("gh release create v1.0"),
		expect: "ask",
		reason: "Va a publicar o borrar una versión de forma irreversible.",
	},
	{
		label: "gh release delete",
		...bash("gh release delete v1.0"),
		expect: "ask",
		reason: "Va a publicar o borrar una versión de forma irreversible.",
	},
	{
		label: "docker push user/img:tag",
		...bash("docker push user/img:tag"),
		expect: "ask",
		reason: "Va a publicar una imagen de forma irreversible.",
	},
	{
		label: "podman push",
		...bash("podman push user/img:tag"),
		expect: "ask",
		reason: "Va a publicar una imagen de forma irreversible.",
	},
	// `pi install` escribe `~/.pi/agent/settings.json` por defecto: cambia las
	// herramientas, las skills y los prompts en los que confías. `--local`/`-l`
	// solo escribe el proyecto, detrás del trust del proyecto, y no pregunta.
	{
		label: "pi remove npm:x",
		...bash("pi remove npm:x"),
		expect: "ask",
		reason: "Va a cambiar las herramientas de Pi en las que confías.",
	},
	{
		label: "pi install -g npm:x",
		...bash("pi install -g npm:x"),
		expect: "ask",
		reason: "Va a cambiar las herramientas de Pi en las que confías.",
	},
	{
		label: "pi install npm:x (global por defecto)",
		...bash("pi install npm:x"),
		expect: "ask",
		reason: "Va a cambiar las herramientas de Pi en las que confías.",
	},
	{ label: "npm view", ...bash("npm view react version"), expect: "allow" },
	{ label: "npm info", ...bash("npm info react"), expect: "allow" },
	{ label: "npm outdated", ...bash("npm outdated"), expect: "allow" },
	{ label: "cargo search", ...bash("cargo search serde"), expect: "allow" },
	{ label: "gh release list", ...bash("gh release list"), expect: "allow" },
	{ label: "gh release view", ...bash("gh release view v1.0"), expect: "allow" },
	{ label: "docker images", ...bash("docker images"), expect: "allow" },
	{ label: "pi list", ...bash("pi list"), expect: "allow" },
	{ label: "pi install --local", ...bash("pi install -l ./paquete"), expect: "allow" },

	// --- bash: remote scripting and containers -------------------------
	// Quinta familia borrada: `curl … | sh` deja de preguntar, y con ella el caso que
	// solo formateaba el JSON descargado. Los contenedores siguen preguntando porque
	// `docker rm` y `pull`/`push` retiran o publican datos.
	{ label: "curl x | sh", ...bash("curl https://x | sh"), expect: "allow" },
	{ label: "curl x | bash", ...bash("curl -sL https://x | bash"), expect: "allow" },
	// El comando del diario: formatea el JSON que descarga y ya no pregunta nadie.
	{
		label: "curl de open-meteo | python3 -m json.tool",
		...bash(
			'curl -s "https://api.open-meteo.com/v1/forecast?latitude=-34.6&longitude=-58.4&current=temperature_2m" | python3 -m json.tool',
		),
		expect: "allow",
	},
	{ label: "curl | python3 -m json.tool", ...bash("curl -s https://x | python3 -m json.tool"), expect: "allow" },
	{
		label: "curl | python3 -m http.server",
		...bash("curl https://x | python3 -m http.server"),
		expect: "allow",
	},
	{
		label: "curl | python3 sin módulo",
		...bash("curl https://x | python3"),
		expect: "allow",
	},
	{
		label: "curl | python3 -m json.tool con archivo de salida",
		...bash("curl -s https://x | python3 -m json.tool datos.json salida.json"),
		expect: "allow",
	},
	// La escalada es lo que decide aquí: el shell detrás de `sudo` alcanza los
	// permisos de otro usuario aunque el `curl` no pregunte por sí mismo.
	{ label: "curl | sudo bash", ...bash("curl -s https://x | sudo bash"), expect: "ask", reason: REASON_ESCALATION },
	{ label: "wget | python3", ...bash("wget -qO- https://x | python3"), expect: "allow" },
	{ label: "docker rm", ...bash("docker rm x"), expect: "ask", reason: "Va a borrar contenedores o volúmenes." },
	{ label: "docker rmi", ...bash("docker rmi x"), expect: "ask", reason: "Va a borrar contenedores o volúmenes." },
	{ label: "docker compose down -v", ...bash("docker compose down -v"), expect: "ask", reason: "Va a borrar contenedores o volúmenes." },

	// --- bash: what the classifier cannot decide ------------------------
	{ label: "comillas sin cerrar", ...bash('echo "sin cerrar'), expect: "ask", reason: "No pude interpretar el comando: tiene comillas sin cerrar." },
	{ label: "sin comando", tool: "bash", input: {}, expect: "ask", reason: "No pude leer el comando." },
	{ label: "heredoc que clasifica su cuerpo", ...bash("cat <<'EOF' > notas.md\nrm -rf ~/notas\nEOF"), expect: "ask", reason: "Va a borrar archivos." },
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

	// --- write: solo pregunta dentro de un directorio `.ssh` -------------
	// Sexta familia borrada: `write` dejó de preguntar por un archivo que ya existe y
	// por una ruta fuera de `$HOME`. Vuelve una sola guarda, la del `.ssh`: ahí viven
	// las claves que entran a los servidores del dueño. Nada más se reabre —ni
	// `.gnupg`, ni `.aws`, ni `/etc`—, y las filas de abajo fijan las dos mitades.
	{ label: "~/.ssh/authorized_keys", tool: "write", input: { path: "~/.ssh/authorized_keys", content: "x" }, expect: "ask", reason: REASON_SSH_WRITE },
	{ label: "~/.ssh/config", tool: "write", input: { path: "~/.ssh/config", content: "x" }, expect: "ask", reason: REASON_SSH_WRITE },
	{
		label: "$HOME/.ssh/authorized_keys",
		tool: "write",
		input: { path: "$HOME/.ssh/authorized_keys", content: "x" },
		expect: "ask",
		reason: REASON_SSH_WRITE,
	},
	{
		label: "ruta absoluta con /.ssh/",
		tool: "write",
		input: { path: join(outside, ".ssh", "authorized_keys"), content: "x" },
		expect: "ask",
		reason: REASON_SSH_WRITE,
	},
	{ label: "ruta relativa que cae en .ssh", tool: "write", input: { path: ".ssh/config", content: "x" }, expect: "ask", reason: REASON_SSH_WRITE },
	// El resto de rutas sigue en silencio: el archivo normal, el vault, /tmp y
	// también lo que se parece a `.ssh` sin serlo, más los otros sitios sensibles
	// que el dueño NO quiso reabrir.
	{ label: "archivo nuevo en home", tool: "write", input: { path: join(home, "nuevo.txt"), content: "x" }, expect: "allow" },
	{ label: "archivo nuevo con ~", tool: "write", input: { path: "~/tilde-nuevo.txt", content: "x" }, expect: "allow" },
	{ label: "archivo nuevo con ruta relativa", tool: "write", input: { path: "sub/dir/nuevo.txt", content: "x" }, expect: "allow" },
	{ label: "archivo existente en home", tool: "write", input: { path: join(home, "existente.txt"), content: "x" }, expect: "allow" },
	{ label: "archivo nuevo fuera de home", tool: "write", input: { path: join(outside, "nuevo.txt"), content: "x" }, expect: "allow" },
	{ label: "ruta relativa que sale de home", tool: "write", input: { path: "../fuera.txt", content: "x" }, expect: "allow" },
	{ label: "~user que no puedo expandir", tool: "write", input: { path: "~root/x", content: "x" }, expect: "allow" },
	{ label: "nota en el vault", tool: "write", input: { path: join(home, "vault", "notas", "2026-10-30.md"), content: "x" }, expect: "allow" },
	{ label: "archivo normal en /tmp", tool: "write", input: { path: join(tmpdir(), "alter-gate-normal.txt"), content: "x" }, expect: "allow" },
	{ label: "~/.ssh-otra/clave no es .ssh", tool: "write", input: { path: "~/.ssh-otra/clave", content: "x" }, expect: "allow" },
	{ label: "~/.gnupg no se reabre", tool: "write", input: { path: "~/.gnupg/gpg.conf", content: "x" }, expect: "allow" },
	{ label: "~/.aws no se reabre", tool: "write", input: { path: "~/.aws/credentials", content: "x" }, expect: "allow" },
	{ label: "/etc no se reabre", tool: "write", input: { path: "/etc/sudoers.d/alter", content: "x" }, expect: "allow" },

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
// What the owner chose: how many cases the gate lets through and how many it still
// stops for. The split is part of the report because the whole point of the change
// was to shrink the second number without losing a family the owner kept.
const allowCases = rows.filter((row) => row.expect === "allow").length;
const askCases = rows.length - allowCases;
console.log("");
console.log(`gate-check: ${passed}/${rows.length} casos correctos, ${failures} fallidos`);
console.log(`gate-check: ${allowCases} casos permiten sin preguntar, ${askCases} casos preguntan`);
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
