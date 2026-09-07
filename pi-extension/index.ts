import type {
	BashOperations,
	ExtensionAPI,
	ExtensionCommandContext,
	ExtensionContext,
} from "@earendil-works/pi-coding-agent";
import {
	createBashTool,
	createEditTool,
	createReadTool,
	createWriteTool,
	getAgentDir,
} from "@earendil-works/pi-coding-agent";
import {
	createCipheriv,
	createDecipheriv,
	createHash,
	createHmac,
	randomBytes,
	randomUUID,
} from "node:crypto";
import { mkdir, readFile, rename, unlink, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { Type } from "typebox";

const STATE_ENTRY = "pi-remote-state";

interface TargetInfo {
	protocol_version: number;
	worker_id: string;
	os: string;
	arch: string;
	hostname: string;
	root: string;
	tools: string[];
	shell_profile: string;
}

interface TargetsResponse {
	remote: boolean;
	remote_info: TargetInfo;
	workers: TargetInfo[];
}

interface RPCErrorBody {
	code: string;
	message: string;
	details?: unknown;
}

interface RPCEnvelope<T> {
	id: string;
	ok: boolean;
	result?: T;
	error?: RPCErrorBody;
}

interface ReadResult {
	path: string;
	content?: string;
	content_base64?: string;
	encoding: "utf-8" | "base64";
	mime_type?: string;
	start_line?: number;
	end_line?: number;
	total_lines?: number;
	truncated?: boolean;
	next_offset?: number;
}

interface FindResult {
	path: string;
	is_directory: boolean;
}

interface RemoteImage {
	type: "image";
	data: string;
	mimeType: string;
}

interface BashResult {
	output: string;
	exit_code: number;
	truncated?: boolean;
	timed_out?: boolean;
	shell_profile: string;
}

interface MutationResult {
	message: string;
	path: string;
	bytes?: number;
	replacements?: number;
	first_changed_line?: number;
}

interface PersistedState {
	enabled: boolean;
	url: string;
	target: string;
	targetNotes?: Record<string, string>;
	info?: TargetInfo;
	lastError?: string;
}

interface RuntimeState extends Omit<PersistedState, "targetNotes"> {
	token: string;
	targetNotes: Record<string, string>;
}

interface RemoteConnection {
	id: string;
	url: string;
	target: string;
	note: string;
	token: string;
}

interface RemoteConnectionStatus {
	connection: RemoteConnection;
	targets?: TargetsResponse;
	online: boolean;
	error?: string;
}

interface RemoteStore {
	version: 1;
	active?: string;
	connections: RemoteConnection[];
}

const REMOTE_STORE_PATH = join(getAgentDir(), "remote.json");

class RemoteRPCError extends Error {
	readonly code: string;
	readonly details: unknown;

	constructor(error: RPCErrorBody) {
		super(error.message);
		this.name = "RemoteRPCError";
		this.code = error.code;
		this.details = error.details;
	}
}

interface SecureEnvelope {
	v: number;
	nonce: string;
	ciphertext: string;
}

const secureVersion = 1;
const httpClientPurpose = "http-client";
const httpServerPurpose = "http-server";

function base64UrlEncode(value: Uint8Array): string {
	return Buffer.from(value).toString("base64url");
}

function base64UrlDecode(value: string): Buffer {
	return Buffer.from(value, "base64url");
}

function deriveSecureKey(token: string, purpose: string): Buffer {
	return createHash("sha256")
		.update(`pi-remote/secure/v1/${purpose}\0`, "utf8")
		.update(token, "utf8")
		.digest();
}

function httpSecureAAD(direction: string, method: string, path: string, requestNonce: string): string {
	return `http/v1\n${direction}\n${method}\n${path}\n${requestNonce}`;
}

function encryptSecureEnvelope(token: string, purpose: string, aad: string, plaintext: Uint8Array): string {
	const nonce = randomBytes(12);
	const cipher = createCipheriv("aes-256-gcm", deriveSecureKey(token, purpose), nonce);
	cipher.setAAD(Buffer.from(aad, "utf8"));
	const ciphertext = Buffer.concat([cipher.update(Buffer.from(plaintext)), cipher.final(), cipher.getAuthTag()]);
	return JSON.stringify({
		v: secureVersion,
		nonce: base64UrlEncode(nonce),
		ciphertext: base64UrlEncode(ciphertext),
	} satisfies SecureEnvelope);
}

function decryptSecureEnvelope(token: string, purpose: string, aad: string, serialized: string): Buffer {
	let envelope: SecureEnvelope;
	try {
		envelope = JSON.parse(serialized) as SecureEnvelope;
	} catch (error) {
		throw new Error(`Invalid secure response envelope: ${errorMessage(error)}`);
	}
	if (envelope.v !== secureVersion || typeof envelope.nonce !== "string" || typeof envelope.ciphertext !== "string") {
		throw new Error("Invalid secure response envelope");
	}
	const nonce = base64UrlDecode(envelope.nonce);
	const ciphertext = base64UrlDecode(envelope.ciphertext);
	if (nonce.length !== 12 || ciphertext.length < 16) throw new Error("Invalid secure response envelope");
	const decipher = createDecipheriv("aes-256-gcm", deriveSecureKey(token, purpose), nonce);
	decipher.setAAD(Buffer.from(aad, "utf8"));
	decipher.setAuthTag(ciphertext.subarray(ciphertext.length - 16));
	try {
		return Buffer.concat([
			decipher.update(ciphertext.subarray(0, ciphertext.length - 16)),
			decipher.final(),
		]);
	} catch {
		throw new Error("Secure response authentication failed");
	}
}

function createRequestAuth(token: string, method: string, path: string, body: Uint8Array, nonce = base64UrlEncode(randomBytes(16))): { nonce: string; headers: Record<string, string> } {
	const timestamp = Math.floor(Date.now() / 1000).toString();
	const proof = createHmac("sha256", token)
		.update(method, "utf8")
		.update("\n", "utf8")
		.update(path, "utf8")
		.update("\n", "utf8")
		.update(timestamp, "utf8")
		.update("\n", "utf8")
		.update(nonce, "utf8")
		.update("\n", "utf8")
		.update(base64UrlEncode(body), "utf8")
		.digest("base64url");
	return {
		nonce,
		headers: {
			"X-Pi-Remote-Nonce": nonce,
			"X-Pi-Remote-Timestamp": timestamp,
			"X-Pi-Remote-Proof": proof,
		},
	};
}

class RPCClient {
	constructor(
		private readonly baseURL: string,
		private readonly token: string,
	) {}

	async targets(signal?: AbortSignal): Promise<TargetsResponse> {
		return this.request<TargetsResponse>("/v1/targets", { method: "GET", signal });
	}

	async call<T>(target: string, tool: string, input: unknown, signal?: AbortSignal): Promise<T> {
		const envelope = await this.request<RPCEnvelope<T>>("/v1/rpc", {
			method: "POST",
			signal,
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ target, tool, input }),
		});
		if (!envelope.ok || !envelope.result) {
			throw new RemoteRPCError(envelope.error ?? { code: "rpc_error", message: "RPC returned no result" });
		}
		return envelope.result;
	}

	private async request<T>(path: string, init: RequestInit): Promise<T> {
		if (!this.token) throw new Error("PI remote token is not configured");
		const url = `${this.baseURL}${path}`;
		const parsedURL = new URL(url);
		const method = (init.method ?? "GET").toUpperCase();
		const plaintextBody = typeof init.body === "string" ? Buffer.from(init.body, "utf8") : new Uint8Array();
		const requestNonce = base64UrlEncode(randomBytes(16));
		const wireBody = plaintextBody.length > 0
			? encryptSecureEnvelope(this.token, httpClientPurpose, httpSecureAAD("request", method, parsedURL.pathname, requestNonce), plaintextBody)
			: undefined;
		const auth = createRequestAuth(this.token, method, parsedURL.pathname, wireBody ? Buffer.from(wireBody, "utf8") : new Uint8Array(), requestNonce);
		const headers = new Headers(init.headers);
		for (const [name, value] of Object.entries(auth.headers)) headers.set(name, value);
		const response = await fetch(url, { ...init, body: wireBody, headers });
		let body: unknown;
		const rawBody = Buffer.from(await response.arrayBuffer());
		const responseBody = response.headers.get("X-Pi-Remote-Encrypted") === "1"
			? decryptSecureEnvelope(this.token, httpServerPurpose, httpSecureAAD("response", method, parsedURL.pathname, auth.nonce), rawBody.toString("utf8")).toString("utf8")
			: rawBody.toString("utf8");
		try {
			body = JSON.parse(responseBody);
		} catch {
			throw new Error(`Gateway returned HTTP ${response.status} with a non-JSON body`);
		}
		if (!response.ok) {
			const envelope = body as RPCEnvelope<never>;
			if (envelope.error) throw new RemoteRPCError(envelope.error);
			throw new Error(`Gateway returned HTTP ${response.status}`);
		}
		return body as T;
	}
}

