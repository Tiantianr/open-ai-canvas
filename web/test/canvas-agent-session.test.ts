import { describe, expect, test } from "bun:test";

import { CINEMATIC_SESSION_LONG_RUNNING_AFTER_MS, cinematicSessionPollIntervalMs, isCinematicSessionLongRunning } from "../src/lib/canvas/cinematic-session-polling";

describe("影视 Agent 会话轮询", () => {
    test("前四分钟保持快速轮询", () => {
        expect(cinematicSessionPollIntervalMs(0)).toBe(2000);
        expect(cinematicSessionPollIntervalMs(CINEMATIC_SESSION_LONG_RUNNING_AFTER_MS - 1)).toBe(2000);
        expect(isCinematicSessionLongRunning(CINEMATIC_SESSION_LONG_RUNNING_AFTER_MS - 1)).toBe(false);
    });

    test("长任务继续等待并逐级降低轮询频率", () => {
        expect(isCinematicSessionLongRunning(CINEMATIC_SESSION_LONG_RUNNING_AFTER_MS)).toBe(true);
        expect(cinematicSessionPollIntervalMs(CINEMATIC_SESSION_LONG_RUNNING_AFTER_MS)).toBe(5000);
        expect(cinematicSessionPollIntervalMs(15 * 60 * 1000)).toBe(15000);
    });
});
