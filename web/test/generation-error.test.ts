import { describe, expect, test } from "bun:test";
import { requiresProviderTaskQuery } from "../src/lib/generation-error";

describe("requiresProviderTaskQuery", () => {
    test("阻止已有后台任务的超时失败被直接重试", () => {
        expect(requiresProviderTaskQuery({ taskId: "task-1", errorDetails: "视频生成等待超时，请到任务中心查询原上游任务" })).toBe(true);
        expect(requiresProviderTaskQuery({ taskId: "task-1", errorDetails: "ComfyUI H3 提交状态不确定" })).toBe(true);
    });

    test("普通失败和没有任务 ID 的失败仍可重试", () => {
        expect(requiresProviderTaskQuery({ taskId: "task-1", errorDetails: "输出分辨率不在支持范围内" })).toBe(false);
        expect(requiresProviderTaskQuery({ errorDetails: "视频生成等待超时" })).toBe(false);
    });
});