const readSchema = Type.Object({
	path: Type.String({ description: "Path relative to the active target workspace, or an absolute target path" }),
	offset: Type.Optional(Type.Number({ description: "Line number to start reading from (1-indexed)" })),
	limit: Type.Optional(Type.Number({ description: "Maximum number of lines to read" })),
});

const bashSchema = Type.Object({
	command: Type.String({ description: "Bash script to execute on the active target" }),
	timeout: Type.Optional(Type.Number({ description: "Timeout in seconds" })),
});

const editSchema = Type.Object({
	path: Type.String({ description: "Path relative to the active target workspace, or an absolute target path" }),
	edits: Type.Array(
		Type.Object({
			oldText: Type.String({ description: "Exact text that must occur exactly once in the original file" }),
			newText: Type.String({ description: "Replacement text" }),
		}),
	),
});

const writeSchema = Type.Object({
	path: Type.String({ description: "Path relative to the active target workspace, or an absolute target path" }),
	content: Type.String({ description: "Complete UTF-8 file content" }),
});

export default function piRemoteExtension(pi: ExtensionAPI) {
	pi.registerFlag("pi-remote-url", {
		description: "Pi remote Gateway URL (HTTP)",
		type: "string",
	});
	pi.registerFlag("pi-remote-token", {
		description: "Pi remote encryption token override (normally set by /remote connect)",
		type: "string",
	});
	pi.registerFlag("pi-remote-target", {
		description: "Initial target: off, gateway, or a reverse Worker ID",
		type: "string",
	});

	const localCwd = process.cwd();
	const localRead = createReadTool(localCwd);
	const localBash = createBashTool(localCwd);
	const localEdit = createEditTool(localCwd);
	const localWrite = createWriteTool(localCwd);

	let state: RuntimeState = createInitialState();
	let remoteAutocompleteRegistered = false;

	const client = (snapshot = state) => new RPCClient(snapshot.url, snapshot.token);

	pi.registerTool({
		name: "read",
		label: "read",
		description: "Read text or image content from the active local or remote target.",
		promptSnippet: "Read file contents on the active execution target",
		promptGuidelines: ["Use read to inspect files on the active execution target instead of cat or sed."],
		parameters: readSchema,
		async execute(id, params, signal, onUpdate, ctx) {
			if (!state.enabled) return localRead.execute(id, params, signal, onUpdate, ctx);
			const snapshot = { ...state };
			const result = await client(snapshot).call<ReadResult>(snapshot.target, "read", params, signal);
			const details = readDetails(result);
			if (result.encoding === "base64") {
				if (result.mime_type?.startsWith("image/") && result.content_base64) {
					return {
						content: [
							{ type: "text", text: `Read remote image ${result.path} [${result.mime_type}]` },
							{ type: "image", data: result.content_base64, mimeType: result.mime_type },
						],
						details,
					};
				}
				return {
					content: [{ type: "text", text: `Remote file ${result.path} is binary [${result.mime_type ?? "application/octet-stream"}].` }],
					details,
				};
			}
			return { content: [{ type: "text", text: result.content ?? "" }], details };
		},
	});

	pi.registerTool({
		name: "bash",
		label: "bash",
		description: "Execute a Bash script on the active local or remote target. Never silently uses CMD or PowerShell.",
		promptSnippet: "Execute Bash commands on the active execution target",
		promptGuidelines: ["Treat bash as the shell reported for the active target; never silently mix CMD or PowerShell syntax."],
		parameters: bashSchema,
		async execute(id, params, signal, onUpdate, ctx) {
			if (!state.enabled) return localBash.execute(id, params, signal, onUpdate, ctx);
			const snapshot = { ...state };
			try {
				const result = await client(snapshot).call<BashResult>(snapshot.target, "bash", params, signal);
				const output = result.output || "(no output)";
				const details = bashDetails(result);
				onUpdate?.({ content: [{ type: "text", text: output }], details });
				if (result.exit_code !== 0) {
					throw new Error(`${output}\n\nCommand exited with code ${result.exit_code}`);
				}
				return { content: [{ type: "text", text: output }], details };
			} catch (error) {
				throw formatBashError(error);
			}
		},
	});

	pi.registerTool({
		name: "edit",
		label: "edit",
		description: "Apply unique, non-overlapping exact text replacements on the active target.",
		promptSnippet: "Make precise file edits on the active execution target",
		promptGuidelines: [
			"Use edit for precise target-file changes; every edits[].oldText must match exactly once in the original file.",
		],
		parameters: editSchema,
		async execute(id, params, signal, onUpdate, ctx) {
			if (!state.enabled) return localEdit.execute(id, params, signal, onUpdate, ctx);
			const snapshot = { ...state };
			const result = await client(snapshot).call<MutationResult>(snapshot.target, "edit", params, signal);
			return { content: [{ type: "text", text: result.message }], details: result };
		},
	});

	pi.registerTool({
		name: "write",
		label: "write",
		description: "Create or completely overwrite a file on the active target.",
		promptSnippet: "Create or overwrite files on the active execution target",
		promptGuidelines: ["Use write only for new target files or complete rewrites."],
		parameters: writeSchema,
		async execute(id, params, signal, onUpdate, ctx) {
			if (!state.enabled) return localWrite.execute(id, params, signal, onUpdate, ctx);
			const snapshot = { ...state };
			const result = await client(snapshot).call<MutationResult>(snapshot.target, "write", params, signal);
			return { content: [{ type: "text", text: result.message }], details: result };
		},
	});

	pi.on("user_bash", () => {
		if (!state.enabled) return;
		return { operations: createRemoteBashOperations(() => ({ ...state }), client) };
	});

	pi.on("before_agent_start", async (event) => {
		return { systemPrompt: `${event.systemPrompt}\n\n${targetPrompt(state, localCwd)}` };
	});

	pi.on("session_start", async (event, ctx) => {
		registerRemoteAutocomplete(ctx);
		if (event.reason === "startup") {
			state = createInitialState();
			try {
				await clearActiveRemoteConnection();
			} catch (error) {
				ctx.ui.notify(`Unable to reset saved remote connection state: ${errorMessage(error)}`, "warning");
			}
		} else {
			state = await restoreActiveRemoteConnection(ctx, state);
		}
		const flagURL = pi.getFlag("pi-remote-url") as string | undefined;
		const flagToken = pi.getFlag("pi-remote-token") as string | undefined;
		const flagTarget = pi.getFlag("pi-remote-target") as string | undefined;
		const environmentTarget = process.env.PI_REMOTE_TARGET;
		if (flagURL) state.url = normalizeGatewayURL(flagURL);
		if (flagToken) state.token = flagToken;
		if (flagTarget) {
			state.enabled = flagTarget !== "off";
			state.target = flagTarget;
		} else if (flagURL && state.target === "off") {
			state.enabled = true;
			state.target = "remote";
		}
		if (!flagTarget && environmentTarget) {
			state.enabled = environmentTarget !== "off";
			state.target = environmentTarget;
		}
		const notedState = withTargetNote(state);
		if (notedState !== state) pi.appendEntry(STATE_ENTRY, persistedState(notedState));
		state = notedState;
		if (state.enabled) await refreshSelectedTarget(ctx, false);
		updateStatus(ctx, state);
	});

	pi.on("input", async (event, ctx) => {
		if (!state.enabled || event.source !== "interactive") return;
		return transformRemoteFileMentions(event.text, event.images as RemoteImage[] | undefined, { ...state }, client, ctx.signal ?? new AbortController().signal);
	});

	pi.on("session_tree", async (_event, ctx) => {
		state = restoreState(ctx, state);
		if (state.enabled) await refreshSelectedTarget(ctx, false);
		const notedState = withTargetNote(state);
		if (notedState !== state) pi.appendEntry(STATE_ENTRY, persistedState(notedState));
		state = notedState;
		updateStatus(ctx, state);
	});

	pi.registerCommand("remote", {
		description: "Show or switch the execution target (connect/list/remove/status/off/Worker ID)",
		getArgumentCompletions: (prefix) => {
			const values = ["connect", "list", "remove", "status", "off", "refresh", "note"];
			const matches = values.filter((value) => value.startsWith(prefix));
			return matches.length ? matches.map((value) => ({ value, label: value })) : null;
		},
		handler: async (args, ctx) => {
			await ctx.waitForIdle();
			const parts = args.trim().split(/\s+/).filter(Boolean);
			if (parts[0] === "status") {
				ctx.ui.notify(targetSummary(state, localCwd), "info");
				return;
			}
			if (parts[0] === "list") {
				await listRemoteConnections(ctx);
				return;
			}
			if (parts[0] === "remove") {
				await removeRemoteConnection(ctx);
				return;
			}
			if (parts[0] === "connect") {
				if (!parts[1]) {
					ctx.ui.notify("Usage: /remote connect <gateway> <token> [note...] or /remote connect <gateway> <token> --worker <worker-id> [note...]", "error");
					return;
				}
				const connection = parseConnectArguments(parts.slice(2));
				if (!connection) {
					ctx.ui.notify("Usage: /remote connect <gateway> <token> [note...] or /remote connect <gateway> <token> --worker <worker-id> [note...]", "error");
					return;
				}
				state.url = normalizeGatewayURL(parts[1]);
				state.token = connection.token;
				await switchTarget(connection.target, ctx, undefined, connection.note);
				return;
			}
			if (parts[0] === "note") {
				await setTargetNote(ctx, parts.slice(1).join(" "));
				return;
			}
			if (parts[0] === "off") {
				await switchTarget("off", ctx);
				return;
			}
			if (parts[0] === "refresh") {
				if (state.enabled) await refreshSelectedTarget(ctx, true);
				else ctx.ui.notify("Pi remote is already off", "info");
				return;
			}
			if (parts[0]) {
				await switchTarget(parts[0], ctx);
				return;
			}
			showRemoteCommandHelp(ctx);
		},
	});

	async function listRemoteConnections(ctx: ExtensionCommandContext) {
		try {
			const store = await loadRemoteStore();
			if (store.connections.length === 0) {
				ctx.ui.notify("No saved remote connections", "info");
				return;
			}
			const statuses = await Promise.all(
				store.connections.map((connection) => checkRemoteConnection(connection, ctx.signal)),
			);
			const choices = statuses.map((status) => formatRemoteConnectionStatus(status, store.active));
			const selected = await ctx.ui.select("Saved remote connections", choices);
			if (!selected) return;
			const selectedStatus = statuses[choices.indexOf(selected)];
			if (!selectedStatus) {
				ctx.ui.notify("The selected remote connection no longer exists", "error");
				return;
			}
			if (!selectedStatus.online) {
				ctx.ui.notify(`Remote connection is offline: ${selectedStatus.error ?? "target unavailable"}`, "error");
				return;
			}
			state = stateForRemoteConnection(state, selectedStatus.connection, false);
			await switchTarget(state.target, ctx, selectedStatus.targets, selectedStatus.connection.note);
		} catch (error) {
			ctx.ui.notify(errorMessage(error), "error");
		}
	}

	async function removeRemoteConnection(ctx: ExtensionCommandContext) {
		try {
			const store = await loadRemoteStore();
			if (store.connections.length === 0) {
				ctx.ui.notify("No saved remote connections", "info");
				return;
			}
			const choices = store.connections.map(formatSavedConnection);
			const selected = await ctx.ui.select("Remove saved remote connection", choices);
			if (!selected) return;
			const selectedConnection = store.connections[choices.indexOf(selected)];
			if (!selectedConnection) {
				ctx.ui.notify("The selected remote connection no longer exists", "error");
				return;
			}
			const confirmed = await ctx.ui.confirm(
				"Remove saved remote connection?",
				`${formatSavedConnection(selectedConnection)}\n\nIf it is active, Pi will switch back to its local tools.`,
			);
			if (!confirmed) return;
			const wasActive = store.active === selectedConnection.id;
			const remainingConnections = store.connections.filter(
				(connection) => connection.id !== selectedConnection.id,
			);
			await saveRemoteStore({
				version: 1,
				active: wasActive ? undefined : store.active,
				connections: remainingConnections,
			});
			if (wasActive) await switchTarget("off", ctx);
			ctx.ui.notify(`Removed saved remote connection: ${selectedConnection.note}`, "info");
		} catch (error) {
			ctx.ui.notify(errorMessage(error), "error");
		}
	}

	async function switchTarget(
		target: string,
		ctx: ExtensionCommandContext,
		knownTargets?: TargetsResponse,
		note?: string,
	) {
		if (target === "off") {
			state = { ...state, enabled: false, target: "off", info: undefined, lastError: undefined };
			await persistAndAnnounce(pi, ctx, state, localCwd);
			return;
		}
		try {
			const targets = knownTargets ?? (await client().targets(ctx.signal));
			const info = findTarget(targets, target);
			if (!info) throw new Error(`Target ${target} is not available on ${state.url}`);
			state = { ...state, enabled: true, target, info, lastError: undefined };
			state = withTargetNote(state, note);
			await persistAndAnnounce(pi, ctx, state, localCwd);
		} catch (error) {
			ctx.ui.notify(errorMessage(error), "error");
		}
	}

	async function refreshSelectedTarget(ctx: ExtensionContext, notify: boolean) {
		try {
			const targets = await client().targets(ctx.signal);
			const info = findTarget(targets, state.target);
			if (!info) throw new Error(`Target ${state.target} is offline`);
			state = { ...state, info, lastError: undefined };
			if (notify) {
				pi.appendEntry(STATE_ENTRY, persistedState(state));
				ctx.ui.notify(`Refreshed ${targetSummary(state, localCwd)}`, "info");
			}
		} catch (error) {
			state = { ...state, info: undefined, lastError: errorMessage(error) };
			if (notify) ctx.ui.notify(state.lastError, "error");
		}
		updateStatus(ctx, state);
	}

	function registerRemoteAutocomplete(ctx: ExtensionContext) {
		if (remoteAutocompleteRegistered) return;
		remoteAutocompleteRegistered = true;
		ctx.ui.addAutocompleteProvider((current) => ({
			triggerCharacters: current.triggerCharacters,
			applyCompletion: current.applyCompletion.bind(current),
			shouldTriggerFileCompletion: current.shouldTriggerFileCompletion?.bind(current),
			async getSuggestions(lines, cursorLine, cursorCol, options) {
				const text = (lines[cursorLine] ?? "").slice(0, cursorCol);
				const prefix = remoteAtPrefix(text);
				if (!prefix || !state.enabled) return current.getSuggestions(lines, cursorLine, cursorCol, options);
				const snapshot = { ...state };
				try {
					const entries = await client(snapshot).call<FindResult[]>(snapshot.target, "find", {
						query: remoteAtQuery(prefix),
						max_results: 20,
					}, options.signal);
					const items = entries.map((entry) => remoteCompletionItem(entry, prefix));
					return items.length > 0 ? { items, prefix } : null;
				} catch {
					return null;
				}
			},
		}));
	}

	async function setTargetNote(ctx: ExtensionCommandContext, requestedNote: string) {
		if (!state.enabled) {
			ctx.ui.notify("Select a remote target before setting a note", "error");
			return;
		}
		let note = requestedNote.trim();
		if (!note) {
			const enteredNote = await ctx.ui.input("Remote target note", targetNote(state));
			if (enteredNote === undefined) return;
			note = enteredNote.trim();
		}
		state = withTargetNote({ ...state, targetNotes: { ...state.targetNotes, [state.target]: note } });
		await persistAndAnnounce(pi, ctx, state, localCwd);
	}
}

