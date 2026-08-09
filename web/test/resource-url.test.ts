import { describe, expect, test } from "bun:test";

import { resourceIdFromUrl } from "../src/services/api/resources";

describe("resourceIdFromUrl", () => {
    test("从 Canvas 文件地址恢复资源 ID", () => {
        expect(resourceIdFromUrl("/api/resources/2575b91f9f4ca4987063c08d7dce682e/file?direct=1")).toBe("2575b91f9f4ca4987063c08d7dce682e");
        expect(resourceIdFromUrl("/api/resources/resource%20id/file")).toBe("resource id");
    });

    test("不把外部或含路径的地址识别为 Canvas 资源", () => {
        expect(resourceIdFromUrl("https://example.com/api/resources/external/file")).toBe("");
        expect(resourceIdFromUrl("/api/resources/nested%2Fid/file")).toBe("");
    });
});
