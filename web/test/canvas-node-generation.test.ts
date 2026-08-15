import { describe, expect, test } from "bun:test";
import { buildNodeGenerationContext } from "../src/components/canvas/canvas-node-generation";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "../src/types/canvas";

describe("buildNodeGenerationContext", () => {
    test("视频 promptOnly 展开显式文本引用但忽略其他上游文本", () => {
        const nodes: CanvasNodeData[] = [
            canvasNode("shot-text", CanvasNodeType.Text, { content: "镜头的真实正文" }),
            canvasNode("unused-text", CanvasNodeType.Text, { content: "未被引用的正文" }),
            canvasNode("video-config", CanvasNodeType.Config, { composerContent: "任务说明：@[node:shot-text]" }),
        ];
        const connections: CanvasConnection[] = [
            { id: "shot-to-config", fromNodeId: "shot-text", toNodeId: "video-config" },
            { id: "unused-to-config", fromNodeId: "unused-text", toNodeId: "video-config" },
        ];

        const context = buildNodeGenerationContext("video-config", nodes, connections, "任务说明：@[node:shot-text]", [], true);

        expect(context.prompt).toContain("任务说明：【文本1】");
        expect(context.prompt).toContain("【文本1】\n镜头的真实正文");
        expect(context.prompt).not.toContain("未被引用的正文");
        expect(context.textCount).toBe(1);
    });
});

function canvasNode(id: string, type: CanvasNodeType, metadata: CanvasNodeData["metadata"]): CanvasNodeData {
    return { id, type, title: id, position: { x: 0, y: 0 }, width: 320, height: 180, metadata };
}