function remoteAtPrefix(text: string): string | undefined {
	const match = text.match(/(?:^|\s)(@(?:"[^"\n]*|[^\s]*))$/);
	return match?.[1];
}

interface ConnectArguments {
	target: string;
	token: string;
	note?: string;
}

function parseConnectArguments(args: string[]): ConnectArguments | undefined {
	const token = args[0];
	if (!token) return undefined;
	if (args[1] !== "--worker") {
		return { target: "remote", token, note: args.slice(1).join(" ") || undefined };
	}
	if (!args[2]) return undefined;
	return { target: args[2], token, note: args.slice(3).join(" ") || undefined };
}

function remoteAtQuery(prefix: string): string {
	return prefix.startsWith('@"') ? prefix.slice(2) : prefix.slice(1);
}

function remoteCompletionItem(entry: FindResult, prefix: string) {
	const path = entry.is_directory ? `${entry.path}/` : entry.path;
	const quoted = prefix.startsWith('@"') || path.includes(" ");
	const value = quoted ? `@"${path}"` : `@${path}`;
	const label = entry.path.split("/").pop() ?? entry.path;
	return { value, label: entry.is_directory ? `${label}/` : label, description: entry.path };
}

async function transformRemoteFileMentions(
	text: string,
	images: RemoteImage[] | undefined,
	state: RuntimeState,
	createClient: (state?: RuntimeState) => RPCClient,
	signal: AbortSignal,
) {
	const mentionPattern = /(^|\s)@(?:"([^"]+)"|([^\s]+))/g;
	const mentions = [...text.matchAll(mentionPattern)];
	if (mentions.length === 0) return;
	let transformed = text;
	const attachments = images ? [...images] : [];
	for (const mention of mentions.reverse()) {
		const path = mention[2] ?? mention[3];
		if (!path) continue;
		try {
			const result = await createClient(state).call<ReadResult>(state.target, "read", { path }, signal);
			const replacement = remoteFileContent(result, attachments);
			const start = (mention.index ?? 0) + (mention[1]?.length ?? 0);
			transformed = `${transformed.slice(0, start)}${replacement}${transformed.slice(start + mention[0].length - (mention[1]?.length ?? 0))}`;
		} catch {
			// Leave non-file @mentions untouched. They must never be read from Pi's host.
		}
	}
	return { action: "transform" as const, text: transformed, images: attachments };
}

