import { requestAudioGeneration } from "@/services/api/audio";
import { fetchImageModels, requestGeneration, requestImageQuestion } from "@/services/api/image";
import { createVideoGenerationTask, testComfyUIH3Connection, testMiniMaxH3Connection } from "@/services/api/video";
import { defaultConfig, encodeChannelModel, modelOptionName, type ModelCapability, type ModelChannel } from "@/stores/use-config-store";
import type { ModelProtocol } from "@/lib/model-protocols";

export async function testChannelModelConnection(channel: ModelChannel, model: string, capability: ModelCapability, protocol: ModelProtocol) {
    if (!channel.baseUrl.trim()) throw new Error("请先填写 Base URL");
    if (!channel.apiKey.trim() && protocol !== "comfyui-h3") throw new Error("请先填写 API Key");
    const selectedModel = encodeChannelModel(channel.id, model);
    const modelCost = channel.modelCosts?.find((item) => item.model === model);
    const testProtocol = channel.apiFormat === "gemini" && !modelCost?.protocol ? undefined : protocol;
    const testChannel: ModelChannel = {
        ...channel,
        models: channel.models.includes(model) ? channel.models : [...channel.models, model],
        modelCosts: [
            {
                model,
                displayName: modelCost?.displayName,
                capability,
                protocol: testProtocol,
                billingMode: modelCost?.billingMode || "fixed_request",
                unitPriceMicrocredits: modelCost?.unitPriceMicrocredits || 0,
                capabilityConfig: modelCost?.capabilityConfig,
            },
            ...(channel.modelCosts || []).filter((item) => item.model !== model),
        ],
    };
    const config = {
        ...defaultConfig,
        channelMode: "local" as const,
        baseUrl: channel.baseUrl,
        apiKey: channel.apiKey,
        apiFormat: channel.apiFormat,
        channels: [testChannel],
        model: selectedModel,
        imageModel: selectedModel,
        videoModel: selectedModel,
        textModel: selectedModel,
        audioModel: selectedModel,
        models: [selectedModel],
        imageModels: capability === "image" ? [selectedModel] : [],
        videoModels: capability === "video" ? [selectedModel] : [],
        textModels: capability === "text" ? [selectedModel] : [],
        audioModels: capability === "audio" ? [selectedModel] : [],
        count: "1",
        size: capability === "image" ? "1024x1024" : "16:9",
        videoSeconds: "6",
        vquality: "720",
        videoGenerateAudio: "false",
    };

    switch (capability) {
        case "text":
            await requestImageQuestion(config, [{ role: "user", content: "Reply with OK." }], () => undefined);
            return "文本响应正常";
        case "image":
            await requestGeneration(config, "A simple gray circle on a white background.");
            return "图片生成正常";
        case "audio":
            await requestAudioGeneration(config, "Model test.");
            return "音频生成正常";
        case "video": {
            if (protocol === "async-video-generations") {
                const models = await fetchImageModels({ baseUrl: channel.baseUrl, apiKey: channel.apiKey, apiFormat: channel.apiFormat });
                if (!models.some((item) => modelOptionName(item) === modelOptionName(model))) throw new Error(`上游模型目录不包含 ${modelOptionName(model)}`);
                return "模型目录验证正常，未创建视频任务";
            }
            if (protocol === "minimax-h3") {
                if (modelOptionName(model).toLowerCase() !== "minimax-h3") throw new Error("MiniMax H3 协议仅支持模型 MiniMax-H3");
                await testMiniMaxH3Connection(config);
                return "MiniMax H3 鉴权验证正常，未创建视频任务";
            }
            if (protocol === "comfyui-h3") {
                const modelName = modelOptionName(model).toLowerCase();
                if (modelName !== "minimax-h3" && modelName !== "minimax-h3-r2v" && modelName !== "minimax-h3-r2v-pdd-4step") throw new Error("ComfyUI H3 协议仅支持 MiniMax-H3-R2V 或 MiniMax-H3-R2V-PDD-4Step");
                await testComfyUIH3Connection(config);
                return "ComfyUI H3 节点验证正常，未创建视频任务";
            }
            const task = await createVideoGenerationTask(config, "A static gray circle on a white background.");
            return `视频任务已创建（${task.id}）`;
        }
    }
}
