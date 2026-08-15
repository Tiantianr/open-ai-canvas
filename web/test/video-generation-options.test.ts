import { describe, expect, test } from "bun:test";
import { buildGenerationConfig, supportsVideoReferenceAudio } from "../src/lib/canvas/canvas-project-generation";
import { defaultModelCapabilityConfig, modelCapabilityConfigFor } from "../src/lib/model-capabilities";
import { asyncVideoGenerationMaxReferenceImages, asyncVideoGenerationModelDuration, normalizeMiniMaxH3Duration, normalizeMiniMaxH3Ratio, normalizeMiniMaxH3Resolution, normalizeVideoSize } from "../src/lib/video-generation-options";
import { defaultConfig, type AiConfig } from "../src/stores/use-config-store";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";

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

test("视频提交按所选模型能力修正旧节点分辨率", () => {
    const modelName = "MiniMax-H3-R2V-PDD-4Step";
    const selectedModel = `local-h3::${modelName}`;
    const capability = defaultModelCapabilityConfig("comfyui-h3", modelName);
    capability.video = {
        ...capability.video!,
        duration: { selection: "enum", values: [5, 7, 10, 15], default: 5 },
        ratios: ["16:9", "9:16"],
        defaultRatio: "16:9",
        resolutions: ["480p"],
        defaultResolution: "480p",
    };
    const config: AiConfig = {
        ...defaultConfig,
        channels: [
            {
                id: "local-h3",
                name: "Local H3",
                baseUrl: "http://localhost",
                apiKey: "",
                apiFormat: "openai",
                interfaceType: "comfyui-h3",
                models: [modelName],
                modelCosts: [{ model: modelName, capability: "video", protocol: "comfyui-h3", billingMode: "fixed_request", unitPriceMicrocredits: 0, capabilityConfig: capability }],
            },
        ],
        models: [selectedModel],
        videoModels: [selectedModel],
        model: selectedModel,
        videoModel: selectedModel,
    };
    const node: CanvasNodeData = {
        id: "pdd-video",
        type: CanvasNodeType.Video,
        title: "PDD video",
        position: { x: 0, y: 0 },
        width: 640,
        height: 360,
        metadata: { model: selectedModel, seconds: "10", size: "16:9", vquality: "720" },
    };

    expect(buildGenerationConfig(config, node, "video")).toMatchObject({ model: selectedModel, videoSeconds: "10", size: "16:9", vquality: "480" });
    expect(capability.video?.references).toMatchObject({ maxAudios: 3, maxAudioBytes: 15 * 1024 * 1024, maxAudioDurationSeconds: 15 });
    expect(supportsVideoReferenceAudio(config)).toBe(true);
});