function remoteFileContent(result: ReadResult, images: RemoteImage[]): string {
	if (result.encoding === "base64" && result.content_base64 && result.mime_type?.startsWith("image/")) {
		images.push({ type: "image", data: result.content_base64, mimeType: result.mime_type });
		return `<file name="${result.path}"></file>`;
	}
	return `<file name="${result.path}">\n${result.content ?? ""}\n</file>`;
}

function createRemoteBashOperations(
	getState: () => RuntimeState,
	createClient: (state?: RuntimeState) => RPCClient,
): BashOperations {
	return {
		async exec(command, _cwd, { onData, signal, timeout }) {
			const snapshot = getState();
			try {
				const result = await createClient(snapshot).call<BashResult>(
					snapshot.target,
					"bash",
					{ command, timeout },
					signal,
				);
				if (result.output) onData(Buffer.from(result.output, "utf8"));
				return { exitCode: result.exit_code };
			} catch (error) {
				if (error instanceof RemoteRPCError) {
					const details = error.details as BashResult | undefined;
					if (details?.output) onData(Buffer.from(details.output, "utf8"));
					if (error.code === "timeout") throw new Error(`timeout:${timeout}`);
					if (error.code === "aborted") throw new Error("aborted");
				}
				if (signal?.aborted) throw new Error("aborted");
				throw error;
			}
		},
	};
}

