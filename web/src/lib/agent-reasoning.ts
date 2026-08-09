export const AGENT_REASONING_EFFORT_OPTIONS = [
    { value: "auto", label: "自动" },
    { value: "minimal", label: "最少" },
    { value: "low", label: "低" },
    { value: "medium", label: "中" },
    { value: "high", label: "高" },
    { value: "xhigh", label: "极高" },
    { value: "max", label: "最大" },
] as const;

export type AgentReasoningEffort = (typeof AGENT_REASONING_EFFORT_OPTIONS)[number]["value"];
type ExplicitAgentReasoningEffort = Exclude<AgentReasoningEffort, "auto">;

export function normalizeAgentReasoningEffort(value: unknown): AgentReasoningEffort {
    const effort = typeof value === "string" ? value.trim().toLowerCase() : "";
    return AGENT_REASONING_EFFORT_OPTIONS.some((option) => option.value === effort) ? (effort as AgentReasoningEffort) : "auto";
}

export function explicitAgentReasoningEffort(value: unknown): ExplicitAgentReasoningEffort | undefined {
    const effort = normalizeAgentReasoningEffort(value);
    return effort === "auto" ? undefined : effort;
}

export function withOpenAIResponsesReasoning<T extends Record<string, unknown>>(body: T, value: unknown): T & { reasoning?: { effort: ExplicitAgentReasoningEffort } } {
    const effort = explicitAgentReasoningEffort(value);
    return effort ? { ...body, reasoning: { effort } } : body;
}

export function withOpenAIChatReasoning<T extends Record<string, unknown>>(body: T, value: unknown): T & { reasoning_effort?: ExplicitAgentReasoningEffort } {
    const effort = explicitAgentReasoningEffort(value);
    return effort ? { ...body, reasoning_effort: effort } : body;
}
