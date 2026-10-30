/**
 * Pi extension: Alter's identity block, the approval gate for destructive tools
 * and the two deterministic Alter tools (S7, T12, T13, T17 and T22 of the phase-1
 * Telegram bridge).
 *
 * The gate intercepts `tool_call` and asks the owner only for what cannot be
 * undone: `edit` never asks, `write` asks when the target already exists or sits
 * outside `$HOME`, and `bash` asks only when the command can destroy, publish or
 * escalate. The read-only tools and the two Alter tools never ask, and a tool the
 * gate has no rule for asks only when its own name says it destroys something
 * (`delete_file`, `reset_session`): Pi loads the global configuration (S7), so a
 * button per package tool would rebuild the toll booth one layer out. The question
 * is `ctx.ui.confirm()`, which in RPC mode the bridge turns into two Telegram
 * buttons; no answer means blocked.
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

/**
 * Title of every approval prompt: one short question a person would ask, the same
 * for every rule. Naming the tool (`¿Permitir bash?`) told the owner nothing he
 * could weigh; the consequence is now the first line of the message.
 */
const APPROVAL_TITLE = "¿Lo permito?";

/**
 * Separator between the reason and the command of the approval message: the Go
 * bridge splits on it and renders each part itself. It is a unit separator, a
 * character no shell command needs, so it can never arrive inside a command; the
 * extension sends data, the bridge owns the markup.
 */
const APPROVAL_SEPARATOR = "\x1f";

/**
 * Rune budget of the whole approval message: reason, separator and command. The
 * bridge clips a dialog message at its own cap, keeping both ends, so a message
 * composed inside this budget reaches the owner exactly as written and a typical
 * multi-line command is never cut.
 */
const APPROVAL_MESSAGE_LIMIT = 800;

/** Runes the command keeps even when the reason is long, so the cap never hides it. */
const APPROVAL_MIN_COMMAND_RUNES = 80;

/** Longest argument preview used outside the chat message, for one-line renderings. */
const APPROVAL_ARG_LIMIT = 240;

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
 * Renders the arguments of a tool call as one short line, for the audit line and
 * for an input that carries no command (`write`, a package tool). Whitespace is
 * collapsed because a journal line is one line; the approval message keeps the
 * command untouched instead, so its line breaks survive.
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

/** Length of a text in runes, so a cap written in runes is enforced in runes. */
function runeLength(text: string): number {
	return Array.from(text).length;
}

/**
 * Command of a `bash` tool call exactly as the owner's agent wrote it, or null
 * when the call carries none. The line breaks are kept on purpose: a multi-line
 * script can only be judged line by line.
 */
function commandText(input: unknown): string | null {
	if (typeof input !== "object" || input === null) return null;
	const command = (input as Json).command;
	if (typeof command !== "string") return null;
	const trimmed = command.trim();
	return trimmed.length > 0 ? trimmed : null;
}

/** Head of a clipped command, cut back to the last complete line it contains. */
function wholeLinesFromStart(text: string): string {
	const cut = text.lastIndexOf("\n");
	return cut > 0 ? text.slice(0, cut) : text;
}

/** Tail of a clipped command, starting at the first complete line it contains. */
function wholeLinesFromEnd(text: string): string {
	const cut = text.indexOf("\n");
	return cut >= 0 && cut + 1 < text.length ? text.slice(cut + 1) : text;
}

/**
 * Caps a command at limit runes keeping its head AND its tail: the dangerous line
 * of a long script tends to sit at the end (`... && rm -rf ~/x`), so a head-only
 * cut would hide exactly what the approval exists to show. Only a command that
 * does not fit the message is cut at all, and the elided middle becomes one `…`
 * line, so the part that remains still reads as a command instead of stopping in
 * the middle of a word.
 */
function clipCommand(command: string, limit: number): string {
	const runes = Array.from(command);
	if (runes.length <= limit) return command;
	const head = Math.floor((limit * 2) / 3);
	const tail = limit - head - 3; // the "…" marker and the two line breaks around it
	const headText = wholeLinesFromStart(runes.slice(0, head).join(""));
	const tailText = wholeLinesFromEnd(runes.slice(runes.length - tail).join(""));
	return `${headText}\n…\n${tailText}`;
}

/**
 * Removes the message separator from one part. A command (or a path) carrying the
 * control character would otherwise split the message in the wrong place, and
 * half the command would reach the owner as the reason.
 */
function withoutSeparator(text: string): string {
	return text.replace(/\x1f/g, "");
}

/**
 * Text of the approval prompt: the consequence as one short sentence, then the
 * command, and no markup at all.
 *
 * The bridge renders the command: inside a markdown fence this renderer keeps HTML
 * entities literal, so `>` reached the phone as `&gt;` and three backticks in a
 * command closed the fence early. Sending the two parts separated, and escaping
 * both before they enter the markup, is what makes the command arrive literally
 * and keeps a `<tg-button>` in it from becoming a button.
 *
 * The reason goes first because it says why the button exists, the command follows
 * because it says what will run, and both fit the bridge's cap so it never has to
 * clip what the owner must read.
 */
function approvalMessage(reason: string, input: unknown): string {
	const command = commandText(input);
	const separator = runeLength(APPROVAL_SEPARATOR);
	// A reason can carry a model-supplied path (`Va a sobrescribir <ruta>.`), so it
	// is fitted first: the command keeps a readable share whatever the path says.
	const reasonLimit = Math.max(0, APPROVAL_MESSAGE_LIMIT - separator - APPROVAL_MIN_COMMAND_RUNES);
	const reasonText = previewWithTail(withoutSeparator(reason), reasonLimit);
	const budget = Math.max(APPROVAL_MIN_COMMAND_RUNES, APPROVAL_MESSAGE_LIMIT - runeLength(reasonText) - separator);
	const body = command === null ? previewArguments(input, budget) : clipCommand(command, budget);
	return `${reasonText}${APPROVAL_SEPARATOR}${withoutSeparator(body)}`;
}