function normalizeGatewayURL(value: string): string {
	const parsed = new URL(value);
	if (parsed.protocol === "ws:") parsed.protocol = "http:";
	if (parsed.protocol !== "http:") {
		throw new Error("Gateway URL must use HTTP or WS");
	}
	parsed.pathname = parsed.pathname.replace(/\/v1\/workers\/connect\/?$/, "").replace(/\/$/, "");
	parsed.search = "";
	parsed.hash = "";
	return parsed.toString().replace(/\/$/, "");
}

function createInitialState(): RuntimeState {
	return {
		enabled: false,
		url: normalizeGatewayURL(process.env.PI_REMOTE_URL ?? "http://127.0.0.1:8787"),
		target: "off",
		token: "",
		targetNotes: {},
	};
}

async function restoreActiveRemoteConnection(ctx: ExtensionContext, fallback: RuntimeState): Promise<RuntimeState> {
	try {
		const store = await loadRemoteStore();
		const connection = findActiveRemoteConnection(store);
		return connection ? stateForRemoteConnection(fallback, connection, false) : fallback;
	} catch (error) {
		ctx.ui.notify(`Unable to restore active remote connection: ${errorMessage(error)}`, "warning");
		return fallback;
	}
}

function stateForRemoteConnection(
	state: RuntimeState,
	connection: RemoteConnection,
	preferCurrentToken = true,
): RuntimeState {
	const target = connection.target;
	return withTargetNote(
		{
			...state,
			enabled: true,
			url: normalizeGatewayURL(connection.url),
			target,
			token: preferCurrentToken ? state.token || connection.token : connection.token,
			info: undefined,
			lastError: undefined,
		},
		connection.note,
	);
}

