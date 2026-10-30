/**
 * Pi extension: Alter's identity block, the approval gate for destructive tools
 * and the two deterministic Alter tools (S7, T12, T13, T17 and T22 of the phase-1
 * Telegram bridge).
 *
 * The gate intercepts `tool_call` and asks the owner only three things: the loss of
 * data, an irreversible publication and a change to Pi's own tool set. `edit` never
 * asks and `write` asks on one path only, a target inside a `.ssh` directory, where
 * a key decides who gets in; `bash` asks only for the programs that delete data,
 * rewrite published history, publish, or install and remove Pi packages, plus the
 * two escalation programs (`sudo`, `doas`), which reach another user's powers
 * whatever they run. Permissions, services, installers, downloaded code, shell
 * redirections and every other path were dropped on purpose, and
 * `docs/alter-limites.md` states what that leaves unprotected. The read-only tools
 * and the two Alter tools never ask, and a tool the gate has no rule for asks only
 * when its own name says it destroys something (`delete_file`, `reset_session`): Pi
 * loads the global configuration (S7), so a button per package tool would rebuild
 * the toll booth one layer out. The question is `ctx.ui.confirm()`, which in RPC
 * mode the bridge turns into two Telegram buttons; no answer means blocked.
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
 * anything, and the two deterministic Alter tools are validated by the bridge
 * against the vault and the owner chat. `edit` is a surgical change whose diff is
 * the review, and `write` stopped asking for the ordinary case: writing a file,
 * over an empty path or over a full one, is not one of the three things the owner
 * chose to gate, so it asks for nothing even when the target exists and even
 * outside `$HOME`. It keeps one guard of its own (`classifyWrite`): a target inside
 * a `.ssh` directory, where a key decides who gets in. The button is otherwise
 * reserved for the loss of data, the irreversible publication and Pi's own tool
 * set.
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

/** Contents of a path field of a tool call, or null when the field is missing. */
function readPathField(input: unknown): string | null {
	if (typeof input !== "object" || input === null) return null;
	const value = (input as Json).path;
	return typeof value === "string" ? value : null;
}

/**
 * A path the model wrote with the shell's own variable (`$HOME/.ssh/config`)
 * instead of `~`. Pi does not expand a variable, so the gate expands this one
 * spelling itself: it is the spelling the owner named when he asked for the `.ssh`
 * guard back. Every other variable is left alone, so `$SINK` stays a path this gate
 * cannot judge.
 */
function expandHomeVariable(raw: string): string {
	const home = homeDir();
	if (raw === "$HOME" || raw === "${HOME}") return home;
	if (raw.startsWith("$HOME/")) return `${home}/${raw.slice("$HOME/".length)}`;
	if (raw.startsWith("${HOME}/")) return `${home}/${raw.slice("${HOME}/".length)}`;
	return raw;
}

/**
 * The one directory where a write is not ordinary work. `~/.ssh` holds the keys and
 * the `authorized_keys` that say who may enter the owner's servers: rewriting it
 * hands over access instead of losing data, so it is the single path `write` asks
 * about. The list stays one entry long on purpose — `.gnupg`, `.aws`, `/etc` and
 * the shell start-up files are sensitive too, and the owner chose not to reopen
 * them.
 */
const SSH_DIRECTORY = ".ssh";

/**
 * True when the resolved path sits inside a `.ssh` directory, at any depth. The
 * component is compared whole, so `~/.ssh/config` and `/srv/x/.ssh/authorized_keys`
 * match while `~/.ssh-otra/clave` does not.
 */
