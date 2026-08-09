import { describe, expect, test } from "bun:test";

import { currentSeedanceAssetId, normalizeSeedanceAssetId, seedanceAssetUri, withSeedanceAssetReference } from "../src/lib/seedance-provider-assets";

describe("Seedance provider assets", () => {
    test("normalizes 91Token asset IDs and URIs", () => {
        expect(normalizeSeedanceAssetId(" asset-20260802122505-mb5fl ")).toBe("asset-20260802122505-mb5fl");
        expect(normalizeSeedanceAssetId("asset://asset-20260802122505-mb5fl")).toBe("asset-20260802122505-mb5fl");
        expect(seedanceAssetUri("asset-20260802122505-mb5fl")).toBe("asset://asset-20260802122505-mb5fl");
        expect(normalizeSeedanceAssetId("group-20260802-test")).toBe("");
        expect(normalizeSeedanceAssetId("asset-test/path")).toBe("");
    });

    test("ignores a binding after the image storage key changes", () => {
        expect(currentSeedanceAssetId({ seedanceAssetId: "asset-character-1", seedanceAssetStorageKey: "resource:image-1", storageKey: "resource:image-1" })).toBe("asset-character-1");
        expect(currentSeedanceAssetId({ seedanceAssetId: "asset-character-1", seedanceAssetStorageKey: "resource:image-1", storageKey: "resource:image-2" })).toBe("");
    });

    test("replaces only enabled references and keeps the preview untouched", () => {
        const reference = { id: "image-1", dataUrl: "https://example.com/preview.png", storageKey: "resource:image-1", seedanceAssetId: "asset-character-1" };
        expect(withSeedanceAssetReference(reference, false)).toBe(reference);
        expect(withSeedanceAssetReference(reference, true)).toEqual({ ...reference, dataUrl: "", url: "asset://asset-character-1", storageKey: undefined });
        expect(reference.dataUrl).toBe("https://example.com/preview.png");
    });
});