async function loadRemoteStore(): Promise<RemoteStore> {
	let contents: string;
	try {
		contents = await readFile(REMOTE_STORE_PATH, "utf8");
	} catch (error) {
		if (hasErrorCode(error, "ENOENT")) return emptyRemoteStore();
		throw new Error(`Unable to read ${REMOTE_STORE_PATH}: ${errorMessage(error)}`);
	}
	try {
		return parseRemoteStore(JSON.parse(contents) as unknown);
	} catch (error) {
		throw new Error(`Invalid ${REMOTE_STORE_PATH}: ${errorMessage(error)}`);
	}
}

async function saveRemoteStore(store: RemoteStore): Promise<void> {
	await mkdir(dirname(REMOTE_STORE_PATH), { recursive: true, mode: 0o700 });
	const temporaryPath = `${REMOTE_STORE_PATH}.${process.pid}.${randomUUID()}.tmp`;
	try {
		await writeFile(temporaryPath, `${JSON.stringify(store, null, 2)}\n`, {
			encoding: "utf8",
			mode: 0o600,
		});
		await rename(temporaryPath, REMOTE_STORE_PATH);
	} finally {
		await unlink(temporaryPath).catch(() => undefined);
	}
}

async function saveActiveRemoteConnection(state: RuntimeState): Promise<void> {
	const store = await loadRemoteStore();
	const target = state.target;
	const note = targetNote(state);
	const index = store.connections.findIndex(
		(connection) => connection.url === state.url && connection.target === target,
	);
	const connection: RemoteConnection = index >= 0
		? { ...store.connections[index], url: state.url, target, note, token: state.token }
		: { id: randomConnectionID(), url: state.url, target, note, token: state.token };
	const connections = [...store.connections];
	if (index >= 0) connections[index] = connection;
	else connections.push(connection);
	await saveRemoteStore({ version: 1, active: connection.id, connections });
}

