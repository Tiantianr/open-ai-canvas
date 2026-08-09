const CINEMATIC_SESSION_POLL_INTERVAL_MS = 2000;
const CINEMATIC_SESSION_LONG_POLL_INTERVAL_MS = 5000;
const CINEMATIC_SESSION_LOW_FREQUENCY_POLL_INTERVAL_MS = 15000;
export const CINEMATIC_SESSION_LONG_RUNNING_AFTER_MS = 4 * 60 * 1000;
const CINEMATIC_SESSION_LOW_FREQUENCY_AFTER_MS = 15 * 60 * 1000;

export function isCinematicSessionLongRunning(elapsedMs: number) {
    return elapsedMs >= CINEMATIC_SESSION_LONG_RUNNING_AFTER_MS;
}

export function cinematicSessionPollIntervalMs(elapsedMs: number) {
    const value = Math.max(0, elapsedMs);
    if (value >= CINEMATIC_SESSION_LOW_FREQUENCY_AFTER_MS) return CINEMATIC_SESSION_LOW_FREQUENCY_POLL_INTERVAL_MS;
    if (isCinematicSessionLongRunning(value)) return CINEMATIC_SESSION_LONG_POLL_INTERVAL_MS;
    return CINEMATIC_SESSION_POLL_INTERVAL_MS;
}
