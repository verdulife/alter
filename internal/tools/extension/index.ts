/**
 * Pi extension: Alter's identity block, the approval gate for destructive tools
 * and the four deterministic Alter tools (S7, T12 and T13 of the phase-1 Telegram
 * bridge).
 *
 * The gate intercepts `tool_call`: `bash`, `write` and `edit` ask the owner before
 * they run, the read-only tools and the four Alter tools never do, and everything
 * else is asked about too. The question is `ctx.ui.confirm()`, which in RPC mode
 * the bridge turns into two Telegram buttons; no answer means blocked.
 *
 * The four tools forward every vault mutation and every message to the owner to
 * the bridge's loopback tools server:
 *
 *   ALTER_TOOLS_URL    base URL, for example http://127.0.0.1:43117
 *   ALTER_TOOLS_TOKEN  per-process secret sent as the X-Alter-Token header
 *
 * Both variables are set by the bridge in Pi's environment; the model never
 * chooses them and never sees the token. They are additive on top of Pi's own
 * tools: the bridge no longer restricts the toolset (S7), so `bash`, `read`,
 * `write`, `edit`, `grep`, `find`, `ls` and whatever the loaded packages provide
 * are still there.
 *
 * The extension is loaded by the bridge with `-e`, next to the persona file that
 * `--append-system-prompt` injects.
 *
 * Dependency-free on purpose: only `node:fs`/`node:path` and the global `fetch`
 * are used, so the extension works without installing anything into the Pi
 * package.
 */

import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
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
const SAVE_NOTE_PARAMETERS: any = {
	type: "object",
	properties: {
		title: {
			type: "string",
			description: "Título de la nota. Se convierte en el H1 y en el nombre del archivo; no admite '/' ni '..'.",
		},
		body: {
			type: "string",
			description: "Cuerpo en markdown, sin el H1 (el frontmatter y el título los añade el vault).",
		},
		folder: {
			type: "string",
			description: "Carpeta destino relativa al vault, por ejemplo 'alter'. Vacío = raíz del vault.",
		},
		tags: {
			type: "array",
			items: { type: "string" },
			description: "Etiquetas opcionales para el frontmatter YAML.",
		},
	},
	required: ["title", "body"],
	additionalProperties: false,
};

const APPEND_NOTE_PARAMETERS: any = {
	type: "object",
	properties: {
		note: {
			type: "string",
			description: "Ruta de una nota existente, relativa al vault, por ejemplo 'alter/ideas.md'.",
		},
		line: {
			type: "string",
			description: "Línea markdown que se añade al final de la nota.",
		},
	},
	required: ["note", "line"],
	additionalProperties: false,
};

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
 * Tools that always need the owner's approval before they run (T13). `bash` can
 * touch anything under `$HOME`, and `write`/`edit` change files in place: with no
 * terminal in front of Pi, the button is the only real approval that exists.
 */
const APPROVAL_REQUIRED = new Set(["bash", "write", "edit"]);

/**
 * Tools that never need approval: the read-only built-ins, which cannot change
 * anything, and the four deterministic Alter tools, which the bridge validates
 * against the vault and the owner chat on its own.
 */
const APPROVAL_FREE = new Set([
	"read",
	"grep",
	"find",
	"ls",
	"save_note",
	"append_note",
	"send_file",
	"ask_options",
]);

/** Longest argument preview the approval prompt carries, matching the bridge cap. */
const APPROVAL_ARG_LIMIT = 240;

/**
 * Renders the arguments of a tool call as one short line for the approval prompt.
 * The bridge escapes and truncates it again, so this only keeps the request small.
 */
function previewArguments(input: unknown): string {
	let rendered: string;
	try {
		rendered = typeof input === "object" && input !== null ? JSON.stringify(input) : String(input);
	} catch {
		rendered = "[argumentos no representables]";
	}
	rendered = (rendered ?? "").replace(/\s+/g, " ").trim();
	return rendered.length > APPROVAL_ARG_LIMIT ? `${rendered.slice(0, APPROVAL_ARG_LIMIT)}…` : rendered;
}

/** Failed tool result: the model sees the reason and must not retry blindly. */
function failed(text: string): ToolResult {
	return { content: [{ type: "text", text }], details: {}, isError: true };
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

	// T13: approve every destructive tool call through the owner's buttons.
	//
	// The gate runs on `tool_call`, which Pi awaits before executing anything and
	// which can stop the call with `{ block: true, reason }`. The question travels
	// through `ctx.ui.confirm()`; in RPC mode that becomes an
	// `extension_ui_request`, and the bridge answers it with the two Telegram
	// buttons (or with `cancelled`).
	//
	// Fail-closed on every axis: a denial, a cancellation (RPC delivers both as
	// `false`), a thrown error and anything the bridge cannot ask about all block
	// the call. A tool that is neither explicitly exempted nor destructive is
	// still confirmed, so a tool added by a loaded package can never slip past the
	// gate unnoticed.
	pi.on("tool_call", async (event, ctx) => {
		const name = event.toolName;
		if (APPROVAL_FREE.has(name)) {
			return undefined;
		}

		const destructive = APPROVAL_REQUIRED.has(name);
		const title = destructive ? `¿Ejecutar ${name}?` : `¿Permitir la herramienta ${name}?`;
		const message = previewArguments(event.input);

		let approved: boolean;
		try {
			approved = await ctx.ui.confirm(title, message);
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
		name: "save_note",
		label: "Guardar nota",
		description:
			"Guarda una nota markdown nueva en el vault del dueño y devuelve su ruta relativa. El vault añade el frontmatter YAML (title, tags, date) y el H1 con el título.",
		parameters: SAVE_NOTE_PARAMETERS,
		async execute(_toolCallId, params, signal) {
			const payload: Json = { title: params.title, body: params.body };
			if (params.folder) payload.folder = params.folder;
			if (Array.isArray(params.tags) && params.tags.length > 0) payload.tags = params.tags;

			const reply = await callTools("save_note", payload, signal);
			if (!reply.ok) return failed(reply.error);

			const path = text(reply.data, "path");
			return ok(`Nota guardada en el vault: ${path}`, { path });
		},
	});

	pi.registerTool({
		name: "append_note",
		label: "Añadir línea",
		description:
			"Añade una línea al final de una nota que ya existe en el vault. Si la nota no existe o la ruta sale del vault, falla y no crea nada.",
		parameters: APPEND_NOTE_PARAMETERS,
		async execute(_toolCallId, params, signal) {
			const reply = await callTools("append_note", { note: params.note, line: params.line }, signal);
			if (!reply.ok) return failed(reply.error);

			const path = text(reply.data, "path");
			return ok(`Línea añadida a ${path}`, { path });
		},
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