async function clearActiveRemoteConnection(): Promise<void> {
	const store = await loadRemoteStore();
	if (!store.active) return;
	await saveRemoteStore({ ...store, active: undefined });
}

function parseRemoteStore(value: unknown): RemoteStore {
	if (!isRecord(value) || value.version !== 1 || !Array.isArray(value.connections)) {
		throw new Error("expected version 1 with a connections array");
	}
	if (value.active !== undefined && typeof value.active !== "string") {
		throw new Error("active must be a string");
	}
	return {
		version: 1,
		active: value.active as string | undefined,
		connections: value.connections.map((connection, index) => parseRemoteConnection(connection, index)),
	};
}

function parseRemoteConnection(value: unknown, index: number): RemoteConnection {
	if (
		!isRecord(value) ||
		typeof value.id !== "string" ||
		typeof value.url !== "string" ||
		typeof value.target !== "string" ||
		typeof value.note !== "string" ||
		typeof value.token !== "string" ||
		!value.id ||
		!value.url ||
		!value.target ||
		value.target === "off"
	) {
		throw new Error(`connection ${index} is invalid`);
	}
	return {
		id: value.id,
		url: normalizeGatewayURL(value.url),
		target: value.target,
		note: value.note.trim(),
		token: value.token,
	};
}

function emptyRemoteStore(): RemoteStore {
	return { version: 1, connections: [] };
}

function findActiveRemoteConnection(store: RemoteStore): RemoteConnection | undefined {
	if (!store.active) return undefined;
	return store.connections.find((connection) => connection.id === store.active);
}

function formatSavedConnection(connection: RemoteConnection): string {
	const note = connection.note || "(unnamed remote)";
	return `${note} — ${savedTargetLabel(connection.target)} @ ${connection.url} [${connection.id}]`;
}

async function checkRemoteConnection(
	connection: RemoteConnection,
	signal?: AbortSignal,
): Promise<RemoteConnectionStatus> {
	try {
		const targets = await new RPCClient(connection.url, connection.token).targets(signal);
		if (!findTarget(targets, connection.target)) {
			return {
				connection,
				targets,
				online: false,
				error: `target ${connection.target} is not online`,
			};
		}
		return { connection, targets, online: true };
	} catch (error) {
		return { connection, online: false, error: errorMessage(error) };
	}
}

function formatRemoteConnectionStatus(status: RemoteConnectionStatus, activeID?: string): string {
	const marker = status.connection.id === activeID ? "*" : " ";
	const availability = status.online ? "online" : `offline: ${status.error ?? "unavailable"}`;
	return `${marker} ${formatSavedConnection(status.connection)} — ${availability}`;
}

function showRemoteCommandHelp(ctx: ExtensionCommandContext): void {
	ctx.ui.notify(
		[
			"Pi remote subcommands:",
			"/remote connect <gateway> <token> [note...]",
			"/remote connect <gateway> <token> --worker <worker-id> [note...]",
			"/remote list       list saved connections and their online status",
			"/remote remove     remove a saved connection",
			"/remote status     show the current target",
			"/remote off        use Pi's local tools",
			"/remote refresh    refresh the current target",
			"/remote note [text] change the current note",
		].join("\n"),
		"info",
	);
}

function savedTargetLabel(target: string): string {
	return target === "remote" ? "Gateway machine" : `Worker ${target}`;
}

