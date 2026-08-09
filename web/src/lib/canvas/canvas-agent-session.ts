import { agentSessionFailureMessage, createAgentSession, queryAgentSession, type AgentSessionDetail, type CreateSessionInput } from "@/services/api/task-center";
import { cinematicSessionPollIntervalMs, isCinematicSessionLongRunning } from "@/lib/canvas/cinematic-session-polling";

type CinematicSessionWaitOptions = {
    signal?: AbortSignal;
    onLongRunning?: (detail: AgentSessionDetail) => void;
};

type CreateCinematicSessionOptions = CinematicSessionWaitOptions & {
    onCreated?: (detail: AgentSessionDetail) => void;
};

export async function createCinematicAgentSession(input: CreateSessionInput, options: CreateCinematicSessionOptions = {}) {
    const created = await createAgentSession(input);
    throwIfAborted(options.signal);
    options.onCreated?.(created);
    return waitForCinematicAgentSession(created, options);
}

export async function resumeCinematicAgentSession(id: string, options: CinematicSessionWaitOptions = {}) {
    throwIfAborted(options.signal);
    const detail = await queryAgentSession(id);
    return waitForCinematicAgentSession(detail, options);
}

export function cinematicAgentSessionOpsJson(detail: AgentSessionDetail) {
    if (detail.session.status !== "completed") throw new Error("后端影视 Agent 会话尚未完成");
    if (!detail.session.canvasOpsJson) throw new Error("后端影视 Agent 没有返回画布操作");
    return detail.session.canvasOpsJson;
}

export function isAgentSessionPollingAbort(error: unknown) {
    return error instanceof Error && error.name === "AbortError";
}

async function waitForCinematicAgentSession(initialDetail: AgentSessionDetail, options: CinematicSessionWaitOptions) {
    let detail = initialDetail;
    const waitStartedAt = Date.now();
    const createdAt = Date.parse(initialDetail.session.createdAt);
    const sessionStartedAt = Number.isFinite(createdAt) ? Math.min(createdAt, waitStartedAt) : waitStartedAt;
    let longRunningNotified = false;
    // 后端任务超时和租约恢复负责收敛终态；前端不能按固定轮询次数误判仍在运行的会话。
    while (true) {
        throwIfAborted(options.signal);
        if (detail.session.status === "completed") return detail;
        if (detail.session.status === "failed") throw new Error(agentSessionFailureMessage(detail));
        const elapsedMs = Math.max(0, Date.now() - sessionStartedAt);
        if (!longRunningNotified && isCinematicSessionLongRunning(elapsedMs)) {
            longRunningNotified = true;
            options.onLongRunning?.(detail);
        }
        await abortableDelay(cinematicSessionPollIntervalMs(elapsedMs), options.signal);
        detail = await queryAgentSession(initialDetail.session.id);
    }
}

function abortableDelay(ms: number, signal?: AbortSignal) {
    return new Promise<void>((resolve, reject) => {
        throwIfAborted(signal);
        const finish = () => {
            signal?.removeEventListener("abort", abort);
            resolve();
        };
        const abort = () => {
            window.clearTimeout(timer);
            reject(new DOMException("Aborted", "AbortError"));
        };
        const timer = window.setTimeout(finish, ms);
        signal?.addEventListener("abort", abort, { once: true });
    });
}

function throwIfAborted(signal?: AbortSignal) {
    if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
}
