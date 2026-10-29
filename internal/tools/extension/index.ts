/**
 * Pi extension: Alter's identity block, the approval gate for destructive tools
 * and the two deterministic Alter tools (S7, T12, T13, T17 and T22 of the phase-1
 * Telegram bridge).
 *
 * The gate intercepts `tool_call` and asks the owner only for what cannot be
 * undone: `edit` never asks, `write` asks when the target already exists or sits
 * outside `$HOME`, and `bash` asks only when the command can destroy, publish or
 * escalate. The read-only tools and the two Alter tools never ask, and a tool the
 * gate has no rule for still asks. The question is `ctx.ui.confirm()`, which in
 * RPC mode the bridge turns into two Telegram buttons; no answer means blocked.
 *
 * The two tools exist for what Pi's own toolset cannot reach, not as a second way
 * to do what it already does: they deliver a vault file to the owner's chat and
 * ask the owner to choose between options. Everything else, writing the vault
 * included, goes through Pi's own `bash`, `read`, `write`, `edit`, `grep`, `find`
 * and `ls`, so there is no note-writing tool here (T17).
 *
 * The two tools POST to the bridge's loopback tools server, which they find
 * through two variables:
 *
 *   ALTER_TOOLS_URL    base URL, for example http://127.0.0.1:43117
 *   ALTER_TOOLS_TOKEN  per-process secret sent as the X-Alter-Token header
 *
 * Both are set by the bridge in Pi's environment; the model never chooses them
 * and never sees the token. They are additive on top of Pi's own tools: the
 * bridge does not restrict the toolset (S7), so `bash`, `read`, `write`, `edit`,
 * `grep`, `find`, `ls` and whatever the loaded packages provide are still there.
 *
 * The extension is loaded by the bridge with `-e`, next to the persona file that
 * `--append-system-prompt` injects.
 *
 * Dependency-free on purpose: only `node:fs`/`node:path` and the global `fetch`
 * are used, so the extension works without installing anything into the Pi
 * package.
 */

