/**
 * Pi extension: the four deterministic Alter tools (S7 of the phase-1 Telegram
 * bridge).
 *
 * Pi never receives `bash`, `edit` or `write`. Every vault mutation and every
 * message to the owner goes through these tools, which forward the call to the
 * bridge's loopback tools server:
 *
 *   ALTER_TOOLS_URL    base URL, for example http://127.0.0.1:43117
 *   ALTER_TOOLS_TOKEN  per-process secret sent as the X-Alter-Token header
 *
 * Both variables are set by the bridge in Pi's environment; the model never
 * chooses them and never sees the token.
 *
 * Load it explicitly and list the four tools in the allowlist, because `--tools`
 * replaces Pi's whole tool selection instead of extending it:
 *
 *   pi -e internal/tools/extension/index.ts \
 *      --tools read,grep,find,ls,save_note,append_note,send_file,ask_options
 *
 * Dependency-free on purpose: only the global `fetch` is used, so the extension
 * works without installing anything into the Pi package.
 */

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

export default function alterTools(pi: ExtensionAPI): void {
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
