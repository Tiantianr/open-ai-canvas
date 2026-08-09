export const VIDEO_DURATION_OPTIONS = [6, 9, 10, 15] as const;
export const VIDEO_RESOLUTION_OPTIONS = [480, 720, 1080, 2160] as const;
export const VIDEO_DURATION_MIN = 1;
export const MINIMAX_H3_DURATION_OPTIONS = [4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15] as const;
export const MINIMAX_H3_RESOLUTION_OPTIONS = [
    { value: "768P", label: "768P" },
    { value: "2K", label: "2K" },
] as const;
export const MINIMAX_H3_RATIO_OPTIONS = [
    { value: "16:9", label: "横屏" },
    { value: "9:16", label: "竖屏" },
    { value: "1:1", label: "方形" },
    { value: "4:3", label: "标准横屏" },
    { value: "3:4", label: "标准竖屏" },
    { value: "21:9", label: "宽银幕" },
    { value: "adaptive", label: "自适应" },
] as const;

const ASYNC_VIDEO_GENERATION_MODEL_DURATIONS: Record<string, number> = {
    "sora-2-4s": 4,
    "sora-2-8s": 8,
    "sora-2-12s": 12,
    "seedance-1.5-pro-5s": 5,
    "seedance-1.5-pro-10s": 10,
    "seedance-1.5-pro-12s": 12,
    "seedance-2.0": 8,
    "seedance-2.0-mini": 8,
    "seedance-2.0-fast-5s": 5,
    "seedance-2.0-fast-10s": 10,
    "seedance-2.0-fast-15s": 15,
};

const ASYNC_VIDEO_GENERATION_DEFAULT_MAX_REFERENCE_IMAGES = 12;
const ASYNC_VIDEO_GENERATION_MAX_REFERENCE_IMAGES: Record<string, number> = {
    "sora-2-4s": 1,
    "sora-2-8s": 1,
    "sora-2-12s": 1,
    "seedance-1.5-pro-5s": 1,
    "seedance-1.5-pro-10s": 1,
    "seedance-1.5-pro-12s": 1,
    "seedance-2.0": ASYNC_VIDEO_GENERATION_DEFAULT_MAX_REFERENCE_IMAGES,
    "seedance-2.0-mini": ASYNC_VIDEO_GENERATION_DEFAULT_MAX_REFERENCE_IMAGES,
    "seedance-2.0-fast-5s": ASYNC_VIDEO_GENERATION_DEFAULT_MAX_REFERENCE_IMAGES,
    "seedance-2.0-fast-10s": ASYNC_VIDEO_GENERATION_DEFAULT_MAX_REFERENCE_IMAGES,
    "seedance-2.0-fast-15s": ASYNC_VIDEO_GENERATION_DEFAULT_MAX_REFERENCE_IMAGES,
};

export function asyncVideoGenerationModelDuration(model: string | undefined) {
    return ASYNC_VIDEO_GENERATION_MODEL_DURATIONS[String(model || "").trim().toLowerCase()];
}

export function asyncVideoGenerationMaxReferenceImages(model: string | undefined) {
    return ASYNC_VIDEO_GENERATION_MAX_REFERENCE_IMAGES[String(model || "").trim().toLowerCase()] || ASYNC_VIDEO_GENERATION_DEFAULT_MAX_REFERENCE_IMAGES;
}

export function normalizeMiniMaxH3Duration(value: string | number | undefined) {
    const seconds = Math.floor(Number(value) || 5);
    return Math.max(MINIMAX_H3_DURATION_OPTIONS[0], Math.min(MINIMAX_H3_DURATION_OPTIONS[MINIMAX_H3_DURATION_OPTIONS.length - 1], seconds));
}

export function normalizeMiniMaxH3Resolution(value: string | number | undefined) {
    return ["2k", "4k", "2160", "2160p"].includes(String(value || "").trim().toLowerCase()) ? "2K" : "768P";
}

export function normalizeMiniMaxH3Ratio(value: string | undefined, allowAdaptive = true) {
    const raw = String(value || "").trim();
    if (!raw || raw === "auto" || raw === "adaptive") return allowAdaptive ? "adaptive" : "16:9";
    const direct = MINIMAX_H3_RATIO_OPTIONS.find((item) => item.value === raw && item.value !== "adaptive");
    if (direct) return direct.value;
    const match = raw.match(/^(\d+)x(\d+)$/);
    if (!match) return allowAdaptive ? "adaptive" : "16:9";
    const ratio = Number(match[1]) / Number(match[2]);
    if (!Number.isFinite(ratio) || ratio <= 0) return allowAdaptive ? "adaptive" : "16:9";
    const choices = MINIMAX_H3_RATIO_OPTIONS.filter((item) => item.value !== "adaptive");
    return choices.reduce((best, item) => Math.abs(ratioFor(item.value) - ratio) < Math.abs(ratioFor(best.value) - ratio) ? item : best, choices[0]).value;
}

function ratioFor(value: string) {
    const [width, height] = value.split(":").map(Number);
    return width / height;
}

export function normalizeVideoSize(value: string | undefined) {
    const size = String(value || "").trim();
    if (size === "auto") return "auto";
    if (/^\d+x\d+$/.test(size)) return size;
    // 全局默认值是比例字符串；OpenAI 视频接口需要明确尺寸，方形不能降级成横屏。
    if (size === "1:1") return "1024x1024";
    return ["9:16", "2:3", "3:4"].includes(size) ? "720x1280" : "1280x720";
}

export function normalizeVideoDuration(value: string | number | undefined) {
    const seconds = Math.floor(Number(value) || VIDEO_DURATION_OPTIONS[0]);
    return String(Math.max(VIDEO_DURATION_MIN, seconds));
}

export function normalizeVideoResolution(value: string | number | undefined) {
    const token = String(value || "").trim().toLowerCase();
    if (token === "low") return "480";
    if (token === "auto" || token === "medium" || token === "high") return "720";
    if (token === "4k") return "2160";
    const resolution = Number(token.replace(/p$/i, "")) || 720;
    return String(nearestOption(resolution, VIDEO_RESOLUTION_OPTIONS));
}

function nearestOption(value: number, options: readonly number[]) {
    return options.reduce((nearest, option) => Math.abs(option - value) < Math.abs(nearest - value) ? option : nearest, options[0]);
}
