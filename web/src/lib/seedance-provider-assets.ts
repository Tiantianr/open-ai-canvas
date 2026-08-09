const SEEDANCE_ASSET_URI_PREFIX = "asset://";
const SEEDANCE_ASSET_ID_PATTERN = /^asset-[A-Za-z0-9][A-Za-z0-9._-]{2,159}$/;

type SeedanceAssetBinding = {
    seedanceAssetId?: string;
    seedanceAssetStorageKey?: string;
    storageKey?: string;
};

type SeedanceAssetReference = {
    seedanceAssetId?: string;
    dataUrl: string;
    url?: string;
    storageKey?: string;
};

export function normalizeSeedanceAssetId(value: unknown) {
    if (typeof value !== "string") return "";
    const trimmed = value.trim();
    const id = trimmed.toLowerCase().startsWith(SEEDANCE_ASSET_URI_PREFIX) ? trimmed.slice(SEEDANCE_ASSET_URI_PREFIX.length).trim() : trimmed;
    return SEEDANCE_ASSET_ID_PATTERN.test(id) ? id : "";
}

export function seedanceAssetUri(value: unknown) {
    const id = normalizeSeedanceAssetId(value);
    return id ? `${SEEDANCE_ASSET_URI_PREFIX}${id}` : "";
}

export function currentSeedanceAssetId(binding: SeedanceAssetBinding) {
    const id = normalizeSeedanceAssetId(binding.seedanceAssetId);
    if (!id) return "";
    const boundStorageKey = binding.seedanceAssetStorageKey?.trim();
    if (boundStorageKey && boundStorageKey !== binding.storageKey?.trim()) return "";
    return id;
}

export function withSeedanceAssetReference<T extends SeedanceAssetReference>(reference: T, enabled: boolean): T {
    if (!enabled) return reference;
    const url = seedanceAssetUri(reference.seedanceAssetId);
    if (!url) return reference;
    return { ...reference, url, dataUrl: "", storageKey: undefined };
}