import { existsSync, readFileSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { basename, dirname, join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type Json = Record<string, unknown>;

/** Result shape Pi expects from a tool: model-facing text plus structured details. */
type ToolResult = {
	content: { type: "text"; text: string }[];
	details: Json;
	isError?: boolean;
};

/**
 * Plain JSON Schema for each tool, mirroring the contract table in
 * `odd/tasks/telegram-bridge.md`. They are typed as `any` because Pi's
 * `parameters` expect a TypeBox `TSchema` and this file must not import TypeBox;
 * the server is the authority that validates the payload.
 */
const SEND_FILE_PARAMETERS: any = {
	type: "object",
	properties: {
		path: {
			type: "string",
			description: "Ruta de un archivo que ya está en el vault, relativa a su raíz. No puede salir del vault.",
		},
		caption: {
			type: "string",
			description: "Texto opcional que acompaña al archivo en Telegram.",
		},
	},
	required: ["path"],
	additionalProperties: false,
};

const ASK_OPTIONS_PARAMETERS: any = {
	type: "object",
	properties: {
		question: {
			type: "string",
			description: "Pregunta breve que se envía al dueño junto a los botones.",
		},
		options: {
			type: "array",
			items: { type: "string" },
			minItems: 2,
			maxItems: 4,
			description: "Entre 2 y 4 opciones seleccionables.",
		},
	},
	required: ["question", "options"],
	additionalProperties: false,
};

/** Successful tool result carrying the model-facing text. */
function ok(text: string, details: Json = {}): ToolResult {
	return { content: [{ type: "text", text }], details, isError: false };
}

/**
 * Tools that never need approval (T13, T22). The read-only built-ins cannot change
 * anything, the two deterministic Alter tools are validated by the bridge against
 * the vault and the owner chat, and `edit` is a surgical change: it applies only
 * where its `oldText` matches, so the diff is the review. Asking for it was a toll
 * booth, not a safeguard.
 */
const APPROVAL_FREE = new Set([
	"read",
	"grep",
	"find",
	"ls",
	"edit",
	"send_file",
	"ask_options",
]);

/** Longest argument preview the approval prompt carries; well under the bridge's 280-rune cap. */
const APPROVAL_ARG_LIMIT = 240;

/**
 * Budget for reason plus preview together. The bridge clips a dialog message at
 * 280 runes keeping both ends, so staying below that keeps its clip from cutting
 * the tail of the command this prompt exists to show.
 */
const APPROVAL_MESSAGE_LIMIT = 260;

/** Preview length on the audit line: the journal wants a hint, not the whole command. */
const LOG_ARG_LIMIT = 160;

/**
 * Cuts a long preview down to its head AND its tail. The dangerous part of a
 * long command tends to sit at the end (`... && rm -rf ~/x`), so a head-only cut
 * would hide exactly what the approval exists to show. Two thirds go to the head
 * and one third to the tail; the bridge applies the same split with a wider cap,
 * so its own truncation can never re-cut the tail preserved here.
 */
function previewWithTail(rendered: string, limit: number): string {
	if (rendered.length <= limit) {
		return rendered;
	}
	const head = Math.floor((limit * 2) / 3);
	const tail = limit - head - 1; // one character for the ellipsis
	return `${rendered.slice(0, head)}…${rendered.slice(rendered.length - tail)}`;
}

/**
 * Renders the arguments of a tool call as one short line for the approval prompt.
 * The bridge escapes it and applies its own wider cap, so this only keeps the
 * request small while keeping both ends of a long command visible.
 */
function previewArguments(input: unknown, limit: number = APPROVAL_ARG_LIMIT): string {
	let rendered: string;
	try {
		rendered = typeof input === "object" && input !== null ? JSON.stringify(input) : String(input);
	} catch {
		rendered = "[argumentos no representables]";
	}
	rendered = (rendered ?? "").replace(/\s+/g, " ").trim();
	return previewWithTail(rendered, limit);
}

/**
 * Text of the approval prompt: the reason first, then the arguments, sized so the
 * bridge never has to clip the result. The reason is what tells the owner which
 * rule fired (`git push publica en el remoto`), so it goes first and never gets
 * cut.
 */
function approvalMessage(reason: string, input: unknown): string {
	const budget = Math.max(80, APPROVAL_MESSAGE_LIMIT - reason.length - 1);
	return `${reason}\n${previewArguments(input, budget)}`;
}

/** One verdict per tool call: allowed silently, or asked with the rule that fired. */
type Verdict = { ask: boolean; reason: string };

function allow(): Verdict {
	return { ask: false, reason: "" };
}

function ask(reason: string): Verdict {
	return { ask: true, reason };
}

/**
 * Home directory as the gate sees it: `$HOME` first, because that is what the
 * shell and Pi expand `~` against, with the OS answer as fallback. Read per call,
 * never cached, so a changed environment is honoured immediately.
 */
function homeDir(): string {
	const fromEnv = (process.env.HOME ?? "").trim();
	return fromEnv.length > 0 ? fromEnv : homedir();
}

/**
 * Resolves a path the way Pi's own tools do (`resolveToCwd` in the package): a
 * leading `@` is dropped, `~` and `~/x` expand against `$HOME`, and anything else
 * resolves against the session's working directory.
 *
 * Returns null when the path cannot be resolved honestly — `~user` (Pi does not
 * expand it either, so resolving it would describe a different file), an empty
 * string, or a NUL byte. The caller asks instead of guessing.
 */
function resolveUserPath(raw: string, cwd: string): string | null {
	let value = raw.trim();
	if (value.length === 0 || value.includes("\0")) return null;
	if (value.startsWith("@")) value = value.slice(1);
	if (value === "~") return homeDir();
	if (value.startsWith("~/")) return resolve(homeDir(), value.slice(2));
	if (value.startsWith("~")) return null;
	return resolve(cwd.length > 0 ? cwd : process.cwd(), value);
}

/**
 * Paths where a write is an escalation even inside `$HOME`, or a system area where
 * one write can break the machine: shell start-up files, keys, autostart, PATH
 * directories and the usual roots. Used for shell redirections, whose target the
 * shell expands and whose effect the owner cannot see from the command alone.
 */
const SENSITIVE_HOME_PATHS = [
	".ssh",
	".gnupg",
	".config/autostart",
	".gitconfig",
	".bashrc",
	".bash_profile",
	".profile",
	".zshrc",
	".zprofile",
	".local/bin",
];

const SENSITIVE_ROOT_PATHS = [
	"/etc",
	"/root",
	"/boot",
	"/usr",
	"/bin",
	"/sbin",
	"/lib",
	"/lib64",
	"/opt",
	"/var",
	"/srv",
	"/sys",
	"/proc",
	"/dev",
];

function isSensitivePath(absolutePath: string): boolean {
	const home = homeDir();
	for (const relative of SENSITIVE_HOME_PATHS) {
		const prefix = resolve(home, relative);
		if (absolutePath === prefix || absolutePath.startsWith(prefix + sep)) return true;
	}
	for (const root of SENSITIVE_ROOT_PATHS) {
		if (absolutePath === root || absolutePath.startsWith(root + "/")) return true;
	}
	return false;
}

/** True when the path sits outside the owner's home: no rule covers it, so it asks. */
function isOutsideHome(absolutePath: string): boolean {
	const home = homeDir();
	return !(absolutePath === home || absolutePath.startsWith(home + sep));
}

/** Path inside a file, or false when it is a directory or unreadable. */
function isDirectory(absolutePath: string): boolean {
	try {
		return statSync(absolutePath).isDirectory();
	} catch {
		return false;
	}
}

/** Filesystem path as the owner knows it: `~/x` inside home, absolute outside, capped. */
function displayPath(absolutePath: string): string {
	const home = homeDir();
	const shown = absolutePath === home ? "~" : absolutePath.startsWith(home + sep) ? `~/${absolutePath.slice(home.length + 1)}` : absolutePath;
	return shown.length <= 60 ? shown : `${shown.slice(0, 57)}…`;
}

/** Contents of a path field of a tool call, or null when the field is missing. */
function readPathField(input: unknown): string | null {
	if (typeof input !== "object" || input === null) return null;
	const value = (input as Json).path;
	return typeof value === "string" ? value : null;
}

/** Failed tool result: the model sees the reason and must not retry blindly. */
function failed(text: string): ToolResult {
	return { content: [{ type: "text", text }], details: {}, isError: true };
}

/**
 * Classifier for `bash` (T22). Permission is for what cannot be undone, so the gate
 * asks when the command deletes, truncates, rewrites history, publishes, installs
 * system state or escalates privileges, and stays quiet for the rest (`ls`, `cat`,
 * `grep`, `go test`, `git status`, `git commit`...).
 *
 * Two rules keep it from being decorative:
 *
 *   - it matches every simple command of the line, not the first token, because
 *     `git -C x push`, `sudo rm` and `FOO=1 rm -rf` are the same act as `git push`
 *     and `rm`;
 *   - what it cannot decide, it asks. An unbalanced quote, a path it cannot
 *     resolve, a `~user` it cannot expand: all of them ask, never fail open.
 */

/** One shell word, one operator or one redirection. */
type Token = { kind: "word" | "op" | "redir"; value: string };

/** One simple command of a line: its words, its redirections and the operator before it. */
type SimpleCommand = {
	words: string[];
	redirs: { op: string; target: string }[];
	afterOp: string | null;
};

/**
 * Splits a command line into words, operators and redirections, honouring single
 * quotes, double quotes and backslash escapes so that `echo "a > b"` is not read as
 * a redirection. Returns null when the quoting never closes: the line cannot be
 * classified, and the call asks.
 *
 * Command substitutions and subshells (`$(`, backticks, parentheses) are treated as
 * separators, so the command inside them is classified too instead of hiding behind
 * the outer one.
 */
function tokenize(command: string): Token[] | null {
	const tokens: Token[] = [];
	let word = "";
	let inWord = false;
	let quote: '"' | "'" | null = null;
	let index = 0;

	const pushWord = () => {
		if (!inWord) return;
		tokens.push({ kind: "word", value: word });
		word = "";
		inWord = false;
	};
	// A file descriptor glued to a redirection (`2>`, `1>>`) is not an argument.
	const dropFd = () => {
		if (inWord && /^\d+$/.test(word)) {
			word = "";
			inWord = false;
		}
	};

	while (index < command.length) {
		const char = command[index];

		if (quote === "'") {
			if (char === "'") quote = null;
			else word += char;
			inWord = true;
			index += 1;
			continue;
		}
		if (quote === '"') {
			if (char === "\\" && index + 1 < command.length) {
				word += command[index + 1];
				index += 2;
			} else {
				if (char === '"') quote = null;
				else word += char;
				index += 1;
			}
			inWord = true;
			continue;
		}

		if (char === "\\" && index + 1 < command.length) {
			word += command[index + 1];
			inWord = true;
			index += 2;
			continue;
		}
		if (char === "'" || char === '"') {
			quote = char;
			inWord = true;
			index += 1;
			continue;
		}
		if (char === " " || char === "\t" || char === "\r") {
			pushWord();
			index += 1;
			continue;
		}
		if (char === "\n") {
			pushWord();
			tokens.push({ kind: "op", value: "\n" });
			index += 1;
			continue;
		}
		if (char === "#" && !inWord) {
			while (index < command.length && command[index] !== "\n") index += 1;
			continue;
		}

		const pair = command.slice(index, index + 2);
		if (pair === "&&" || pair === "||" || pair === "|&") {
			pushWord();
			tokens.push({ kind: "op", value: pair });
			index += 2;
			continue;
		}
		if (char === "|" || char === ";" || char === "&" || char === "(" || char === ")" || char === "`") {
			pushWord();
			tokens.push({ kind: "op", value: char });
			index += 1;
			continue;
		}
		if (char === ">") {
			dropFd();
			pushWord();
			index += 1;
			let op = ">";
			if (command[index] === ">") {
				op = ">>";
				index += 1;
			}
			if (command[index] === "&") {
				op += "&";
				index += 1;
			}
			tokens.push({ kind: "redir", value: op });
			continue;
		}
		if (char === "<") {
			dropFd();
			pushWord();
			index += 1;
			let op = "<";
			if (command[index] === "<") {
				op = "<<";
				index += 1;
				if (command[index] === "<") {
					op = "<<<";
					index += 1;
				}
			}
			tokens.push({ kind: "redir", value: op });
			continue;
		}

		word += char;
		inWord = true;
		index += 1;
	}

	if (quote !== null) return null;
	pushWord();
	return tokens;
}

/** Groups a token list into simple commands, keeping the operator that preceded each one. */
function splitCommands(tokens: Token[]): SimpleCommand[] {
	const commands: SimpleCommand[] = [];
	let words: string[] = [];
	let redirs: { op: string; target: string }[] = [];
	let afterOp: string | null = null;
	let pendingOp: string | null = null;
	let pendingRedir: string | null = null;
	let started = false;

	const begin = () => {
		if (started) return;
		afterOp = pendingOp;
		pendingOp = null;
		started = true;
	};
	const flush = () => {
		if (words.length > 0 || redirs.length > 0) commands.push({ words, redirs, afterOp });
		words = [];
		redirs = [];
		afterOp = null;
		pendingRedir = null;
		started = false;
	};

	for (const token of tokens) {
		if (token.kind === "op") {
			flush();
			pendingOp = token.value;
			continue;
		}
		if (token.kind === "redir") {
			begin();
			pendingRedir = token.value;
			continue;
		}
		if (pendingRedir !== null) {
			const op = pendingRedir;
			pendingRedir = null;
			begin();
			// `>&2` duplicates a file descriptor, `<` reads, `<<`/`<<<` take a
			// delimiter or a string: none of them writes a file worth checking.
			if (!op.endsWith("&") && op !== "<" && op !== "<<" && op !== "<<<") {
				redirs.push({ op, target: token.value });
			}
			continue;
		}
		begin();
		words.push(token.value);
	}
	flush();
	return commands;
}

/** Shells that run inline code with `-c` and can be the reader of a download pipe. */
const SHELLS = new Set(["sh", "bash", "zsh", "dash", "ksh", "fish", "ash", "busybox"]);

/** Interpreters that execute a piped script just as a shell would. */
const INTERPRETERS = new Set(["python", "python3", "perl", "ruby", "node", "php"]);

/**
 * Programs that only run the command after them. The program is skipped and the
 * inner command classified, so `env FOO=1 rm x` and `xargs rm` hit the `rm` rule.
 * `sudo`/`doas` are listed so the download-pipe check can see the shell behind
 * them; the escalation itself is caught before this set is consulted.
 */
const TRANSPARENT_WRAPPERS = new Set([
	"env",
	"command",
	"builtin",
	"exec",
	"nohup",
	"time",
	"nice",
	"ionice",
	"stdbuf",
	"xargs",
	"timeout",
	"sudo",
	"doas",
]);

/** Flags that swallow the next token, so a wrapper's value is not read as a command. */
const WRAPPER_VALUE_FLAGS = new Set([
	"-u",
	"--unset",
	"-n",
	"--adjustment",
	"-I",
	"--replace",
	"-s",
	"--signal",
	"-d",
	"--delimiter",
	"-P",
	"--max-procs",
	"-L",
	"--max-lines",
]);

/** Drops a wrapper's own flags (and their values) to reach the wrapped command. */
function stripWrapperArgs(program: string, args: string[]): string[] {
	const rest = [...args];
	while (rest.length > 0 && rest[0].startsWith("-")) {
		const flag = rest.shift() as string;
		if (WRAPPER_VALUE_FLAGS.has(flag)) rest.shift();
	}
	if (program === "timeout") rest.shift(); // the duration is not a command
	if (program === "nice" && rest.length > 0 && /^-?\d+$/.test(rest[0])) rest.shift();
	return rest;
}

/** Shell word that starts with an assignment, as in `FOO=1 rm x` or `A=b c=d cmd`. */
const ENV_ASSIGNMENT = /^[A-Za-z_][A-Za-z0-9_]*=/;

/**
 * Programs that ask without reading their arguments: deleting, truncating,
 * changing permissions or ownership, killing processes, touching services, or
 * changing package and scheduled-work state. The value is the reason the owner
 * sees, kept short and specific.
 */
const ASK_PROGRAMS: Record<string, string> = {
	rm: "rm borra archivos",
	rmdir: "rmdir borra directorios",
	unlink: "unlink borra un archivo",
	shred: "shred destruye el contenido de un archivo",
	truncate: "truncate vacía un archivo",
	dd: "dd puede sobrescribir cualquier archivo o dispositivo",
	mkfs: "mkfs formatea un sistema de archivos",
	fdisk: "fdisk reparte un disco",
	parted: "parted reparte un disco",
	wipefs: "wipefs borra firmas de un sistema de archivos",
	chmod: "chmod cambia permisos",
	chown: "chown cambia el dueño de un archivo",
	chgrp: "chgrp cambia el grupo de un archivo",
	chattr: "chattr cambia atributos del sistema de archivos",
	kill: "kill mata procesos",
	pkill: "pkill mata procesos",
	killall: "killall mata procesos",
	systemctl: "systemctl cambia servicios del sistema",
	service: "service cambia servicios del sistema",
	shutdown: "shutdown apaga el sistema",
	reboot: "reboot reinicia el sistema",
	poweroff: "poweroff apaga el sistema",
	halt: "halt detiene el sistema",
	apt: "apt instala o quita paquetes del sistema",
	"apt-get": "apt-get instala o quita paquetes del sistema",
	aptitude: "aptitude instala o quita paquetes del sistema",
	dpkg: "dpkg cambia paquetes del sistema",
	brew: "brew instala o quita paquetes",
	snap: "snap instala o quita paquetes",
	crontab: "crontab programa trabajo automático",
};

/** Package managers whose `-g`/`--global` install reaches outside the project. */
const GLOBAL_INSTALL_PROGRAMS = new Set(["npm", "pnpm", "yarn", "bun"]);

/** Package managers whose `install` subcommand changes the interpreter's environment. */
const INSTALL_PROGRAMS = new Set(["pip", "pip3", "pipx", "uv"]);

/** Docker flags that take a value, so the value is not mistaken for a subcommand. */
const DOCKER_VALUE_FLAGS = new Set(["-H", "--host", "--context", "--config", "--log-level", "-l"]);

/** True for an argument that makes a package manager install globally. */
function isGlobalInstallFlag(argument: string): boolean {
	return (
		argument === "-g" ||
		argument === "--global" ||
		argument === "global" ||
		argument === "-g=true" ||
		argument === "--location=global"
	);
}

/** Program a simple command actually runs, after assignments and transparent wrappers. */
function programOf(input: string[]): string {
	let rest = [...input];
	for (let guard = 0; guard < 8; guard += 1) {
		while (rest.length > 0 && ENV_ASSIGNMENT.test(rest[0])) rest = rest.slice(1);
		if (rest.length === 0) return "";
		const program = basename(rest[0]);
		if (!TRANSPARENT_WRAPPERS.has(program)) return program;
		rest = stripWrapperArgs(program, rest.slice(1));
	}
	return "";
}

/** The inline script of `sh -c '…'`, or null when the shell is not running one. */
function scriptArgument(args: string[]): string | null {
	for (let index = 0; index < args.length; index += 1) {
		const argument = args[index];
		if (argument === "--") return null;
		// A non-flag argument ends the shell's options: it is a script file, whose
		// contents this gate cannot read, but running a script is running a program.
		if (!argument.startsWith("-")) return null;
		if (/^-[A-Za-z]*c[A-Za-z]*$/.test(argument)) return args[index + 1] ?? null;
	}
	return null;
}

/** Verdict for a `git` invocation, read from the real subcommand. */
function classifyGit(args: string[]): Verdict {
	const rest = [...args];
	// Global flags come before the subcommand; some of them take a value (`-C dir`).
	while (rest.length > 0 && rest[0].startsWith("-")) {
		const flag = rest.shift() as string;
		if (flag === "-C" || flag === "-c" || flag === "--git-dir" || flag === "--work-tree" || flag === "--namespace" || flag === "--exec-path") {
			rest.shift();
		}
	}
	const subcommand = rest[0] ?? "";
	const subArgs = rest.slice(1);

	switch (subcommand) {
		case "push":
			return ask("git push publica en el remoto");
		case "reset":
			if (subArgs.some((argument) => argument === "--hard" || argument.startsWith("--hard="))) {
				return ask("git reset --hard descarta cambios locales");
			}
			return allow();
		case "clean":
			if (subArgs.some((argument) => argument === "--force" || (argument.startsWith("-") && !argument.startsWith("--") && argument.includes("f")))) {
				return ask("git clean -f borra archivos no rastreados");
			}
			return allow();
		case "rebase":
			return ask("git rebase reescribe historia");
		case "filter-branch":
			return ask("git filter-branch reescribe historia");
		case "checkout":
			if (subArgs.includes("--")) return ask("git checkout -- descarta cambios locales");
			return allow();
		case "restore":
			return ask("git restore descarta cambios locales");
		case "commit":
			if (subArgs.some((argument) => argument === "--amend")) return ask("git commit --amend reescribe el último commit");
			return allow();
		case "branch":
			if (subArgs.some((argument) => argument === "--delete" || (argument.startsWith("-") && !argument.startsWith("--") && /[dD]/.test(argument)))) {
				return ask("git branch -d borra una rama");
			}
			return allow();
		case "tag":
			if (subArgs.some((argument) => argument === "--delete" || (argument.startsWith("-") && !argument.startsWith("--") && /[dD]/.test(argument)))) {
				return ask("git tag -d borra una etiqueta");
			}
			return allow();
		case "stash":
			if (subArgs[0] === "drop" || subArgs[0] === "clear") return ask("git stash drop borra un stash");
			return allow();
		default:
			return allow();
	}
}

/** Verdict for a `docker`/`podman` invocation: only removals ask. */
function classifyContainer(program: string, args: string[]): Verdict {
	const rest = [...args];
	while (rest.length > 0 && rest[0].startsWith("-")) {
		const flag = rest.shift() as string;
		if (DOCKER_VALUE_FLAGS.has(flag)) rest.shift();
	}
	const subcommand = rest[0] ?? "";
	const subArgs = rest.slice(1);

	if (subcommand === "rm" || subcommand === "rmi") return ask(`${program} ${subcommand} borra contenedores o imágenes`);
	if (subcommand === "prune") return ask(`${program} prune borra datos de ${program}`);
	if (subcommand === "system" && subArgs[0] === "prune") return ask(`${program} system prune borra datos`);
	if (subcommand === "volume" && subArgs[0] === "rm") return ask(`${program} volume rm borra un volumen`);
	if (subcommand === "compose" && subArgs[0] === "down") {
		const removesVolumes = subArgs.some((argument) => argument === "-v" || argument === "--volumes" || argument.startsWith("--volumes="));
		if (removesVolumes) return ask(`${program} compose down -v borra volúmenes`);
	}
	return allow();
}

/**
 * Verdict for one redirection. `>` destroys what was in the file, so it asks when
 * the target already exists; `>>` appends and destroys nothing, so it only asks
 * when the target is a sensitive path (appending a key to `authorized_keys` or a
 * line to `.bashrc` is escalation even though nothing is lost). A target with a
 * variable or a command substitution is not resolved by a shell-agnostic reader, so
 * it asks.
 */
function classifyRedirection(redirection: { op: string; target: string }, cwd: string): Verdict {
	if (redirection.op !== ">" && redirection.op !== ">>") return allow();
	if (redirection.target.includes("$") || redirection.target.includes("`")) {
		return ask(`${redirection.op} escribe en una ruta con variables`);
	}
	const absolutePath = resolveUserPath(redirection.target, cwd);
	if (absolutePath === null) return ask(`${redirection.op} escribe en una ruta que no puedo resolver`);
	if (isSensitivePath(absolutePath)) return ask(`${redirection.op} escribe en ${displayPath(absolutePath)}`);
	if (redirection.op === ">" && existsSync(absolutePath)) return ask(`> sobrescribe ${displayPath(absolutePath)}`);
	return allow();
}

/** Verdict for the words of one simple command, ignoring redirections. */
function classifyWords(input: string[], cwd: string, depth: number): Verdict {
	if (depth > 4) return ask("anidamiento de comandos demasiado profundo");

	const words = [...input];
	while (words.length > 0 && ENV_ASSIGNMENT.test(words[0])) words.shift();
	// Only assignments left (`FOO=1`): the shell sets a variable for nothing, it
	// has no other effect, so there is nothing to ask about.
	if (words.length === 0) return allow();

	const program = basename(words[0]);
	const args = words.slice(1);

	// The escape hatch itself: whatever it runs, the escalation is the risk.
	if (program === "sudo" || program === "doas") return ask(`${program} escala privilegios`);

	if (TRANSPARENT_WRAPPERS.has(program)) {
		const inner = stripWrapperArgs(program, args);
		if (inner.length === 0) return allow();
		return classifyWords(inner, cwd, depth + 1);
	}

	// `bash -c 'rm -rf x'` hides a command inside a word: classify that word too.
	if (SHELLS.has(program)) {
		const script = scriptArgument(args);
		if (script === null) return allow();
		return classifyCommandString(script, cwd, depth + 1);
	}

	const alwaysAsk = ASK_PROGRAMS[program];
	if (alwaysAsk !== undefined) return ask(alwaysAsk);

	if (program === "git") return classifyGit(args);
	if (program === "docker" || program === "podman" || program === "docker-compose") {
		return classifyContainer(program === "docker-compose" ? "docker" : program, program === "docker-compose" ? ["compose", ...args] : args);
	}
	if (GLOBAL_INSTALL_PROGRAMS.has(program) && args.some(isGlobalInstallFlag)) {
		return ask(`${program} instala un paquete global`);
	}
	if (INSTALL_PROGRAMS.has(program) && args.includes("install")) {
		return ask(`${program} install instala paquetes`);
	}
	if (program === "cargo" && args.includes("install")) return ask("cargo install instala un binario");

	if (program === "find") {
		const deletesOrRuns = args.some((argument) =>
			["-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fls"].includes(argument),
		);
		if (deletesOrRuns) return ask("find borra o ejecuta comandos");
		return allow();
	}

	if (program === "mv") return classifyMove(args, cwd);

	return allow();
}

/** Verdict for `mv`: moving onto an existing file overwrites it without asking. */
function classifyMove(args: string[], cwd: string): Verdict {
	const operands = args.filter((argument) => !argument.startsWith("-") || argument === "-");
	if (operands.length < 2) return allow();

	const destination = resolveUserPath(operands[operands.length - 1], cwd);
	if (destination === null) return ask("mv mueve a una ruta que no puedo resolver");

	// `mv a dir` moves into the directory; only a name that already exists inside
	// it is an overwrite. `mv a b` with `b` a file replaces `b` outright.
	const target = isDirectory(destination) ? join(destination, basename(operands[0])) : destination;
	if (existsSync(target)) return ask(`mv sobrescribe ${displayPath(target)}`);
	return allow();
}

/** Verdict for one simple command, redirections and download pipes included. */
function classifySimpleCommand(command: SimpleCommand, next: SimpleCommand | undefined, cwd: string, depth: number): Verdict {
	for (const redirection of command.redirs) {
		const verdict = classifyRedirection(redirection, cwd);
		if (verdict.ask) return verdict;
	}

	const program = programOf(command.words);
	if (program === "") return allow();

	// `curl … | sh` runs code nobody read: the pipe is the whole risk.
	if ((program === "curl" || program === "wget") && next !== undefined && (next.afterOp === "|" || next.afterOp === "|&")) {
		const reader = programOf(next.words);
		if (SHELLS.has(reader) || INTERPRETERS.has(reader)) {
			return ask(`${program} | ${reader} ejecuta código descargado`);
		}
	}

	return classifyWords(command.words, cwd, depth);
}

/**
 * Verdict for a whole command line. Every simple command of the line is classified,
 * because `true && rm -rf x` destroys exactly as much as `rm -rf x` does.
 */
function classifyCommandString(command: string, cwd: string, depth: number): Verdict {
	if (depth > 4) return ask("anidamiento de comandos demasiado profundo");

	const tokens = tokenize(command);
	if (tokens === null) return ask("no pude interpretar el comando: comillas sin cerrar");

	const commands = splitCommands(tokens);
	// A line this long is not something the owner can review on a phone anyway.
	if (commands.length > 200) return ask("el comando tiene demasiadas partes para revisarlo");

	for (let index = 0; index < commands.length; index += 1) {
		const verdict = classifySimpleCommand(commands[index], commands[index + 1], cwd, depth);
		if (verdict.ask) return verdict;
	}
	return allow();
}

/** Verdict for the `bash` tool. A missing command is not decidable, so it asks. */
function classifyBash(input: unknown, cwd: string): Verdict {
	if (typeof input !== "object" || input === null) return ask("no pude leer el comando");
	const command = (input as Json).command;
	if (typeof command !== "string") return ask("no pude leer el comando");
	return classifyCommandString(command, cwd, 0);
}

/**
 * Verdict for the `write` tool (T22): a new file inside `$HOME` needs no
 * permission, an existing file (it overwrites the whole content) and anything
 * outside `$HOME` do.
 */
function classifyWrite(input: unknown, cwd: string): Verdict {
	const raw = readPathField(input);
	if (raw === null) return ask("no pude leer la ruta del archivo");

	const absolutePath = resolveUserPath(raw, cwd);
	if (absolutePath === null) return ask("no puedo resolver la ruta del archivo");
	if (isOutsideHome(absolutePath)) return ask(`escribe fuera de tu home: ${displayPath(absolutePath)}`);
	if (existsSync(absolutePath)) return ask(`sobrescribe ${displayPath(absolutePath)}`);
	return allow();
}

/** Verdict for one tool call, before anything runs. */
function classifyCall(name: string, input: unknown, cwd: string): Verdict {
	if (APPROVAL_FREE.has(name)) return allow();
	if (name === "bash") return classifyBash(input, cwd);
	if (name === "write") return classifyWrite(input, cwd);
	// A tool this gate has no rule for is a decision it cannot make: it asks.
	return ask(`no tengo una regla para la herramienta ${name}`);
}

/**
 * Writes one audit line per decision to stderr, which the bridge forwards to the
 * journal as `bridge: pi stderr: …`. A real turn can then be replayed: what ran
 * unasked, what asked, and why.
 */
function logDecision(name: string, verdict: Verdict, input: unknown): void {
	const decision = verdict.ask ? `ask (${verdict.reason})` : "allow";
	console.error(`alter-gate: ${decision} ${name} ${previewArguments(input, LOG_ARG_LIMIT)}`);
}

type ServerReply = { ok: true; data: Json } | { ok: false; error: string };

function describe(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}

/**
 * POSTs one tool call to the bridge. A 4xx body carries `{"error": "..."}`, which
 * is returned verbatim: it is business text for the model to explain, not a
 * crash to retry.
 */
async function callTools(tool: string, payload: Json, signal?: AbortSignal): Promise<ServerReply> {
	const base = (process.env.ALTER_TOOLS_URL ?? "").trim().replace(/\/+$/, "");
	if (!base) {
		return { ok: false, error: "ALTER_TOOLS_URL no está definido: el servidor de tools del bridge no está disponible." };
	}
	const token = (process.env.ALTER_TOOLS_TOKEN ?? "").trim();
	if (!token) {
		return { ok: false, error: "ALTER_TOOLS_TOKEN no está definido: no puedo autenticarme con el bridge." };
	}

	let response: Response;
	try {
		response = await fetch(`${base}/tools/${tool}`, {
			method: "POST",
			headers: { "content-type": "application/json", "x-alter-token": token },
			body: JSON.stringify(payload),
			signal,
		});
	} catch (error) {
		return { ok: false, error: `No se pudo hablar con el servidor de tools (${base}): ${describe(error)}` };
	}

	const raw = await response.text().catch(() => "");
	let data: Json | undefined;
	if (raw.trim().length > 0) {
		try {
			const parsed: unknown = JSON.parse(raw);
			if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) {
				data = parsed as Json;
			}
		} catch {
			data = undefined;
		}
	}

	if (!response.ok) {
		const serverError = typeof data?.error === "string" ? data.error : "";
		return { ok: false, error: serverError || raw.trim() || `el servidor de tools respondió ${response.status}` };
	}
	return { ok: true, data: data ?? {} };
}

/** Reads a string field of a reply, tolerating a missing one. */
function text(data: Json, key: string): string {
	const value = data[key];
	return typeof value === "string" ? value : "";
}

/**
 * Name of the system prompt section that carries Alter's identity. Pi renders
 * custom sections after every built-in one (see `buildSystemPromptSections`), so
 * this is the last instruction block of the prompt.
 */
const IDENTITY_SECTION = "alter_identity";

/**
 * Fail-safe identity, used only when the persona file cannot be read. A shorter
 * block is better than losing the last word, but it must never disagree with
 * `alter-persona.md`: it states the same two facts the full persona opens with.
 */
const IDENTITY_FALLBACK =
	"Eres ALTER, el secretario personal de Verdu, y no el Gentleman. Heredas su disciplina " +
	"(planificar antes de actuar, delegar cuando toca, verificar lo que afirmas, cuidar al revisor) " +
	"pero tu identidad, tu canal y tu voz son Alter: respondes breve, en español y en markdown rico, " +
	"sin volcar código ni salidas de herramientas en el chat.";

/**
 * Reads the persona once, at load time, so the injected block is exactly the file
 * the bridge also passes to `--append-system-prompt`: one text, no drift.
 *
 * `ALTER_PERSONA_FILE` wins, so a deployment that points the bridge at another
 * persona gets the same file injected last. The default is the repository copy,
 * resolved from this extension's own location because Pi's working directory is
 * the owner's `$HOME`, not the repository root.
 */
function loadIdentity(): string {
	const configured = (process.env.ALTER_PERSONA_FILE ?? "").trim();
	const path = configured || resolve(dirname(fileURLToPath(import.meta.url)), "..", "..", "..", "alter-persona.md");
	try {
		const persona = readFileSync(path, "utf8").trim();
		return persona.length > 0 ? persona : IDENTITY_FALLBACK;
	} catch {
		return IDENTITY_FALLBACK;
	}
}

/** The identity block, read once per Pi process. */
const IDENTITY = loadIdentity();

export default function alterTools(pi: ExtensionAPI): void {
	// Identity precedence (T12). Why the ordering matters:
	//
	// gentle-pi is loaded globally as a package and, in its own
	// `before_agent_start` handler, APPENDS its block through
	// `appendSystemPromptOnce` (gentle-pi/lib/append-system-prompt.ts): it writes
	// `options.appendSystemPrompt = current + block` and never replaces the
	// prompt. That text becomes the `addendum` section, and the CLI's
	// `--append-system-prompt` (our persona) sits earlier still. Doing nothing
	// leaves the Gentleman block last, so the model introduces itself as the
	// Gentleman.
	//
	// Appending to the same string cannot fix that: the `-e` extension loads
	// before the package handlers, so gentle-pi would still write after us.
	// Pi renders custom `sections` after `addendum`, the skills and the cwd, in
	// whatever order the handlers ran, so registering the identity as a section
	// is what puts it after the Gentleman block.
	//
	// Known limit: a handler that runs after this one can still add its own
	// section (the MCP catalogue does), and a handler that forces the whole
	// prompt replaces every section. The first only appends a tool catalogue; the
	// second is handled below.
	pi.on("before_agent_start", (event) => {
		// Idempotent: assigning the key replaces the previous value instead of
		// appending to it, so a handler that runs twice leaves one block.
		event.systemPromptOptions.sections[IDENTITY_SECTION] = IDENTITY;

		// A handler that forces the whole prompt discards every section. In that
		// case re-assert the block over the text that will actually be sent, read
		// from the event, and never truncate what was there.
		const current = event.systemPrompt;
		if (event.systemPromptOptions.forceSystemPrompt !== undefined && !current.includes(IDENTITY)) {
			return { systemPrompt: `${current}\n\n${IDENTITY}` };
		}
		return undefined;
	});

	// T13/T22: approve the tool calls that can destroy, publish or escalate.
	//
	// The gate runs on `tool_call`, which Pi awaits before executing anything and
	// which can stop the call with `{ block: true, reason }`. The question travels
	// through `ctx.ui.confirm()`; in RPC mode that becomes an
	// `extension_ui_request`, and the bridge answers it with the two Telegram
	// buttons (or with `cancelled`).
	//
	// Permission is for what cannot be undone, so the gate asks selectively:
	// `classifyCall` decides, and only an asking verdict reaches the owner's phone.
	//
	// Fail-closed on every axis: a denial, a cancellation (RPC delivers both as
	// `false`), a thrown error and anything the classifier cannot decide all block
	// the call.
	pi.on("tool_call", async (event, ctx) => {
		const name = event.toolName;
		const cwd = typeof ctx.cwd === "string" && ctx.cwd.length > 0 ? ctx.cwd : process.cwd();
		const verdict = classifyCall(name, event.input, cwd);

		logDecision(name, verdict, event.input);
		if (!verdict.ask) return undefined;

		let approved: boolean;
		try {
			approved = await ctx.ui.confirm(`¿Permitir ${name}?`, approvalMessage(verdict.reason, event.input));
		} catch (error) {
			return {
				block: true,
				reason: `No pude pedir aprobación para ${name} (${describe(error)}). No se ejecutó.`,
			};
		}

		if (!approved) {
			return {
				block: true,
				reason: `El dueño no aprobó ${name}, así que no se ejecutó. No lo reintentes igual: propone otra cosa o pregúntale qué prefiere.`,
			};
		}
		return undefined;
	});

	pi.registerTool({
		name: "send_file",
		label: "Enviar archivo",
		description:
			"Envía al dueño por Telegram un archivo que ya está en el vault. La ruta es relativa a la raíz del vault y no puede apuntar fuera de él.",
		parameters: SEND_FILE_PARAMETERS,
		async execute(_toolCallId, params, signal) {
			const payload: Json = { path: params.path };
			if (params.caption) payload.caption = params.caption;

			const reply = await callTools("send_file", payload, signal);
			if (!reply.ok) return failed(reply.error);

			const path = text(reply.data, "path");
			const caption = text(reply.data, "caption");
			const sent = caption ? `Archivo enviado (${path}): ${caption}` : `Archivo enviado: ${path}`;
			return ok(sent, { path, caption });
		},
	});

	pi.registerTool({
		name: "ask_options",
		label: "Preguntar con opciones",
		description:
			"Haz al dueño una pregunta con 2 a 4 opciones seleccionables y espera su pulsación (máximo 10 minutos). Devuelve la opción elegida, o 'sin respuesta' si no contesta; en ese caso sigue sin insistir.",
		parameters: ASK_OPTIONS_PARAMETERS,
		async execute(_toolCallId, params, signal) {
			const reply = await callTools("ask_options", { question: params.question, options: params.options }, signal);
			if (!reply.ok) return failed(reply.error);

			if (reply.data.timed_out === true) {
				return ok("sin respuesta: el dueño no eligió ninguna opción", { answer: "", timed_out: true });
			}
			const answer = text(reply.data, "answer");
			return ok(`El dueño eligió: ${answer}`, { answer, timed_out: false });
		},
	});
}
