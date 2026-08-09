import { describe, expect, test } from "bun:test";

import { explicitAgentReasoningEffort, normalizeAgentReasoningEffort, withOpenAIChatReasoning, withOpenAIResponsesReasoning } from "../src/lib/agent-reasoning";

describe("Agent 思考强度", () => {
    test("非法值和自动档不会发送上游参数", () => {
        expect(normalizeAgentReasoningEffort(" HIGH ")).toBe("high");
        expect(normalizeAgentReasoningEffort("unsupported")).toBe("auto");
        expect(explicitAgentReasoningEffort("auto")).toBeUndefined();
        expect(withOpenAIResponsesReasoning({ model: "test-model" }, "auto")).not.toHaveProperty("reasoning");
        expect(withOpenAIChatReasoning({ model: "test-model" }, "unsupported")).not.toHaveProperty("reasoning_effort");
    });

    test("Responses 使用 reasoning.effort", () => {
        expect(withOpenAIResponsesReasoning({ model: "test-model" }, "xhigh")).toEqual({ model: "test-model", reasoning: { effort: "xhigh" } });
    });

    test("Chat Completions 使用 reasoning_effort", () => {
        expect(withOpenAIChatReasoning({ model: "test-model" }, "max")).toEqual({ model: "test-model", reasoning_effort: "max" });
    });
});