function randomConnectionID(): string {
	return `connection-${randomUUID().slice(0, 8)}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null;
}

function hasErrorCode(error: unknown, code: string): boolean {
	return isRecord(error) && error.code === code;
}

function restoreState(ctx: ExtensionContext, fallback: RuntimeState): RuntimeState {
	let restored: PersistedState | undefined;
	for (const entry of ctx.sessionManager.getBranch()) {
		if (entry.type === "custom" && entry.customType === STATE_ENTRY) {
			restored = entry.data as PersistedState;
		}
	}
	if (!restored) return fallback;
	return {
		...fallback,
		...restored,
		token: fallback.token,
		targetNotes: { ...fallback.targetNotes, ...restored.targetNotes },
	};
}

function persistedState(state: RuntimeState): PersistedState {
	return {
		enabled: state.enabled,
		url: state.url,
		target: state.target,
		targetNotes: state.targetNotes,
		info: state.info,
		lastError: state.lastError,
	};
}

function findTarget(targets: TargetsResponse, target: string): TargetInfo | undefined {
	if (target === "remote") return targets.remote ? targets.remote_info : undefined;
	return targets.workers.find((worker) => worker.worker_id === target);
}

function readDetails(result: ReadResult): Omit<ReadResult, "content" | "content_base64"> {
	const { content: _content, content_base64: _contentBase64, ...details } = result;
	return details;
}

function bashDetails(result: BashResult): Omit<BashResult, "output"> {
	const { output: _output, ...details } = result;
	return details;
}

async function persistAndAnnounce(pi: ExtensionAPI, ctx: ExtensionContext, state: RuntimeState, localCwd: string) {
	pi.appendEntry(STATE_ENTRY, persistedState(state));
	try {
		await persistRemoteConnection(state);
	} catch (error) {
		ctx.ui.notify(`Unable to persist remote connection: ${errorMessage(error)}`, "warning");
	}
	updateStatus(ctx, state);
	const content = targetPrompt(state, localCwd);
	ctx.ui.notify(targetSummary(state, localCwd), "info");
	// The message participates in model context but does not trigger an unsolicited turn.
	pi.sendMessage(
		{ customType: "pi-remote-target", content, display: true, details: persistedState(state) },
		{ deliverAs: "nextTurn" },
	);
}

async function persistRemoteConnection(state: RuntimeState): Promise<void> {
	if (state.enabled) await saveActiveRemoteConnection(state);
	else await clearActiveRemoteConnection();
}

function updateStatus(ctx: ExtensionContext, state: RuntimeState) {
	if (!state.enabled) {
		ctx.ui.setStatus("pi-remote", ctx.ui.theme.fg("muted", "target: Pi local"));
		return;
	}
	const suffix = state.lastError ? " offline" : "";
	ctx.ui.setStatus("pi-remote", ctx.ui.theme.fg(state.lastError ? "error" : "accent", `target: ${targetNote(state)}${suffix}`));
}

function targetSummary(state: RuntimeState, localCwd: string): string {
	if (!state.enabled) return `Execution target: Pi local machine (${localCwd})`;
	const direction = state.target === "remote" ? "forward" : "reverse";
	const route = state.target === "remote" ? "Gateway machine" : `Worker ${state.target}`;
	const info = state.info;
	if (!info) return `Execution target: ${targetNote(state)} (${route}, ${direction}, offline: ${state.lastError ?? "unknown"})`;
	return `Execution target: ${targetNote(state)} (${route}, ${direction}) ${info.hostname} ${info.os}/${info.arch}, root=${info.root}, shell=${info.shell_profile}`;
}

function targetPrompt(state: RuntimeState, localCwd: string): string {
	if (!state.enabled) {
		return [
			"PI REMOTE EXECUTION TARGET:",
			`- Mode: local Pi process`,
			`- Working directory: ${localCwd}`,
			"- read, bash, edit, and write operate on this local machine.",
		].join("\n");
	}
	const direction = state.target === "remote" ? "forward (Pi connects to the target Gateway)" : "reverse (Worker connects out to the Gateway)";
	const info = state.info;
	return [
		"PI REMOTE EXECUTION TARGET:",
		`- User note: ${targetNote(state)}`,
		`- Route: ${state.target === "remote" ? "Gateway machine" : `Worker ${state.target}`}`,
		`- Connection: ${direction}`,
		`- Gateway: ${state.url}`,
		`- Status: ${state.lastError ? `offline: ${state.lastError}` : "online"}`,
		`- Host: ${info?.hostname ?? "unknown"}`,
		`- OS/architecture: ${info ? `${info.os}/${info.arch}` : "unknown"}`,
		`- Working directory: ${info?.root ?? "unknown"}`,
		`- Shell profile: ${info?.shell_profile ?? "unknown"}`,
		"- The read, bash, edit, and write tools are intercepted and operate on this target, not on the Pi host.",
		"- Resolve relative paths against the target working directory. Do not rewrite them using the Pi host working directory.",
		"- Treat the reported shell profile as authoritative: busybox-sh means BusyBox ash/POSIX syntax, while explicit-bash or system-bash means Bash. Never silently mix CMD or PowerShell syntax.",
		"- If the target is offline, stop and report it; never fall back to local execution.",
	].join("\n");
}

function formatBashError(error: unknown): Error {
	if (error instanceof RemoteRPCError) {
		const details = error.details as BashResult | undefined;
		const prefix = details?.output ? `${details.output}\n\n` : "";
		if (error.code === "timeout") return new Error(`${prefix}${error.message}`);
		if (error.code === "aborted") return new Error(`${prefix}Command aborted`);
		return new Error(`${prefix}${error.message}`);
	}
	return error instanceof Error ? error : new Error(String(error));
}

function errorMessage(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}

function withTargetNote(state: RuntimeState, requestedNote?: string): RuntimeState {
	if (!state.enabled || !state.target || state.target === "off") return state;
	const existingNote = state.targetNotes[state.target]?.trim();
	const note = requestedNote?.trim() || existingNote || randomTargetNote();
	if (note === existingNote) return state;
	return {
		...state,
		targetNotes: { ...state.targetNotes, [state.target]: note },
	};
}

function randomTargetNote(): string {
	return `remote-${randomUUID().slice(0, 8)}`;
}

function targetNote(state: RuntimeState): string {
	if (!state.enabled) return "Pi local machine";
	return state.targetNotes[state.target] ?? (state.target === "remote" ? "Gateway machine" : state.target);
}