function isInsideSshDirectory(absolutePath: string): boolean {
	return absolutePath.split(sep).some((component) => component === SSH_DIRECTORY);
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

/** Failed tool result: the model sees the reason and must not retry blindly. */
function failed(text: string): ToolResult {
	return { content: [{ type: "text", text }], details: {}, isError: true };
}

/**
 * Classifier for `bash` (T22). The owner keeps a button for three things only, so
 * the gate asks when the command loses data, rewrites published history, publishes
 * an artifact or changes Pi's own tool set, and stays quiet for the rest: `ls`,
 * `cat`, `grep`, `go test`, `git status`, `git commit`, `chmod`, `systemctl`,
 * `kill`, `apt install`, `pip install`, `npx`, `curl … | sh` and every shell
 * redirection.
 *
 * Two rules keep it from being decorative:
 *
 *   - it matches every simple command of the line, not the first token, because
 *     `git -C x push`, `sudo rm` and `FOO=1 rm -rf` are the same act as `git push`
 *     and `rm`;
 *   - what it cannot decide, it asks. An unbalanced quote and an `mv` whose
 *     destination it cannot resolve ask, never fail open.
 */

/** One shell word, one operator or one redirection. */
type Token = { kind: "word" | "op" | "redir"; value: string };

/**
 * One simple command of a line: its words, the redirections it consumed and the
 * operator before it. Nothing reads `redirs` any more —no shell-writing rule is
 * left— but the redirection target is still kept out of the words, so
 * `echo x > notas.md` never sees `notas.md` as an argument of `echo`.
 */
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

/** Shells that run inline code with `-c`, so the code inside that word is classified. */
const SHELLS = new Set(["sh", "bash", "zsh", "dash", "ksh", "fish", "ash", "busybox"]);

/**
 * Programs that only run the command after them. The program is skipped and the
 * inner command classified, so `env FOO=1 rm x` and `xargs rm` hit the `rm` rule.
 * `sudo`/`doas` are in this set because the command after them is what runs, but
 * they also carry their own rule before this set is consulted: the escalation asks
 * by itself, whatever the inner command is.
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
 * The sentences the owner reads. Each one names the consequence in the second
 * person and never the program: "rm borra archivos" says nothing a person can
 * weigh on a phone, while "Va a borrar archivos." says what he is deciding. The
 * program and its flags stay in the journal, through each rule's `detail`.
 *
 * There are only as many sentences as families the owner still gates: data loss,
 * published history, containers, publication and Pi's tool set, plus the two
 * exceptions he asked back, the `.ssh` write and the privilege escalation.
 */
const REASON_DELETE_FILES = "Va a borrar archivos.";
const REASON_ESCALATION = "Va a escalar a los permisos de otro usuario.";
const REASON_SSH_WRITE = "Va a escribir en tu carpeta .ssh, donde las claves dan acceso a tus servidores.";
const REASON_HISTORY = "Va a publicar o reescribir el historial del repositorio.";
const REASON_CONTAINERS = "Va a borrar contenedores o volúmenes.";
const REASON_PUBLISH = "Va a publicar un paquete de forma irreversible.";
const REASON_PUBLISH_RELEASE = "Va a publicar o borrar una versión de forma irreversible.";
const REASON_PUBLISH_IMAGE = "Va a publicar una imagen de forma irreversible.";
const REASON_PI_TOOLSET = "Va a cambiar las herramientas de Pi en las que confías.";

/** One gate rule: what the owner reads, and what the journal records. */
type Rule = { reason: string; detail: string };

/**
 * Programs that ask without reading their arguments: every one of them loses data
 * on its own (deleting, truncating, reformatting a disk). Permissions and
 * ownership, services and processes, and the package installers used to be here
 * and were removed on purpose: the owner gates data loss, publication and Pi's
 * tool set, and nothing else. The privilege escalation is not in this table
 * because it has its own rule in `classifyWords`: `sudo`/`doas` reach another
 * user's powers whatever the command behind them is. `find`, `mv`, `git`,
 * `docker` and the publishers keep their own reader further down.
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
};

/** Docker flags that take a value, so the value is not mistaken for a subcommand. */
const DOCKER_VALUE_FLAGS = new Set(["-H", "--host", "--context", "--config", "--log-level", "-l"]);

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

/** Programs that download and run a package on every invocation. */
const PACKAGE_RUNNERS = new Set(["npx", "bunx"]);

/** Package managers whose subcommand downloads and runs a package. */
const PACKAGE_RUNNER_SUBCOMMANDS: Record<string, string> = {
	npm: "exec",
	pnpm: "dlx",
	yarn: "dlx",
};

/**
 * Words a package runner was asked to run, or null when the invocation runs no
 * command at all (`npx` alone). The runner only downloads a package, which the
 * owner stopped gating; what it runs is classified as the command it is, so
 * `npx npm publish` keeps its publish button and `npx cowsay hola` passes.
 */
function runnerInnerWords(program: string, args: string[]): string[] | null {
	const rest = [...args];
	const subcommand = PACKAGE_RUNNER_SUBCOMMANDS[program];
	if (subcommand !== undefined) {
		let index = 0;
		while (index < rest.length && rest[index].startsWith("-")) index += 1;
		if (rest[index] !== subcommand) return null;
		rest.splice(0, index + 1);
	}
	return stripWrapperArgs(program, rest);
}

/**
 * Programs whose upload subcommand publishes an artifact the gate cannot take
 * back: the version is claimed in the registry forever, and nothing local records
 * what was sent. Each family keeps its own word for the act, so the subcommand is
 * stored instead of assumed, and the reads of the same program (`npm view`,
 * `cargo search`, `helm list`) are not here and keep passing.
 */
const PUBLISH_SUBCOMMANDS: Record<string, string> = {
	npm: "publish",
	pnpm: "publish",
	yarn: "publish",
	bun: "publish",
	cargo: "publish",
	gem: "push",
	twine: "upload",
	helm: "push",
};

/**
 * Flags a package manager may put before its subcommand, with the value they
 * swallow (`npm --prefix ~/x publish`): without them the value would be read as
 * the subcommand and the publish would walk through unseen.
 */
const PUBLISH_VALUE_FLAGS = new Set(["-C", "--prefix", "-w", "--workspace", "--registry", "--userconfig"]);

/** `gh` global flags that take a value, so the value is not read as the subcommand. */
const GH_VALUE_FLAGS = new Set(["-R", "--repo", "--hostname", "--config-dir"]);

/** `pi` package subcommands that change the set of tools the owner trusts. */
const PI_TOOLSET_SUBCOMMANDS = new Set(["install", "add", "remove", "uninstall"]);

/** `pi` global flags that take a value, so the value is not read as the subcommand. */
const PI_VALUE_FLAGS = new Set(["-e", "--extension", "-m", "--model"]);

/** `pi` flags that write the project settings instead of the global ones. */
const PI_LOCAL_FLAGS = new Set(["-l", "--local"]);

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

/**
 * Verdict for `gh`: creating or deleting a release publishes outside the machine,
 * and neither can be undone. Everything else `gh` does (`gh release list`,
 * `gh release view`, `gh pr view`) keeps passing.
 */
function classifyGh(args: string[]): Verdict {
	const rest = [...args];
	while (rest.length > 0 && rest[0].startsWith("-")) {
		const flag = rest.shift() as string;
		if (GH_VALUE_FLAGS.has(flag)) rest.shift();
	}
	if (rest[0] !== "release") return allow();
	const action = rest[1] ?? "";
	if (action === "create" || action === "delete") {
		return ask(REASON_PUBLISH_RELEASE, `gh release ${action} publica o borra una versión`);
	}
	return allow();
}

/**
 * Verdict for `pi`: installing or removing a package changes the extensions,
 * skills and prompts the owner trusts, and it writes `~/.pi/agent/settings.json`
 * unless it is asked for project scope. A global change therefore answers with a
 * button; `--local`/`-l` only writes `.pi/settings.json`, which Pi loads after
 * project trust, so it keeps passing, and so does every read (`pi list`).
 */
function classifyPi(args: string[]): Verdict {
	const subcommand = firstOperand(args, PI_VALUE_FLAGS);
	if (subcommand === null || !PI_TOOLSET_SUBCOMMANDS.has(subcommand)) return allow();
	if (args.some((argument) => PI_LOCAL_FLAGS.has(argument))) return allow();
	return ask(REASON_PI_TOOLSET, `pi ${subcommand} cambia las herramientas cargadas`);
}

/** Verdict for a `docker`/`podman` invocation: removals and pushes ask. */
function classifyContainer(program: string, args: string[]): Verdict {
	const rest = [...args];
	while (rest.length > 0 && rest[0].startsWith("-")) {
		const flag = rest.shift() as string;
		if (DOCKER_VALUE_FLAGS.has(flag)) rest.shift();
	}
	const subcommand = rest[0] ?? "";
	const subArgs = rest.slice(1);

	// `push` uploads a tag to a registry: the image leaves the machine and the tag
	// it claims cannot be taken back, unlike `pull`, `images` or `inspect`.
	if (subcommand === "push") {
		return ask(REASON_PUBLISH_IMAGE, `${program} push publica una imagen`);
	}
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

/** Verdict for the words of one simple command. */
function classifyWords(input: string[], cwd: string, depth: number): Verdict {
	if (depth > 4) return ask("No puedo revisar un comando tan anidado.", "anidamiento de comandos demasiado profundo");

	const words = [...input];
	while (words.length > 0 && ENV_ASSIGNMENT.test(words[0])) words.shift();
	// Only assignments left (`FOO=1`): the shell sets a variable for nothing, it
	// has no other effect, so there is nothing to ask about.
	if (words.length === 0) return allow();

	const program = basename(words[0]);
	const args = words.slice(1);

	// The escape hatch itself. Reaching another user's privileges is not undone by
	// anything, and `sudo bash -c '…'` opens a root shell that no data-loss rule
	// would stop. The command inside is classified first, so `sudo rm x` still
	// reports the deletion it causes and the journal keeps the concrete rule; the
	// escalation answers for everything the inner command leaves open (`sudo ls`,
	// `sudo -i`, `sudo bash -c '…'`).
	if (program === "sudo" || program === "doas") {
		const inner = stripWrapperArgs(program, args);
		if (inner.length > 0) {
			const innerVerdict = classifyWords(inner, cwd, depth + 1);
			if (innerVerdict.ask) return innerVerdict;
		}
		return ask(REASON_ESCALATION, `${program} escala privilegios`);
	}

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

	// A package runner downloads a package and runs it. Downloading stopped being a
	// reason to ask, but the command it runs is still read, so a publication behind
	// a runner (`npx npm publish`) keeps its button.
	if (PACKAGE_RUNNERS.has(program) || PACKAGE_RUNNER_SUBCOMMANDS[program] !== undefined) {
		const inner = runnerInnerWords(program, args);
		if (inner !== null) {
			if (inner.length === 0) return allow();
			return classifyWords(inner, cwd, depth + 1);
		}
		// `npm publish` is the package manager itself and not a runner, so it falls
		// through to the publish rule below.
	}
	// An upload to a registry cannot be undone, so every form of it asks. The
	// check reads the real subcommand, not the first word after the program, so a
	// value flag (`npm --prefix x publish`) cannot hide it.
	const publishSubcommand = PUBLISH_SUBCOMMANDS[program];
	if (publishSubcommand !== undefined && firstOperand(args, PUBLISH_VALUE_FLAGS) === publishSubcommand) {
		return ask(REASON_PUBLISH, `${program} ${publishSubcommand} publica un paquete`);
	}

	if (program === "git") return classifyGit(args);
	if (program === "gh") return classifyGh(args);
	if (program === "pi") return classifyPi(args);
	if (program === "docker" || program === "podman" || program === "docker-compose") {
		return classifyContainer(program === "docker-compose" ? "docker" : program, program === "docker-compose" ? ["compose", ...args] : args);
	}

	if (program === "find") return classifyFind(args, cwd, depth);
	if (program === "mv") return classifyMove(args, cwd);

	return allow();
}

/**
 * First operand of a program whose flags may come before its subcommand. The
 * flags are skipped, and so is the value of the ones that take one, so
 * `npm --prefix ~/x publish` still reveals `publish` and `pi --extension x list`
 * still reveals `list` instead of stopping at the flag. Null when the arguments
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

/** `find` predicates that run another program. */
const FIND_EXEC_FLAGS = new Set(["-exec", "-execdir", "-ok", "-okdir"]);

/** `find` predicates that destroy a match or truncate a file of their own. */
const FIND_WRITE_FLAGS = new Set(["-delete", "-fprint", "-fprint0", "-fprintf", "-fls"]);

/**
 * Verdict for `find`.
 *
 * `-delete` and the predicates that truncate a file of their own keep asking
 * unconditionally. `-exec`, `-execdir`, `-ok` and `-okdir` run a program the gate
 * never sees as a program of its own, so the words after the predicate are
 * classified as the command they are: `find . -name x -exec rm {} \;` deletes
 * exactly as `rm` does and keeps its button, while `find . -name '*.go' -exec ls -l
 * {} +` reads and asks for nothing.
 */
function classifyFind(args: string[], cwd: string, depth: number): Verdict {
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
		// One line can carry several `-exec` predicates, so the scan keeps going after
		// the `;` or `+` that closes this one.
		let end = index + 1;
		while (end < args.length && args[end] !== ";" && args[end] !== "+") end += 1;
		if (end === index + 1) {
			return ask("No pude interpretar el comando: `find -exec` sin programa.", `find ${argument} sin programa: no reconozco la operación`);
		}
		const verdict = classifyWords(args.slice(index + 1, end), cwd, depth + 1);
		if (verdict.ask) return verdict;
		index = end + 1;
	}
	return allow();
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

/** Verdict for one simple command. The redirections were already split off by
 * `splitCommands`, and they ask for nothing: the owner dropped every shell-writing
 * rule, so `>`, `>>` and `|` are read as the plain operators they are.
 *
 * No shortcut on an empty program: `sudo -i` is a wrapper with nothing after it, so
 * the wrapper itself is what has to be judged. `classifyWords` already allows a
 * simple command made only of assignments.
 */
function classifySimpleCommand(command: SimpleCommand, cwd: string, depth: number): Verdict {
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
		const verdict = classifySimpleCommand(commands[index], cwd, depth);
		if (verdict.ask) return verdict;
	}
	return allow();
}

/**
 * Verdict for the `write` tool (T22). `write` overwrites a whole file without a
 * button, with one exception: a target inside a `.ssh` directory, because the keys
 * stored there are what lets a machine into the owner's servers and an overwritten
 * `authorized_keys` is access handed over, not data lost. Every other path stays
 * silent — an existing file, a path outside `$HOME`, `/etc`, `.gnupg`, `.aws`, a
 * shell start-up — and so does a path this gate cannot resolve: the owner asked for
 * exactly one guard back, and widening it would rebuild the toll booth.
 */
function classifyWrite(input: unknown, cwd: string): Verdict {
	const raw = readPathField(input);
	if (raw === null) return allow();
	const absolutePath = resolveUserPath(expandHomeVariable(raw), cwd);
	if (absolutePath === null) return allow();
	if (!isInsideSshDirectory(absolutePath)) return allow();
	return ask(REASON_SSH_WRITE, `write en ${displayPath(absolutePath)}, una ruta de claves ssh`);
}

/** Verdict for the `bash` tool. A missing command is not decidable, so it asks. */
function classifyBash(input: unknown, cwd: string): Verdict {
	if (typeof input !== "object" || input === null) return ask("No pude leer el comando.", "no pude leer el comando");
	const command = (input as Json).command;
	if (typeof command !== "string") return ask("No pude leer el comando.", "no pude leer el comando");
	return classifyCommandString(command, cwd, 0);
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

	// T13/T22: approve the tool calls that lose data, publish or change Pi's tools.
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
