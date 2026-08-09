import { describe, expect, test } from "bun:test";
import { modelCapabilityConfigFor } from "../src/lib/model-capabilities";
import { asyncVideoGenerationMaxReferenceImages, asyncVideoGenerationModelDuration, normalizeMiniMaxH3Duration, normalizeMiniMaxH3Ratio, normalizeMiniMaxH3Resolution, normalizeVideoSize } from "../src/lib/video-generation-options";

describe("normalizeVideoSize", () => {
    test("将共享配置中的方形比例转换为方形尺寸", () => {
        expect(normalizeVideoSize("1:1")).toBe("1024x1024");
    });

    test("保留明确尺寸和现有横竖屏兼容规则", () => {
        expect(normalizeVideoSize("768x448")).toBe("768x448");
        expect(normalizeVideoSize("9:16")).toBe("720x1280");
        expect(normalizeVideoSize("16:9")).toBe("1280x720");
        expect(normalizeVideoSize("auto")).toBe("auto");
    });
});

describe("asyncVideoGenerationModelDuration", () => {
    test("从异步视频模型 ID 读取固定时长", () => {
        expect(asyncVideoGenerationModelDuration("sora-2-12s")).toBe(12);
        expect(asyncVideoGenerationModelDuration("seedance-2.0-mini")).toBe(8);
        expect(asyncVideoGenerationModelDuration("seedance-2.0-fast-15s")).toBe(15);
    });

    test("未知模型不伪造时长", () => {
        expect(asyncVideoGenerationModelDuration("new-video-model")).toBeUndefined();
    });
});

describe("asyncVideoGenerationMaxReferenceImages", () => {
    test("按模型限制参考图数量", () => {
        expect(asyncVideoGenerationMaxReferenceImages("sora-2-8s")).toBe(1);
        expect(asyncVideoGenerationMaxReferenceImages("seedance-1.5-pro-12s")).toBe(1);
        expect(asyncVideoGenerationMaxReferenceImages("seedance-2.0-fast-10s")).toBe(12);
    });

    test("未知模型保守使用 12 张总上限", () => {
        expect(asyncVideoGenerationMaxReferenceImages("new-video-model")).toBe(12);
    });
});

describe("MiniMax H3 options", () => {
    test("规范化 H3 的分辨率和时长", () => {
        expect(normalizeMiniMaxH3Resolution("720")).toBe("768P");
        expect(normalizeMiniMaxH3Resolution("2K")).toBe("2K");
        expect(normalizeMiniMaxH3Duration("1")).toBe(4);
        expect(normalizeMiniMaxH3Duration("20")).toBe(15);
    });

    test("文本模式要求固定比例，参考模式允许自适应", () => {
        expect(normalizeMiniMaxH3Ratio("auto", false)).toBe("16:9");
        expect(normalizeMiniMaxH3Ratio("auto", true)).toBe("adaptive");
        expect(normalizeMiniMaxH3Ratio("1280x720", false)).toBe("16:9");
    });

    test("旧用户渠道从渠道协议恢复 H3 素材限制", () => {
        const profile = modelCapabilityConfigFor({ channels: [{ id: "legacy-h3", models: ["MiniMax-H3"], interfaceType: "minimax-h3" }] }, "legacy-h3::MiniMax-H3").video!;

        expect(profile.references.maxImages).toBe(9);
        expect(profile.references.maxVideos).toBe(3);
        expect(profile.references.maxAudios).toBe(3);
    });
});