/**
 * One verdict per tool call: allowed silently, or asked with two texts. `reason`
 * is what the owner reads — a sentence about the consequence, never a binary
 * name; `detail` is what the journal records — the rule that fired, so `git push`
 * and `git reset --hard` stay distinguishable in the audit line.
 */
type Verdict = { ask: boolean; reason: string; detail: string };

function allow(): Verdict {
	return { ask: false, reason: "", detail: "" };
}

function ask(reason: string, detail: string): Verdict {
	return { ask: true, reason, detail };
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

/**
 * Devices a redirection may target without destroying anything: `/dev/null` throws
 * the bytes away and `/dev/stdout`, `/dev/stderr` and `/dev/tty` echo them into a
 * descriptor the owner already sees. None of them stores data, so none of them can
 * lose it, and `2>/dev/null` is how every script silences stderr: asking for it
 * turned the gate into a toll booth (six of eight questions in 25 minutes of the
 * production journal were exactly this). Deliberately a closed list of exact
 * paths, never a `/dev/*` prefix: `> /dev/sda` must still ask.
 */
const DISCARD_DEVICES = new Set(["/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty"]);

/**
 * True when a redirection target resolves to a device that only discards or
 * repeats output. The target is resolved the same way every other path is, so
 * `$HOME/../dev/null` lands here too, while a target carrying a variable still
 * falls through to the rule that asks: a shell-agnostic reader cannot know what
 * `$SINK` is, and only a literal device name is safe to allow.
 */
function isDiscardDevice(target: string, cwd: string): boolean {
	if (target.includes("$") || target.includes("`")) return false;
	const absolutePath = resolveUserPath(target, cwd);
	return absolutePath !== null && DISCARD_DEVICES.has(absolutePath);
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

/** An option set with no value-taking flag, for the callers that only need an operand. */
const NO_VALUE_FLAGS: Set<string> = new Set();

/**
 * The sentences the owner reads. Each one names the consequence in the second
 * person and, unless the program IS the point (`sudo`), never the program: "rm
 * borra archivos" says nothing a person can weigh on a phone, while "Va a borrar
 * archivos." says what he is deciding. The program and its flags stay in the
 * journal, through each rule's `detail`.
 */
const REASON_DELETE_FILES = "Va a borrar archivos.";
const REASON_PERMISSIONS = "Va a cambiar permisos de archivos.";
const REASON_SYSTEM_STATE = "Va a cambiar el estado del sistema.";
const REASON_ADMIN_RIGHTS = "Va a pedir permisos de administrador.";
const REASON_INSTALL = "Va a instalar software en el sistema.";
const REASON_SCHEDULE = "Va a programar una tarea automática.";
const REASON_HISTORY = "Va a publicar o reescribir el historial del repositorio.";
const REASON_DOWNLOADED_CODE = "Va a ejecutar código descargado de internet.";
const REASON_CONTAINERS = "Va a borrar contenedores o volúmenes.";
const REASON_MODULE = "Va a ejecutar un módulo de Python.";
const REASON_DOWNLOADED_PACKAGE = "Va a ejecutar un paquete descargado.";
const REASON_ARBITRARY_CODE = "Va a ejecutar código arbitrario.";

/** One gate rule: what the owner reads, and what the journal records. */
type Rule = { reason: string; detail: string };

/**
 * Programs that ask without reading their arguments: deleting, truncating,
 * changing permissions or ownership, stopping a service, or installing a package.
 * The programs whose verdict depends on the subcommand they are given, or on the
 * program they run (`systemctl`, `apt`, `dpkg`, `crontab`, `kill`, `find`), have
 * their own reader further down: each one allows an explicit list of reads and
 * keeps asking for everything it does not recognise.
 */
const ASK_PROGRAMS: Record<string, Rule> = {
	rm: { reason: REASON_DELETE_FILES, detail: "rm borra archivos" },
	rmdir: { reason: REASON_DELETE_FILES, detail: "rmdir borra directorios" },
	unlink: { reason: REASON_DELETE_FILES, detail: "unlink borra un archivo" },
	shred: { reason: REASON_DELETE_FILES, detail: "shred destruye el contenido de un archivo" },
	truncate: { reason: REASON_DELETE_FILES, detail: "truncate vacía un archivo" },
	dd: { reason: REASON_DELETE_FILES, detail: "dd puede sobrescribir cualquier archivo o dispositivo" },
	mkfs: { reason: "Va a formatear un disco.", detail: "mkfs formatea un sistema de archivos" },
	fdisk: { reason: "Va a repartir un disco.", detail: "fdisk reparte un disco" },
	parted: { reason: "Va a repartir un disco.", detail: "parted reparte un disco" },
	wipefs: { reason: "Va a borrar marcas de un disco.", detail: "wipefs borra firmas de un sistema de archivos" },
	chmod: { reason: REASON_PERMISSIONS, detail: "chmod cambia permisos" },
	chown: { reason: REASON_PERMISSIONS, detail: "chown cambia el dueño de un archivo" },
	chgrp: { reason: REASON_PERMISSIONS, detail: "chgrp cambia el grupo de un archivo" },
	chattr: { reason: "Va a cambiar atributos de archivos.", detail: "chattr cambia atributos del sistema de archivos" },
	pkill: { reason: REASON_SYSTEM_STATE, detail: "pkill mata procesos" },
	killall: { reason: REASON_SYSTEM_STATE, detail: "killall mata procesos" },
	service: { reason: REASON_SYSTEM_STATE, detail: "service cambia servicios del sistema" },
	shutdown: { reason: REASON_SYSTEM_STATE, detail: "shutdown apaga el sistema" },
	reboot: { reason: REASON_SYSTEM_STATE, detail: "reboot reinicia el sistema" },
	poweroff: { reason: REASON_SYSTEM_STATE, detail: "poweroff apaga el sistema" },
	halt: { reason: REASON_SYSTEM_STATE, detail: "halt detiene el sistema" },
	brew: { reason: REASON_INSTALL, detail: "brew instala o quita paquetes" },
	snap: { reason: REASON_INSTALL, detail: "snap instala o quita paquetes" },
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

/**
 * Words a simple command actually runs, after its assignments and transparent
 * wrappers: the words of `env FOO=1 rm x` start at `rm`.
 */
function commandWords(input: string[]): string[] {
	let rest = [...input];
	for (let guard = 0; guard < 8; guard += 1) {
		while (rest.length > 0 && ENV_ASSIGNMENT.test(rest[0])) rest = rest.slice(1);
		if (rest.length === 0) return rest;
		const program = basename(rest[0]);
		if (!TRANSPARENT_WRAPPERS.has(program)) return rest;
		rest = stripWrapperArgs(program, rest.slice(1));
	}
	return [];
}

/** Program a simple command actually runs, after assignments and transparent wrappers. */
function programOf(input: string[]): string {
	const rest = commandWords(input);
	return rest.length > 0 ? basename(rest[0]) : "";
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

/**
 * Interpreters whose arguments the gate reads. The production journal had
 * `python3 -m pip install …` allowed because the program was `python3` and nothing
 * looked at what it was asked to run: an installer behind an interpreter walked
 * through the gap. What the interpreter is given decides, not its name.
 */
type InterpreterKind = "python" | "node" | "perl" | "ruby";

/** Program names of each interpreter, version suffixes included (`python3.11`, `perl5.36`). */
const INTERPRETER_NAMES: { kind: InterpreterKind; name: RegExp }[] = [
	{ kind: "python", name: /^python(\d+(\.\d+)?)?$/ },
	{ kind: "node", name: /^node(js)?$/ },
	{ kind: "perl", name: /^perl(\d+(\.\d+)?)?$/ },
	{ kind: "ruby", name: /^ruby(\d+(\.\d+)?)?$/ },
];

/** Interpreter family of a program name, or null when it is not one. */
function interpreterKind(program: string): InterpreterKind | null {
	for (const entry of INTERPRETER_NAMES) {
		if (entry.name.test(program)) return entry.kind;
	}
	return null;
}

/**
 * Short-option letters that make an interpreter run the code written in the
 * command instead of a file: `-c` for Python and `-e` for Node, Perl and Ruby.
 * Perl's `-E` is the same flag with features enabled, so it walks the same rule.
 * A command can pass the code glued to the flag (`python3 -c'print(1)'`), so the
 * letters of the cluster are read, not just the exact flag.
 */
const INLINE_CODE_LETTERS: Record<InterpreterKind, string> = {
	python: "c",
	node: "e",
	perl: "eE",
	ruby: "e",
};

/**
 * Letters of an interpreter's short-option cluster (`-c`, `-we`, `-cprint(1)`), or
 * null when the word is not a short option. Only the leading letters are read, so a
 * glued value (`-cprint(1)`) does not turn the parentheses into flag names.
 */
function optionLetters(argument: string): string | null {
	if (!argument.startsWith("-") || argument.startsWith("--") || argument === "-") return null;
	const letters = /^-([A-Za-z]*)/.exec(argument)?.[1] ?? "";
	return letters.length > 0 ? letters : null;
}

/** Perl short options that swallow a glued value (`-Mstrict`, `-Ilib`), so they are not code. */
const PERL_VALUE_PREFIXES = ["-M", "-m", "-I"];

/** Python short options that take their value as the next word (`-W ignore`, `-X utf8`). */
const PYTHON_VALUE_LETTERS = "WX";

/** True for the module names that are pip itself: `pip`, `pip3`, `pip3.11`. */
function isPipModule(module: string): boolean {
	return /^pip\d*(\.\d+)*$/.test(module);
}

/**
 * Closed allowlist of `python -m` modules that only transform the data they are
 * given: they read stdin or an input file and write the result to stdout. The
 * number is how many operands the module reads, and with that shape no invocation
 * writes a file, opens a socket or installs anything: `json.tool` writes its
 * *second* positional (`python3 -m json.tool in.json out.json` overwrites
 * `out.json`), and that form is the reason for the cap.
 *
 * `json.tool` re-prints JSON, `base64` encodes or decodes bytes. Nothing else is
 * here: `http.server`/`pydoc -b` serve, `venv`/`ensurepip`/`pip` install,
 * `doctest`/`timeit` run code, `gzip`/`zipfile`/`pickletools -o` write files.
 * Every module this reader has not checked keeps the ask it had, and the whole
 * list is documented in `docs/alter-limites.md`.
 */
const DATA_TRANSFORM_MODULES: Record<string, number> = {
	"json.tool": 1,
	base64: 1,
};

/**
 * True when `module` is in the allowlist and its words leave it no output file:
 * a flag is not an operand, and every other word counts as one.
 */
function isDataTransformModule(module: string, args: string[]): boolean {
	const operandLimit = DATA_TRANSFORM_MODULES[module];
	if (operandLimit === undefined) return false;
	let operands = 0;
	for (const argument of args) {
		// `-` is the stdin operand, not a flag: it occupies a slot like a file does.
		if (argument.startsWith("-") && argument !== "-") continue;
		operands += 1;
		if (operands > operandLimit) return false;
	}
	return true;
}

/** What a `python` command line runs: inline code, a module, or neither. */
type PythonRequest =
	| { kind: "inline" }
	| { kind: "module"; module: string; args: string[] }
	| { kind: "none" };

/**
 * Request of a `python` invocation, read from its options. `-c` and `-m` are the
 * only flags that decide, and both end the scan: what follows `-m` belongs to the
 * module. A script argument, `-` and `--` run a file, so they leave nothing for
 * this reader to add.
 */
function pythonRequest(args: string[]): PythonRequest {
	for (let index = 0; index < args.length; index += 1) {
		const argument = args[index];
		// `--` ends the options and `-` reads the script from stdin: both run a
		// script, so they keep the verdict `python3 script.py` has.
		if (argument === "--" || argument === "-") return { kind: "none" };
		if (!argument.startsWith("-")) return { kind: "none" }; // an existing script
		const letters = optionLetters(argument);
		if (letters === null) continue; // a long option (`--version`): none runs code
		// `-W ignore` and `-X utf8` take the next word as their value, so that word is
		// not a script and must not end the scan: `python3 -W ignore -c '…'` still asks.
		if (argument.length === 2 && PYTHON_VALUE_LETTERS.includes(letters)) {
			index += 1;
			continue;
		}
		if (letters.includes("c")) return { kind: "inline" };
		if (!letters.includes("m")) continue;
		return { kind: "module", module: args[index + 1] ?? "", args: args.slice(index + 2) };
	}
	return { kind: "none" };
}

/**
 * Verdict for `python`. A module asks, because it is code that is not in the command
 * line: `-m pip install`/`-m pip uninstall` and `-m ensurepip` with the installer reason,
 * a module of the closed data-transforming allowlist is allowed (`json.tool`
 * re-prints JSON), and any other module as unknown code. Inline code (`-c`) asks
 * as arbitrary code. A script argument is allowed: it is an existing file, the
 * same class as `go test`, and the decision is recorded in `docs/alter-limites.md`.
 */
function classifyPython(program: string, args: string[]): Verdict {
	const request = pythonRequest(args);
	if (request.kind === "inline") return ask(REASON_ARBITRARY_CODE, `${program} -c ejecuta código arbitrario`);
	if (request.kind === "none") return allow();

	const { module, args: moduleArgs } = request;
	if (module.length === 0) {
		return ask(REASON_MODULE, `${program} -m sin módulo: no reconozco la operación`);
	}
	if (isDataTransformModule(module, moduleArgs)) return allow();
	const subcommand = moduleArgs.find((rest) => !rest.startsWith("-")) ?? "";
	if (module === "ensurepip") return ask(REASON_INSTALL, `${program} -m ensurepip instala pip`);
	if (isPipModule(module) && (subcommand === "install" || subcommand === "uninstall")) {
		return ask(REASON_INSTALL, `${program} -m pip ${subcommand} instala o quita paquetes`);
	}
	return ask(REASON_MODULE, `${program} -m ${module} ejecuta un módulo`);
}

/**
 * Verdict for an interpreter. What it is asked to run decides: `-c`/`-e` and the
 * Node long form `--eval` are code written in the command, so they ask without
 * being read (a reader of code is a promise this gate cannot keep); a script
 * argument is allowed, because running an existing file is the same class as
 * `go test`.
 */
function classifyInterpreter(program: string, kind: InterpreterKind, args: string[]): Verdict {
	if (kind === "python") return classifyPython(program, args);

	for (let index = 0; index < args.length; index += 1) {
		const argument = args[index];
		if (argument === "--" || argument === "-") return allow();
		if (!argument.startsWith("-")) return allow(); // an existing script
		// The tokenizer glues a quoted value to its flag (`--eval'x'`), so the long
		// Node option is matched by its head.
		if (kind === "node" && argument.startsWith("--eval")) {
			return ask(REASON_ARBITRARY_CODE, `${program} --eval ejecuta código arbitrario`);
		}
		if (kind === "perl" && PERL_VALUE_PREFIXES.some((prefix) => argument.startsWith(prefix))) continue;
		const letters = optionLetters(argument);
		if (letters === null) continue; // a long option: none of them runs code here
		const flag = Array.from(letters).find((letter) => INLINE_CODE_LETTERS[kind].includes(letter));
		if (flag !== undefined) {
			return ask(REASON_ARBITRARY_CODE, `${program} -${flag} ejecuta código arbitrario`);
		}
	}
	return allow();
}

/** Programs that download and run a package on every invocation. */
const PACKAGE_RUNNERS = new Set(["npx", "bunx"]);

/** Package managers whose subcommand downloads and runs a package. */
const PACKAGE_RUNNER_SUBCOMMANDS: Record<string, string> = {
	npm: "exec",
	pnpm: "dlx",
	yarn: "dlx",
};

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
			return ask(REASON_HISTORY, "git push publica en el remoto");
		case "reset":
			if (subArgs.some((argument) => argument === "--hard" || argument.startsWith("--hard="))) {
				return ask("Va a descartar cambios locales.", "git reset --hard descarta cambios locales");
			}
			return allow();
		case "clean":
			if (subArgs.some((argument) => argument === "--force" || (argument.startsWith("-") && !argument.startsWith("--") && argument.includes("f")))) {
				return ask("Va a borrar archivos no rastreados.", "git clean -f borra archivos no rastreados");
			}
			return allow();
		case "rebase":
			return ask(REASON_HISTORY, "git rebase reescribe historia");
		case "filter-branch":
			return ask(REASON_HISTORY, "git filter-branch reescribe historia");
		case "checkout":
			if (subArgs.includes("--")) return ask("Va a descartar cambios locales.", "git checkout -- descarta cambios locales");
			return allow();
		case "restore":
			return ask("Va a descartar cambios locales.", "git restore descarta cambios locales");
		case "commit":
			if (subArgs.some((argument) => argument === "--amend")) {
				return ask("Va a reescribir el último commit.", "git commit --amend reescribe el último commit");
			}
			return allow();
		case "branch":
			if (subArgs.some((argument) => argument === "--delete" || (argument.startsWith("-") && !argument.startsWith("--") && /[dD]/.test(argument)))) {
				return ask("Va a borrar una rama del repositorio.", "git branch -d borra una rama");
			}
			return allow();
		case "tag":
			if (subArgs.some((argument) => argument === "--delete" || (argument.startsWith("-") && !argument.startsWith("--") && /[dD]/.test(argument)))) {
				return ask("Va a borrar una etiqueta del repositorio.", "git tag -d borra una etiqueta");
			}
			return allow();
		case "stash":
			if (subArgs[0] === "drop" || subArgs[0] === "clear") {
				return ask("Va a borrar cambios guardados.", "git stash drop borra un stash");
			}
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

	if (subcommand === "rm" || subcommand === "rmi") {
		return ask(REASON_CONTAINERS, `${program} ${subcommand} borra contenedores o imágenes`);
	}
	if (subcommand === "prune") return ask(REASON_CONTAINERS, `${program} prune borra datos de ${program}`);
	if (subcommand === "system" && subArgs[0] === "prune") return ask(REASON_CONTAINERS, `${program} system prune borra datos`);
	if (subcommand === "volume" && subArgs[0] === "rm") return ask(REASON_CONTAINERS, `${program} volume rm borra un volumen`);
	if (subcommand === "compose" && subArgs[0] === "down") {
		const removesVolumes = subArgs.some((argument) => argument === "-v" || argument === "--volumes" || argument.startsWith("--volumes="));
		if (removesVolumes) return ask(REASON_CONTAINERS, `${program} compose down -v borra volúmenes`);
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
	// A device sink is checked before every other rule, and for every descriptor and
	// for both `>` and `>>`: `2>/dev/null` writes to `/dev`, which the sensitive list
	// covers, but only as a sink that keeps nothing.
	if (isDiscardDevice(redirection.target, cwd)) return allow();
	if (redirection.target.includes("$") || redirection.target.includes("`")) {
		return ask("Va a escribir en una ruta que no puedo comprobar.", `${redirection.op} escribe en una ruta con variables`);
	}
	const absolutePath = resolveUserPath(redirection.target, cwd);
	if (absolutePath === null) {
		return ask("Va a escribir en una ruta que no puedo comprobar.", `${redirection.op} escribe en una ruta que no puedo resolver`);
	}
	const shown = displayPath(absolutePath);
	if (isSensitivePath(absolutePath)) {
		// `>>` adds instead of destroying, but on a start-up file or a key list
		// adding a line is the escalation, so the reason says which of the two it is.
		return redirection.op === ">"
			? ask(`Va a sobrescribir ${shown}.`, `${redirection.op} escribe en ${shown}, una ruta sensible`)
			: ask(`Va a añadir contenido a ${shown}.`, `${redirection.op} escribe en ${shown}, una ruta sensible`);
	}
	if (redirection.op === ">" && existsSync(absolutePath)) {
		return ask(`Va a sobrescribir ${shown}.`, `> sobrescribe ${shown}`);
	}
	return allow();
}

/** Verdict for the words of one simple command, ignoring redirections. */
function classifyWords(input: string[], cwd: string, depth: number): Verdict {
	if (depth > 4) return ask("No puedo revisar un comando tan anidado.", "anidamiento de comandos demasiado profundo");

	const words = [...input];
	while (words.length > 0 && ENV_ASSIGNMENT.test(words[0])) words.shift();
	// Only assignments left (`FOO=1`): the shell sets a variable for nothing, it
	// has no other effect, so there is nothing to ask about.
	if (words.length === 0) return allow();

	const program = basename(words[0]);
	const args = words.slice(1);

	// The escape hatch itself: whatever it runs, the escalation is the risk, and
	// this is the one rule where the program is the consequence.
	if (program === "sudo" || program === "doas") return ask(REASON_ADMIN_RIGHTS, `${program} escala privilegios`);

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
	if (alwaysAsk !== undefined) return ask(alwaysAsk.reason, alwaysAsk.detail);

	// An installer or arbitrary code behind an interpreter is the same act as the
	// program itself, so the interpreter's arguments are read before anything else
	// about it: `python3 -m pip install x` asks as `pip install x` does.
	const kind = interpreterKind(program);
	if (kind !== null) return classifyInterpreter(program, kind, args);

	// A package runner downloads a package and runs it: there is no local file to
	// review, so every form asks.
	const runnerSubcommand = PACKAGE_RUNNER_SUBCOMMANDS[program];
	if (PACKAGE_RUNNERS.has(program)) {
		return ask(REASON_DOWNLOADED_PACKAGE, `${program} descarga y ejecuta un paquete`);
	}
	if (runnerSubcommand !== undefined && firstOperand(args, NO_VALUE_FLAGS) === runnerSubcommand) {
		return ask(REASON_DOWNLOADED_PACKAGE, `${program} ${runnerSubcommand} descarga y ejecuta un paquete`);
	}

	if (program === "git") return classifyGit(args);
	if (program === "docker" || program === "podman" || program === "docker-compose") {
		return classifyContainer(program === "docker-compose" ? "docker" : program, program === "docker-compose" ? ["compose", ...args] : args);
	}
	if (GLOBAL_INSTALL_PROGRAMS.has(program) && args.some(isGlobalInstallFlag)) {
		return ask(REASON_INSTALL, `${program} instala un paquete global`);
	}
	if (INSTALL_PROGRAMS.has(program) && args.includes("install")) {
		return ask(REASON_INSTALL, `${program} install instala paquetes`);
	}
	if (program === "cargo" && args.includes("install")) return ask(REASON_INSTALL, "cargo install instala un binario");

	if (program === "systemctl") return classifySystemctl(args);
	if (program === "apt" || program === "apt-get" || program === "aptitude") return classifyApt(program, args);
	if (program === "dpkg") return classifyDpkg(args);
	if (program === "crontab") return classifyCrontab(args);
	if (program === "kill") return classifyKill(args);
	if (program === "find") return classifyFind(args);

	if (program === "mv") return classifyMove(args, cwd);

	return allow();
}

/**
 * First operand of a program whose flags may come before its subcommand. The
 * flags are skipped, and so is the value of the ones that take one, so
 * `apt-get -y install x` still reveals `install` and `systemctl --user status x`
 * still reveals `status` instead of stopping at the flag. Null when the arguments
 * carry no operand at all.
 */
function firstOperand(args: string[], valueFlags: Set<string>): string | null {
	const rest = [...args];
	while (rest.length > 0 && rest[0].startsWith("-")) {
		const flag = rest.shift() as string;
		if (valueFlags.has(flag)) rest.shift();
	}
	return rest[0] ?? null;
}

/**
 * Programs whose run leaves the machine as it was: the closed allowlist `find` may
 * execute through `-exec`/`-execdir`/`-ok`/`-okdir` without asking. Anything else
 * (`rm`, `mv`, `chmod`, a shell, a script of its own) keeps asking, and so does an
 * empty program, which is a line this reader cannot decide.
 */
const FIND_READ_ONLY_PROGRAMS = new Set(["ls", "cat", "grep", "stat", "file", "wc", "head", "tail", "du", "echo"]);

/** `find` predicates that run another program. */
const FIND_EXEC_FLAGS = new Set(["-exec", "-execdir", "-ok", "-okdir"]);

/** `find` predicates that destroy a match or write a file of their own. */
const FIND_WRITE_FLAGS = new Set(["-delete", "-fprint", "-fprint0", "-fprintf", "-fls"]);

/**
 * Verdict for `find`.
 *
 * `-exec`, `-execdir`, `-ok` and `-okdir` run a program the gate never sees as a
 * program of its own, so the verdict is read from the program that follows the
 * predicate: a read-only one is a read, and everything else keeps the ask that
 * `find` always had. That is what makes `find . -name '*.go' -exec ls -l {} +`
 * pass while `find . -name x -exec rm {} \;` still asks. `-delete` and the
 * predicates that write a file keep asking unconditionally.
 */
function classifyFind(args: string[]): Verdict {
	let index = 0;
	while (index < args.length) {
		const argument = args[index];
		if (FIND_WRITE_FLAGS.has(argument)) {
			return ask(REASON_DELETE_FILES, `find ${argument} borra o escribe archivos`);
		}
		if (!FIND_EXEC_FLAGS.has(argument)) {
			index += 1;
			continue;
		}
		// The program is the word right after the predicate, so a path form
		// (`/bin/ls`) is compared by its name, exactly as `programOf` does.
		const program = basename(args[index + 1] ?? "");
		if (!FIND_READ_ONLY_PROGRAMS.has(program)) {
			return ask(REASON_DELETE_FILES, `find ${argument} ejecuta ${program || "un programa que no puedo leer"}`);
		}
		// Skip the rest of the predicate up to its `;` or `+` and keep reading: one
		// line can carry several `-exec` predicates and every one of them runs.
		index += 2;
		while (index < args.length && args[index] !== ";" && args[index] !== "+") index += 1;
		index += 1;
	}
	return allow();
}

/** `systemctl` flags that take a value, so the value is not read as the subcommand. */
const SYSTEMCTL_VALUE_FLAGS = new Set([
	"-H",
	"--host",
	"-M",
	"--machine",
	"-p",
	"--property",
	"-t",
	"--type",
	"--state",
	"-n",
	"--lines",
	"-o",
	"--output",
	"-s",
	"--signal",
	"--kill-whom",
	"--root",
	"--job-mode",
	"-E",
	"--environment",
]);

/** `systemctl` subcommands that only report the state of the system. */
const SYSTEMCTL_READ_SUBCOMMANDS = new Set([
	"status",
	"is-active",
	"is-enabled",
	"is-failed",
	"is-system-running",
	"show",
	"cat",
	"help",
	"list-units",
	"list-unit-files",
	"list-timers",
	"list-sockets",
	"list-jobs",
	"list-dependencies",
	"list-sessions",
	"list-machines",
]);

/**
 * Verdict for `systemctl`: the subcommand decides, not the program. `status`,
 * `is-active`, `list-units` and the rest of the read list only report, while
 * `start`, `stop`, `restart`, `enable`, `mask`, `daemon-reload` and everything else
 * change the system and keep asking.
 */
function classifySystemctl(args: string[]): Verdict {
	const subcommand = firstOperand(args, SYSTEMCTL_VALUE_FLAGS);
	if (subcommand === null) {
		// Only the version banner is a read on its own. A bare `systemctl` lists units
		// too, but with no subcommand there is nothing the allowlist can be applied to.
		if (args.includes("--version")) return allow();
		return ask(REASON_SYSTEM_STATE, "systemctl sin subcomando: no reconozco la operación");
	}
	if (SYSTEMCTL_READ_SUBCOMMANDS.has(subcommand)) return allow();
	return ask(REASON_SYSTEM_STATE, `systemctl ${subcommand} cambia servicios del sistema`);
}

/** `apt`/`apt-get` flags that take a value, so `-o X=1` does not hide the subcommand. */
const APT_VALUE_FLAGS = new Set(["-o", "--option", "-t", "--target-release", "-c", "--config-file", "-a", "--host-architecture"]);

/** `apt` subcommands that only read the package index. */
const APT_READ_SUBCOMMANDS = new Set([
	"list",
	"search",
	"show",
	"policy",
	"showpkg",
	"showsrc",
	"depends",
	"rdepends",
	"changelog",
	"version",
]);

/**
 * Verdict for `apt`/`apt-get`/`aptitude`: only the subcommands that read the index
 * run unasked. `install`, `remove`, `purge`, `upgrade`, `dist-upgrade`,
 * `autoremove`, `clean` and anything unrecognised keep asking, and the flags before
 * the subcommand (`apt-get -y install x`) never hide it.
 */
function classifyApt(program: string, args: string[]): Verdict {
	const subcommand = firstOperand(args, APT_VALUE_FLAGS);
	if (subcommand !== null && APT_READ_SUBCOMMANDS.has(subcommand)) return allow();
	const detail =
		subcommand === null
			? `${program} sin subcomando: no reconozco la operación`
			: `${program} ${subcommand} instala o quita paquetes del sistema`;
	return ask(REASON_INSTALL, detail);
}

/** `dpkg` actions that only read the database or the contents of a package file. */
const DPKG_READ_ACTIONS = new Set([
	"-l",
	"--list",
	"-L",
	"--listfiles",
	"-s",
	"--status",
	"-S",
	"--search",
	"-p",
	"--print-avail",
	"-c",
	"--contents",
	"-I",
	"--info",
	"--get-selections",
	"--print-architecture",
	"--version",
	"--help",
]);

/** Letters a read-only `dpkg` short cluster may carry (`-l`, `-L`, `-s`, `-S`, `-p`, `-c`). */
const DPKG_READ_LETTERS = new Set(["l", "L", "s", "S", "p", "c"]);

/**
 * Verdict for `dpkg`: the action is a flag, so it is read as one. `-l`/`--list`,
 * `-L`/`--listfiles`, `-s`/`--status` and the other reads run unasked; `-i`, `-r`,
 * `-P`, `--configure` and anything unrecognised keep asking.
 */
function classifyDpkg(args: string[]): Verdict {
	const action = args.find((argument) => argument.startsWith("-"));
	if (action !== undefined) {
		if (DPKG_READ_ACTIONS.has(action)) return allow();
		const letters = shortFlagLetters(action);
		if (letters !== null && letters.every((letter) => DPKG_READ_LETTERS.has(letter))) return allow();
	}
	const detail = action === undefined ? "dpkg sin acción: no reconozco la operación" : `dpkg ${action} cambia paquetes del sistema`;
	return ask(REASON_INSTALL, detail);
}

/** Letters of a short flag cluster (`-lL` → `["l", "L"]`), or null when it is not one. */
function shortFlagLetters(argument: string): string[] | null {
	if (!/^-[A-Za-z]+$/.test(argument)) return null;
	return Array.from(argument.slice(1));
}

/**
 * Verdict for `crontab`: `-l` lists the current table and changes nothing, so it is
 * the only form that runs unasked. `-e` opens the editor, `-r` removes the table and
 * a file argument replaces it, so all three keep asking.
 */
function classifyCrontab(args: string[]): Verdict {
	let lists = false;
	let changes = false;
	for (let index = 0; index < args.length; index += 1) {
		const argument = args[index];
		if (argument === "-u" || argument === "--user") {
			index += 1; // the table the operation applies to is not the operation
			continue;
		}
		if (argument === "--list") {
			lists = true;
			continue;
		}
		const letters = argument.startsWith("-") ? shortFlagLetters(argument) : null;
		if (letters === null) {
			// A file argument replaces the table, and a long flag I do not know could
			// be anything: both keep asking.
			changes = true;
			continue;
		}
		for (const letter of letters) {
			if (letter === "l") lists = true;
			else changes = true; // `e`, `r`, `i` and anything the allowlist does not name
		}
	}
	if (lists && !changes) return allow();
	return ask(REASON_SCHEDULE, "crontab cambia o reemplaza tareas programadas");
}

/**
 * Verdict for `kill`: `-l` lists signal names and `-0` (or `-s 0`) only probes
 * whether a process exists, so both are reads. Any other flag or a bare operand
 * sends a real signal, which changes system state and keeps asking. `pkill` and
 * `killall` are untouched: they have no probe form here.
 */
function classifyKill(args: string[]): Verdict {
	const first = args[0];
	if (first === undefined) return ask(REASON_SYSTEM_STATE, "kill sin argumentos: no reconozco la operación");
	if (first === "-l" || first === "--list" || first === "-L" || first === "--table" || first === "-0") return allow();
	// `kill -s 0 pid` and `kill -n 0 pid` probe exactly like `kill -0 pid`.
	if ((first === "-s" || first === "--signal" || first === "-n") && (args[1] ?? "") === "0") return allow();
	return ask(REASON_SYSTEM_STATE, "kill envía una señal a procesos");
}

/** Verdict for `mv`: moving onto an existing file overwrites it without asking. */
function classifyMove(args: string[], cwd: string): Verdict {
	const operands = args.filter((argument) => !argument.startsWith("-") || argument === "-");
	if (operands.length < 2) return allow();

	const destination = resolveUserPath(operands[operands.length - 1], cwd);
	if (destination === null) {
		return ask("Va a mover archivos a una ruta que no puedo comprobar.", "mv mueve a una ruta que no puedo resolver");
	}

	// `mv a dir` moves into the directory; only a name that already exists inside
	// it is an overwrite. `mv a b` with `b` a file replaces `b` outright.
	const target = isDirectory(destination) ? join(destination, basename(operands[0])) : destination;
	if (existsSync(target)) return ask("Va a sobrescribir un archivo.", `mv sobrescribe ${displayPath(target)}`);
	return allow();
}

/**
 * True when the reader of a download pipe only transforms the data it receives:
 * `curl … | python3 -m json.tool` formats the download, it does not run it, so the
 * pipe adds no risk of its own. The reader has to be a Python module of the closed
 * allowlist, with the same reading of its words the interpreter rule uses.
 */
function readsPipedDataAsData(words: string[]): boolean {
	const inner = commandWords(words);
	if (inner.length === 0) return false;
	if (interpreterKind(basename(inner[0])) !== "python") return false;
	const request = pythonRequest(inner.slice(1));
	return request.kind === "module" && isDataTransformModule(request.module, request.args);
}

/** Verdict for one simple command, redirections and download pipes included. */
function classifySimpleCommand(command: SimpleCommand, next: SimpleCommand | undefined, cwd: string, depth: number): Verdict {
	for (const redirection of command.redirs) {
		const verdict = classifyRedirection(redirection, cwd);
		if (verdict.ask) return verdict;
	}

	const program = programOf(command.words);
	if (program === "") return allow();

	// `curl … | sh` runs code nobody read: the pipe is the whole risk. A reader that
	// only transforms the download (`python3 -m json.tool`) is the exception, and it
	// is read from the same closed allowlist as the interpreter rule.
	if ((program === "curl" || program === "wget") && next !== undefined && (next.afterOp === "|" || next.afterOp === "|&")) {
		const reader = programOf(next.words);
		if ((SHELLS.has(reader) || INTERPRETERS.has(reader)) && !readsPipedDataAsData(next.words)) {
			return ask(REASON_DOWNLOADED_CODE, `${program} | ${reader} ejecuta código descargado`);
		}
	}

	return classifyWords(command.words, cwd, depth);
}

/**
 * Verdict for a whole command line. Every simple command of the line is classified,
 * because `true && rm -rf x` destroys exactly as much as `rm -rf x` does.
 */
function classifyCommandString(command: string, cwd: string, depth: number): Verdict {
	if (depth > 4) return ask("No puedo revisar un comando tan anidado.", "anidamiento de comandos demasiado profundo");

	const tokens = tokenize(command);
	if (tokens === null) {
		return ask("No pude interpretar el comando: tiene comillas sin cerrar.", "no pude interpretar el comando: comillas sin cerrar");
	}

	const commands = splitCommands(tokens);
	// A line this long is not something the owner can review on a phone anyway.
	if (commands.length > 200) {
		return ask("No puedo revisar un comando con tantas partes.", "el comando tiene demasiadas partes para revisarlo");
	}

	for (let index = 0; index < commands.length; index += 1) {
		const verdict = classifySimpleCommand(commands[index], commands[index + 1], cwd, depth);
		if (verdict.ask) return verdict;
	}
	return allow();
}

/** Verdict for the `bash` tool. A missing command is not decidable, so it asks. */
function classifyBash(input: unknown, cwd: string): Verdict {
	if (typeof input !== "object" || input === null) return ask("No pude leer el comando.", "no pude leer el comando");
	const command = (input as Json).command;
	if (typeof command !== "string") return ask("No pude leer el comando.", "no pude leer el comando");
	return classifyCommandString(command, cwd, 0);
}

/**
 * Verdict for the `write` tool (T22): a new file inside `$HOME` needs no
 * permission, an existing file (it overwrites the whole content) and anything
 * outside `$HOME` do.
 */
function classifyWrite(input: unknown, cwd: string): Verdict {
	const raw = readPathField(input);
	if (raw === null) return ask("No pude leer la ruta del archivo.", "no pude leer la ruta del archivo");

	const absolutePath = resolveUserPath(raw, cwd);
	if (absolutePath === null) return ask("No puedo comprobar la ruta del archivo.", "no puedo resolver la ruta del archivo");
	const shown = displayPath(absolutePath);
	if (isOutsideHome(absolutePath)) {
		return ask(`Va a escribir fuera de tu carpeta personal: ${shown}.`, `escribe fuera de $HOME: ${shown}`);
	}
	if (existsSync(absolutePath)) return ask(`Va a sobrescribir ${shown}.`, `sobrescribe ${shown}`);
	return allow();
}

/**
 * Words that name a destruction when they are one whole component of a tool name
 * (T23). A tool the gate has no rule for is not asked by default: Pi loads the
 * global configuration, so the model carries whatever the packages register
 * (`web_search`, `fetch_content`, `memory_save`, the subagent tools…), and a
 * button per package tool is the same toll booth one layer out. The ask is kept
 * for the names that say by themselves that something is destroyed.
 */
const DESTRUCTIVE_WORDS = ["delete", "remove", "drop", "destroy", "purge", "wipe", "truncate", "reset", "kill", "unlink", "rm"];

/** Suffixes a name component may carry on top of a stem: plurals and verb forms. */
const COMPONENT_SUFFIXES = ["ing", "ed", "es", "ers", "er", "s", "d"];

/**
 * Stem of one component, so `remove`, `removing` and `removed` land on the same
 * string. Only `ing`/`ed`/`es`/`er`/`s`/`d` are stripped, and only when a stem of
 * at least two letters is left (`rm` is short on purpose, not a suffix away from
 * nothing). A trailing `e` goes too, which merges `delete`/`deleted` and
 * `purge`/`purged`.
 */
function stemOf(component: string): string {
	const lower = component.toLowerCase();
	let stem = lower;
	for (const suffix of COMPONENT_SUFFIXES) {
		if (lower.endsWith(suffix) && lower.length - suffix.length >= 2) {
			stem = lower.slice(0, lower.length - suffix.length);
			break;
		}
	}
	return stem.endsWith("e") ? stem.slice(0, -1) : stem;
}

/** Stems of the vocabulary, compared against the stem of each name component. */
const DESTRUCTIVE_STEMS = new Set(DESTRUCTIVE_WORDS.map((word) => stemOf(word)));

/**
 * Components of a tool name: `delete_file`, `removeRepo`, `resetHTTPSession` and
 * `DELETE_FILE` all split into the words a reader sees. Only whole components are
 * compared, so `address_lookup` never matches `rm`.
 */
function nameComponents(name: string): string[] {
	const components: string[] = [];
	for (const chunk of name.split(/[^A-Za-z0-9]+/)) {
		if (chunk.length === 0) continue;
		// A capital starts a component (`removeRepo`), a lower-to-upper run starts one
		// (`HTTPSession` → `HTTP`, `Session`) and so does a digit boundary. A chunk that
		// is all capitals (`DELETE`) stays whole instead of splitting per letter.
		for (const part of chunk.split(
			/(?<=[a-z0-9])(?=[A-Z])|(?<=[A-Z])(?=[A-Z][a-z])|(?<=[A-Za-z])(?=[0-9])|(?<=[0-9])(?=[A-Za-z])/,
		)) {
			if (part.length > 0) components.push(part);
		}
	}
	return components;
}

/** True when a tool name says, by itself, that it destroys something. */
function namesDestruction(name: string): boolean {
	return nameComponents(name).some((component) => DESTRUCTIVE_STEMS.has(stemOf(component)));
}

/** Verdict for one tool call, before anything runs. */
function classifyCall(name: string, input: unknown, cwd: string): Verdict {
	if (APPROVAL_FREE.has(name)) return allow();
	if (name === "bash") return classifyBash(input, cwd);
	if (name === "write") return classifyWrite(input, cwd);
	// A tool with no rule of its own is not a decision the owner must answer on a
	// phone: the packages he loaded are part of the agent he asked for. Only the
	// names that say they destroy keep their button, and the match never fails open.
	if (namesDestruction(name)) {
		return ask(`La herramienta ${name} puede borrar datos.`, `el nombre de la herramienta ${name} indica que borra o destruye`);
	}
	return allow();
}

/**
 * Writes one audit line per decision to stderr, which the bridge forwards to the
 * journal as `bridge: pi stderr: …`. A real turn can then be replayed: what ran
 * unasked, what asked, and which rule fired. The line is technical on purpose
 * (`detail`, never the sentence the owner reads): `git push` and `git reset
 * --hard` share a chat sentence, and the journal must still tell them apart.
 */
function logDecision(name: string, verdict: Verdict, input: unknown): void {
	const decision = verdict.ask ? `ask (${verdict.detail})` : "allow";
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
			approved = await ctx.ui.confirm(APPROVAL_TITLE, approvalMessage(verdict.reason, event.input));
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
