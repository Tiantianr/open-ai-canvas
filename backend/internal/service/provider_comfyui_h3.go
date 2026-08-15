package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	comfyUIH3MaxImages       = 9
	comfyUIH3MaxImageBytes   = 30 * 1024 * 1024
	comfyUIH3MaxAudios       = 3
	comfyUIH3MaxAudioBytes   = 15 * 1024 * 1024
	comfyUIH3MinAudioMs      = 1800
	comfyUIH3MaxAudioMs      = 15000
	comfyUIH3MaxPromptRunes  = 7000
	comfyUIH3FramesPerSecond = 24
	comfyUIH3OutputNodeID    = "92"
	comfyUIH3PDDModel        = "minimax-h3-r2v-pdd-4step"
	comfyUIH3PDDLoraName     = "LORA_h3_pdd_af384_step600_s.safetensors"
	comfyUIH3PDDHeadsName    = "HEADS_h3_pdd_af384_step600_bank.safetensors"
)

var comfyUIH3TextLabelPattern = regexp.MustCompile(`【文本[1-9][0-9]*】`)

type comfyUIH3TaskFailure struct {
	PromptID string
	Reason   string
}

func (e *comfyUIH3TaskFailure) Error() string {
	if strings.TrimSpace(e.Reason) != "" {
		return "ComfyUI H3 任务失败：" + e.Reason
	}
	return "ComfyUI H3 任务失败"
}

type comfyUIH3PromptResponse struct {
	PromptID   string                     `json:"prompt_id"`
	NodeErrors map[string]json.RawMessage `json:"node_errors"`
	Error      string                     `json:"error"`
}

type comfyUIH3HistoryEntry struct {
	Outputs map[string]comfyUIH3NodeOutput `json:"outputs"`
	Status  struct {
		StatusStr string            `json:"status_str"`
		Messages  []json.RawMessage `json:"messages"`
	} `json:"status"`
}

type comfyUIH3NodeOutput struct {
	Videos []comfyUIH3OutputFile `json:"videos"`
	Images []comfyUIH3OutputFile `json:"images"`
	Gifs   []comfyUIH3OutputFile `json:"gifs"`
}

type comfyUIH3OutputFile struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

func isComfyUIH3Model(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "minimax-h3" || value == "minimax-h3-r2v" || value == comfyUIH3PDDModel
}

func isComfyUIH3PDDModel(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), comfyUIH3PDDModel)
}

func testComfyUIH3Connection(ctx context.Context, config providerConfig) error {
	var stats map[string]interface{}
	if err := comfyUIH3JSON(ctx, config, http.MethodGet, "/system_stats", nil, &stats); err != nil {
		return fmt.Errorf("ComfyUI system_stats 检查失败：%w", err)
	}
	h3Info, err := comfyUIH3ObjectInfo(ctx, config, "MiniMaxH3ReferenceToVideo")
	if err != nil {
		return fmt.Errorf("ComfyUI 未提供 MiniMaxH3ReferenceToVideo 节点：%w", err)
	}
	if !comfyUIH3ContainsString(h3Info, "ref_audio_") {
		return errors.New("ComfyUI MiniMaxH3ReferenceToVideo 节点不支持独立参考音频")
	}
	if _, err := comfyUIH3ObjectInfo(ctx, config, "LoadAudio"); err != nil {
		return fmt.Errorf("ComfyUI 未提供 LoadAudio 节点：%w", err)
	}
	if !isComfyUIH3PDDModel(config.Model) {
		return nil
	}
	infos := map[string]interface{}{}
	for _, name := range []string{"LoraLoaderModelOnly", "MiniMaxH3SigmaShift", "MiniMaxH3PDDHeadsLoader", "MiniMaxH3PDDModelPatch", "MiniMaxH3PDDScheduler"} {
		info, err := comfyUIH3ObjectInfo(ctx, config, name)
		if err != nil {
			return fmt.Errorf("ComfyUI PDD 缺少节点 %s：%w", name, err)
		}
		infos[name] = info
	}
	for nodeName, assetName := range map[string]string{"LoraLoaderModelOnly": comfyUIH3PDDLoraName, "MiniMaxH3PDDHeadsLoader": comfyUIH3PDDHeadsName} {
		if !comfyUIH3ContainsString(infos[nodeName], assetName) {
			return fmt.Errorf("ComfyUI PDD 缺少权重 %s", assetName)
		}
	}
	return nil
}

func comfyUIH3ObjectInfo(ctx context.Context, config providerConfig, name string) (interface{}, error) {
	var result map[string]interface{}
	if err := comfyUIH3JSON(ctx, config, http.MethodGet, "/object_info/"+url.PathEscape(name), nil, &result); err != nil {
		return nil, err
	}
	info, ok := result[name]
	if !ok || info == nil {
		return nil, errors.New("节点未注册")
	}
	return info, nil
}

func comfyUIH3ContainsString(value interface{}, target string) bool {
	switch typed := value.(type) {
	case string:
		return typed == target
	case []interface{}:
		for _, item := range typed {
			if comfyUIH3ContainsString(item, target) {
				return true
			}
		}
	case map[string]interface{}:
		for _, item := range typed {
			if comfyUIH3ContainsString(item, target) {
				return true
			}
		}
	}
	return false
}

func runComfyUIH3Task(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	promptID := resumedProviderRequestID(ctx)
	if promptID == "" && comfyUIH3SubmissionWasStarted(ctx) {
		return nil, errors.New("ComfyUI H3 提交状态不确定，已停止自动重提；请先在 ComfyUI 历史记录中核对")
	}
	if promptID == "" {
		body, err := comfyUIH3PromptBody(ctx, input)
		if err != nil {
			return nil, err
		}
		if err := markComfyUIH3SubmissionStarted(ctx); err != nil {
			return nil, fmt.Errorf("保存 ComfyUI H3 提交状态失败：%w", err)
		}
		var created comfyUIH3PromptResponse
		if err := comfyUIH3JSON(withProviderRequestKind(ctx, "create"), input.Config, http.MethodPost, "/prompt", body, &created); err != nil {
			return nil, err
		}
		if strings.TrimSpace(created.Error) != "" {
			return nil, fmt.Errorf("ComfyUI 拒绝工作流：%s", created.Error)
		}
		if len(created.NodeErrors) > 0 {
			return nil, fmt.Errorf("ComfyUI 工作流节点校验失败")
		}
		promptID = strings.TrimSpace(created.PromptID)
		if promptID == "" {
			return nil, errors.New("ComfyUI 没有返回 prompt_id")
		}
	}

	deadline := providerPollingDeadline(ctx)
	consecutiveQueryFailures := 0
	for time.Now().Before(deadline) {
		result, pending, err := queryComfyUIH3Task(ctx, input, promptID)
		if err == nil && !pending {
			return result, nil
		}
		if err != nil && !isTransientComfyUIH3QueryError(err) {
			return nil, err
		}
		if err != nil {
			consecutiveQueryFailures++
			if consecutiveQueryFailures >= 3 {
				return nil, fmt.Errorf("ComfyUI H3 状态查询连续失败：%w", err)
			}
		} else {
			consecutiveQueryFailures = 0
		}
		if err := sleepContext(ctx, 5*time.Second); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("ComfyUI H3 视频生成超时")
}

func comfyUIH3SubmissionWasStarted(ctx context.Context) bool {
	metadata, _ := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	stage := strings.ToLower(strings.TrimSpace(metadata.PollStage))
	return stage == "submitting" || stage == "create"
}

func markComfyUIH3SubmissionStarted(ctx context.Context) error {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil || strings.TrimSpace(metadata.TaskID) == "" {
		return nil
	}
	return metadata.Service.repo.UpdateTaskProviderState(metadata.TaskID, "", "submitting", nil)
}

func comfyUIH3PromptBody(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	if strings.TrimSpace(input.Prompt) == "" {
		return nil, errors.New("ComfyUI H3 需要非空视频提示词")
	}
	if utf8.RuneCountInString(input.Prompt) > comfyUIH3MaxPromptRunes {
		return nil, fmt.Errorf("ComfyUI H3 视频提示词不能超过 %d 个字符", comfyUIH3MaxPromptRunes)
	}
	if !comfyUIH3PromptReferencesExpanded(input.Prompt) {
		return nil, errors.New("视频提示词仍包含未展开的文本节点引用，请重新打开画布，或把文本节点正文直接填入提示词")
	}
	if !isComfyUIH3Model(input.Config.Model) {
		return nil, errors.New("ComfyUI H3 协议仅支持模型 MiniMax-H3-R2V 或 MiniMax-H3-R2V-PDD-4Step")
	}
	if len(input.ReferenceImages) == 0 {
		return nil, errors.New("ComfyUI H3 Ref2VA 至少需要一张参考图")
	}
	if len(input.ReferenceImages) > comfyUIH3MaxImages {
		return nil, fmt.Errorf("ComfyUI H3 Ref2VA 最多支持 %d 张参考图", comfyUIH3MaxImages)
	}
	if len(input.ReferenceVideos) > 0 {
		return nil, errors.New("ComfyUI H3 暂不支持参考视频，请移除参考视频")
	}
	if len(input.ReferenceAudios) > comfyUIH3MaxAudios {
		return nil, fmt.Errorf("ComfyUI H3 Ref2VA 最多支持 %d 个参考音频", comfyUIH3MaxAudios)
	}
	totalAudioDuration := int64(0)
	for index, audio := range input.ReferenceAudios {
		if audio.DurationMs > 0 && audio.DurationMs < comfyUIH3MinAudioMs {
			return nil, fmt.Errorf("第 %d 个参考音频不能短于 2 秒", index+1)
		}
		totalAudioDuration += audio.DurationMs
	}
	if totalAudioDuration > comfyUIH3MaxAudioMs {
		return nil, errors.New("ComfyUI H3 参考音频总时长不能超过 15 秒")
	}
	if operation := metadataString(input.Metadata, "videoEditOperation"); operation != "" && operation != "image_to_video" {
		return nil, errors.New("ComfyUI H3 首版仅支持 image_to_video")
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(input.Config.VideoSeconds))
	if err != nil || seconds < 5 || seconds > 15 {
		return nil, errors.New("ComfyUI H3 视频时长必须为 5 到 15 秒")
	}
	pdd := isComfyUIH3PDDModel(input.Config.Model)
	if pdd {
		if seconds != 5 && seconds != 7 && seconds != 10 && seconds != 15 {
			return nil, errors.New("ComfyUI H3 PDD 4-Step 仅支持 5、7、10 或 15 秒")
		}
		size := strings.ToLower(strings.TrimSpace(input.Config.Size))
		if size != "" && size != "16:9" && size != "9:16" {
			return nil, errors.New("ComfyUI H3 PDD 4-Step 首版仅支持 16:9 或 9:16")
		}
		quality := strings.ToLower(strings.TrimSpace(input.Config.VQuality))
		if quality != "" && quality != "480" && quality != "480p" {
			return nil, errors.New("ComfyUI H3 PDD 4-Step 首版仅支持 480P")
		}
	}

	filenames := make([]string, 0, len(input.ReferenceImages))
	for index, reference := range input.ReferenceImages {
		filename, err := uploadComfyUIH3Image(ctx, input.Config, reference, index)
		if err != nil {
			return nil, err
		}
		filenames = append(filenames, filename)
	}
	audioFilenames := make([]string, 0, len(input.ReferenceAudios))
	for index, reference := range input.ReferenceAudios {
		filename, err := uploadComfyUIH3Audio(ctx, input.Config, reference, index)
		if err != nil {
			return nil, err
		}
		audioFilenames = append(audioFilenames, filename)
	}
	seed := time.Now().UnixNano() & 0x7fffffffffffffff
	generateAudio := parseBool(input.Config.VideoGenerateAudio, true)
	if pdd {
		width, height := comfyUIH3PDDDimensions(input.Config.Size, len(input.ReferenceImages))
		return map[string]interface{}{
			"prompt": comfyUIH3PDDWorkflow(input.Prompt, filenames, audioFilenames, width, height, comfyUIH3FrameCount(seconds), seed, generateAudio),
		}, nil
	}
	width, height := comfyUIH3Dimensions(input.Config.Size, input.Config.VQuality, input.ReferenceImages[0])
	return map[string]interface{}{
		"prompt": comfyUIH3Workflow(input.Prompt, filenames, audioFilenames, width, height, comfyUIH3FrameCount(seconds), seed, generateAudio),
	}, nil
}

func comfyUIH3PromptReferencesExpanded(prompt string) bool {
	if strings.Contains(prompt, "@[node:") {
		return false
	}
	lines := strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n")
	for _, label := range comfyUIH3TextLabelPattern.FindAllString(prompt, -1) {
		if !comfyUIH3HasTextBlock(lines, label) {
			return false
		}
	}
	return true
}

func comfyUIH3HasTextBlock(lines []string, label string) bool {
	for index, line := range lines {
		if strings.TrimSpace(line) != label {
			continue
		}
		for _, next := range lines[index+1:] {
			next = strings.TrimSpace(next)
			if next == "" {
				continue
			}
			if comfyUIH3TextLabelPattern.FindString(next) == next {
				break
			}
			return true
		}
	}
	return false
}

func comfyUIH3Workflow(prompt string, filenames, audioFilenames []string, width, height, frames int, seed int64, generateAudio bool) map[string]interface{} {
	link := func(node string, slot int) []interface{} { return []interface{}{node, slot} }
	node := func(classType string, inputs map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"class_type": classType, "inputs": inputs}
	}

	h3Inputs := map[string]interface{}{
		"clip":           link("128", 0),
		"vae":            link("119", 0),
		"audio_vae":      link("120", 0),
		"prompt":         strings.TrimSpace(prompt),
		"width":          width,
		"height":         height,
		"length":         frames,
		"ref_image_size": "match",
	}
	createVideoInputs := map[string]interface{}{"images": link("122", 0), "fps": comfyUIH3FramesPerSecond, "bit_depth": 8}
	if generateAudio {
		createVideoInputs["audio"] = link("121", 0)
	}
	workflow := map[string]interface{}{
		"92":  node("SaveVideo", map[string]interface{}{"video": link("130", 0), "filename_prefix": "Canvas/ComfyUI-H3", "format": "auto", "codec": "auto"}),
		"119": node("VAELoader", map[string]interface{}{"vae_name": "minimax_h3_video_vae_fp16.safetensors"}),
		"120": node("VAELoader", map[string]interface{}{"vae_name": "minimax_h3_audio_vae_fp32.safetensors"}),
		"121": node("VAEDecodeAudio", map[string]interface{}{"samples": link("125", 0), "vae": link("120", 0)}),
		"122": node("VAEDecode", map[string]interface{}{"samples": link("125", 0), "vae": link("119", 0)}),
		"123": node("KSamplerSelect", map[string]interface{}{"sampler_name": "res_multistep"}),
		"124": node("BasicScheduler", map[string]interface{}{"model": link("127", 0), "scheduler": "simple", "steps": 20, "denoise": 1}),
		"125": node("SamplerCustomAdvanced", map[string]interface{}{"noise": link("129", 0), "guider": link("126", 0), "sampler": link("123", 0), "sigmas": link("124", 0), "latent_image": link("136", 1)}),
		"126": node("BasicGuider", map[string]interface{}{"model": link("127", 0), "conditioning": link("136", 0)}),
		"127": node("UNETLoader", map[string]interface{}{"unet_name": "minimax_h3_ref2va_pruned_int8_convrot.safetensors", "weight_dtype": "default"}),
		"128": node("CLIPLoader", map[string]interface{}{"clip_name": "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors", "type": "minimax", "device": "default"}),
		"129": node("RandomNoise", map[string]interface{}{"noise_seed": seed}),
		"130": node("CreateVideo", createVideoInputs),
		"136": node("MiniMaxH3ReferenceToVideo", h3Inputs),
	}
	for index, filename := range filenames {
		id := strconv.Itoa(137 + index)
		workflow[id] = node("LoadImage", map[string]interface{}{"image": filename})
		h3Inputs[fmt.Sprintf("ref_images.ref_image_%d", index)] = link(id, 0)
	}
	for index, filename := range audioFilenames {
		id := strconv.Itoa(146 + index)
		workflow[id] = node("LoadAudio", map[string]interface{}{"audio": filename})
		h3Inputs[fmt.Sprintf("ref_audios.ref_audio_%d", index)] = link(id, 0)
	}
	return workflow
}

func comfyUIH3PDDWorkflow(prompt string, filenames, audioFilenames []string, width, height, frames int, seed int64, generateAudio bool) map[string]interface{} {
	link := func(node string, slot int) []interface{} { return []interface{}{node, slot} }
	node := func(classType string, inputs map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{"class_type": classType, "inputs": inputs}
	}

	h3Inputs := map[string]interface{}{
		"clip": link("3", 0), "vae": link("1", 0), "audio_vae": link("2", 0),
		"prompt": strings.TrimSpace(prompt), "width": width, "height": height, "length": frames, "ref_image_size": "match",
	}
	createVideoInputs := map[string]interface{}{"images": link("17", 0), "fps": comfyUIH3FramesPerSecond, "bit_depth": 8}
	if generateAudio {
		createVideoInputs["audio"] = link("18", 0)
	}
	workflow := map[string]interface{}{
		"1":  node("VAELoader", map[string]interface{}{"vae_name": "minimax_h3_video_vae_fp16.safetensors"}),
		"2":  node("VAELoader", map[string]interface{}{"vae_name": "minimax_h3_audio_vae_fp32.safetensors"}),
		"3":  node("CLIPLoader", map[string]interface{}{"clip_name": "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors", "type": "minimax", "device": "default"}),
		"4":  node("UNETLoader", map[string]interface{}{"unet_name": "minimax_h3_ref2va_pruned_int8_convrot.safetensors", "weight_dtype": "default"}),
		"7":  node("MiniMaxH3ReferenceToVideo", h3Inputs),
		"8":  node("LoraLoaderModelOnly", map[string]interface{}{"model": link("4", 0), "lora_name": comfyUIH3PDDLoraName, "strength_model": 2.0}),
		"9":  node("MiniMaxH3SigmaShift", map[string]interface{}{"model": link("8", 0), "shift_video": 12.0, "shift_audio": 3.0}),
		"10": node("MiniMaxH3PDDHeadsLoader", map[string]interface{}{"heads_name": comfyUIH3PDDHeadsName, "blocks": 4, "partition": ""}),
		"11": node("MiniMaxH3PDDModelPatch", map[string]interface{}{"model": link("9", 0), "pdd_heads": link("10", 0), "mode": "exact_euler_step", "on_out_of_grid": "clamp", "head_strength": 1.0, "contract": "enforce"}),
		"12": node("RandomNoise", map[string]interface{}{"noise_seed": seed}),
		"13": node("BasicGuider", map[string]interface{}{"model": link("11", 0), "conditioning": link("7", 0)}),
		"14": node("KSamplerSelect", map[string]interface{}{"sampler_name": "euler"}),
		"15": node("MiniMaxH3PDDScheduler", map[string]interface{}{"pdd_heads": link("10", 0), "mode": "trained_blocks", "steps": 4, "denoise": 1.0}),
		"16": node("SamplerCustomAdvanced", map[string]interface{}{"noise": link("12", 0), "guider": link("13", 0), "sampler": link("14", 0), "sigmas": link("15", 0), "latent_image": link("7", 1)}),
		"17": node("VAEDecode", map[string]interface{}{"samples": link("16", 0), "vae": link("1", 0)}),
		"18": node("VAEDecodeAudio", map[string]interface{}{"samples": link("16", 0), "vae": link("2", 0)}),
		"19": node("CreateVideo", createVideoInputs),
		"20": node("SaveVideo", map[string]interface{}{"video": link("19", 0), "filename_prefix": "Canvas/ComfyUI-H3-PDD4", "format": "mp4", "codec": "auto"}),
	}
	for index, filename := range filenames {
		id := strconv.Itoa(100 + index)
		workflow[id] = node("LoadImage", map[string]interface{}{"image": filename})
		h3Inputs[fmt.Sprintf("ref_images.ref_image_%d", index)] = link(id, 0)
	}
	for index, filename := range audioFilenames {
		id := strconv.Itoa(110 + index)
		workflow[id] = node("LoadAudio", map[string]interface{}{"audio": filename})
		h3Inputs[fmt.Sprintf("ref_audios.ref_audio_%d", index)] = link(id, 0)
	}
	return workflow
}

func uploadComfyUIH3Image(ctx context.Context, config providerConfig, media providerMedia, index int) (string, error) {
	raw, mimeType, err := comfyUIH3MediaBytes(ctx, config, media)
	if err != nil {
		return "", fmt.Errorf("读取第 %d 张参考图失败：%w", index+1, err)
	}
	if len(raw) == 0 || len(raw) > comfyUIH3MaxImageBytes {
		return "", fmt.Errorf("第 %d 张参考图必须小于 %dMB", index+1, comfyUIH3MaxImageBytes/(1024*1024))
	}
	mimeType = normalizedMediaMimeType(mimeType, raw)
	if detected := strings.TrimSpace(strings.Split(http.DetectContentType(raw), ";")[0]); !strings.HasPrefix(detected, "image/") {
		return "", fmt.Errorf("第 %d 个参考素材不是图片", index+1)
	}
	return uploadComfyUIH3Input(ctx, config, media, raw, mimeType, "参考图", "")
}

func uploadComfyUIH3Audio(ctx context.Context, config providerConfig, media providerMedia, index int) (string, error) {
	raw, mimeType, err := comfyUIH3MediaBytes(ctx, config, media)
	if err != nil {
		return "", fmt.Errorf("读取第 %d 个参考音频失败：%w", index+1, err)
	}
	if len(raw) == 0 || len(raw) > comfyUIH3MaxAudioBytes {
		return "", fmt.Errorf("第 %d 个参考音频不能超过 %dMB", index+1, comfyUIH3MaxAudioBytes/(1024*1024))
	}
	mimeType = normalizedMediaMimeType(mimeType, raw)
	if !strings.HasPrefix(mimeType, "audio/") || !audioSignatureMatches(mimeType, raw) {
		return "", fmt.Errorf("第 %d 个参考素材不是可识别音频", index+1)
	}
	return uploadComfyUIH3Input(ctx, config, media, raw, mimeType, "参考音频", comfyUIH3AudioExtension(mimeType))
}

func comfyUIH3AudioExtension(mimeType string) string {
	switch {
	case strings.Contains(mimeType, "wav"), strings.Contains(mimeType, "wave"):
		return ".wav"
	case strings.Contains(mimeType, "mpeg"), strings.Contains(mimeType, "mp3"):
		return ".mp3"
	case strings.Contains(mimeType, "flac"):
		return ".flac"
	case strings.Contains(mimeType, "aac"):
		return ".aac"
	default:
		return ".ogg"
	}
}

func uploadComfyUIH3Input(ctx context.Context, config providerConfig, media providerMedia, raw []byte, mimeType, label, extension string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filename := providerMediaFilename(media, mimeType)
	if extension != "" {
		filename = strings.TrimSuffix(filename, path.Ext(filename)) + extension
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "image", "filename": filename}))
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(raw); err != nil {
		return "", err
	}
	_ = writer.WriteField("type", "input")
	if err := writer.Close(); err != nil {
		return "", err
	}
	var uploaded struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
	}
	if err := comfyUIH3JSON(withProviderRequestKind(ctx, "upload"), config, http.MethodPost, "/upload/image", &multipartRequest{body: &body, contentType: writer.FormDataContentType()}, &uploaded); err != nil {
		return "", err
	}
	if strings.TrimSpace(uploaded.Name) == "" {
		return "", fmt.Errorf("ComfyUI 上传%s后没有返回文件名", label)
	}
	return path.Join(strings.TrimSpace(uploaded.Subfolder), strings.TrimSpace(uploaded.Name)), nil
}

type multipartRequest struct {
	body        io.Reader
	contentType string
}

func comfyUIH3MediaBytes(ctx context.Context, config providerConfig, media providerMedia) ([]byte, string, error) {
	value := strings.TrimSpace(media.DataURL)
	if value == "" {
		value = strings.TrimSpace(media.URL)
	}
	if strings.HasPrefix(value, "data:") {
		return mediaBytes(providerMedia{DataURL: value})
	}
	if !isPublicMediaURL(value) {
		return nil, "", errors.New("参考素材需要 data URL 或可访问 URL")
	}
	return getProviderExternalBinary(withProviderRequestKind(ctx, "download"), config, value)
}

func queryComfyUIH3Task(ctx context.Context, input canvasGenerationInput, promptID string) (map[string]interface{}, bool, error) {
	var history map[string]comfyUIH3HistoryEntry
	if err := comfyUIH3JSON(withProviderRequestKind(ctx, "poll"), input.Config, http.MethodGet, "/history/"+url.PathEscape(promptID), nil, &history); err != nil {
		return nil, true, err
	}
	entry, ok := history[promptID]
	if !ok {
		return nil, true, nil
	}
	status := strings.ToLower(strings.TrimSpace(entry.Status.StatusStr))
	if status == "error" || status == "failed" || status == "cancelled" || status == "canceled" {
		return nil, false, &comfyUIH3TaskFailure{PromptID: promptID, Reason: comfyUIH3FailureReason(entry)}
	}
	file, ok := comfyUIH3VideoOutput(entry)
	if !ok {
		if status == "success" || status == "completed" {
			return nil, false, errors.New("ComfyUI H3 任务已完成但没有返回视频文件")
		}
		return nil, true, nil
	}
	query := url.Values{}
	query.Set("filename", file.Filename)
	query.Set("subfolder", file.Subfolder)
	query.Set("type", defaultString(file.Type, "output"))
	raw, mimeType, err := comfyUIH3Binary(withProviderRequestKind(ctx, "download"), input.Config, http.MethodGet, "/view?"+query.Encode(), nil, "")
	if err != nil {
		return nil, false, err
	}
	mimeType = strings.TrimSpace(strings.Split(mimeType, ";")[0])
	if !strings.HasPrefix(mimeType, "video/") {
		switch strings.ToLower(path.Ext(file.Filename)) {
		case ".mp4":
			mimeType = "video/mp4"
		case ".webm":
			mimeType = "video/webm"
		case ".mov":
			mimeType = "video/quicktime"
		default:
			return nil, false, errors.New("ComfyUI H3 返回的结果不是可识别的视频文件")
		}
	}
	return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, raw), "mimeType": mimeType}}, false, nil
}

func comfyUIH3VideoOutput(entry comfyUIH3HistoryEntry) (comfyUIH3OutputFile, bool) {
	if output, ok := entry.Outputs[comfyUIH3OutputNodeID]; ok {
		if file, ok := comfyUIH3FirstOutputFile(output); ok {
			return file, true
		}
	}
	for _, output := range entry.Outputs {
		if file, ok := comfyUIH3FirstOutputFile(output); ok {
			return file, true
		}
	}
	return comfyUIH3OutputFile{}, false
}

func comfyUIH3FirstOutputFile(output comfyUIH3NodeOutput) (comfyUIH3OutputFile, bool) {
	for _, files := range [][]comfyUIH3OutputFile{output.Videos, output.Images, output.Gifs} {
		for _, file := range files {
			if strings.TrimSpace(file.Filename) != "" {
				return file, true
			}
		}
	}
	return comfyUIH3OutputFile{}, false
}

func comfyUIH3FailureReason(entry comfyUIH3HistoryEntry) string {
	for _, message := range entry.Status.Messages {
		if len(message) > 0 {
			return string(message)
		}
	}
	return "工作流执行失败"
}

func comfyUIH3JSON(ctx context.Context, config providerConfig, method, requestPath string, body interface{}, target interface{}) error {
	var reader io.Reader
	contentType := ""
	if body != nil {
		if multipartBody, ok := body.(*multipartRequest); ok {
			reader, contentType = multipartBody.body, multipartBody.contentType
		} else {
			encoded, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader, contentType = bytes.NewReader(encoded), "application/json"
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, comfyUIH3APIURL(config.BaseURL, requestPath), reader)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if strings.TrimSpace(config.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	ApplyOutboundHeaders(req, config.Headers)
	return doJSON(req, target)
}

func comfyUIH3Binary(ctx context.Context, config providerConfig, method, requestPath string, body io.Reader, contentType string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, comfyUIH3APIURL(config.BaseURL, requestPath), body)
	if err != nil {
		return nil, "", err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if strings.TrimSpace(config.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	ApplyOutboundHeaders(req, config.Headers)
	return doBinary(req)
}

func comfyUIH3APIURL(baseURL, requestPath string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/prompt", "/history", "/view", "/system_stats"} {
		if strings.HasSuffix(strings.ToLower(base), suffix) {
			base = base[:len(base)-len(suffix)]
			break
		}
	}
	return base + "/" + strings.TrimLeft(requestPath, "/")
}

func comfyUIH3Dimensions(size, quality string, first providerMedia) (int, int) {
	ratio := 16.0 / 9.0
	if width, height, ok := comfyUIH3DimensionPair(size); ok {
		ratio = float64(width) / float64(height)
	} else if first.Width > 0 && first.Height > 0 && strings.TrimSpace(size) == "adaptive" {
		ratio = float64(first.Width) / float64(first.Height)
	} else if parts := strings.Split(strings.TrimSpace(size), ":"); len(parts) == 2 {
		if width, err := strconv.ParseFloat(parts[0], 64); err == nil && width > 0 {
			if height, err := strconv.ParseFloat(parts[1], 64); err == nil && height > 0 {
				ratio = width / height
			}
		}
	}
	shortEdge := 768.0
	maxPixels := 768.0 * 1344.0
	if normalized := strings.ToLower(strings.TrimSpace(quality)); normalized == "2160" || normalized == "2160p" || normalized == "2k" {
		shortEdge = 1088
		maxPixels = 1088 * 1920
	}
	width, height := shortEdge*ratio, shortEdge
	if ratio < 1 {
		width, height = shortEdge, shortEdge/ratio
	}
	if width*height > maxPixels {
		scale := math.Sqrt(maxPixels / (width * height))
		width, height = width*scale, height*scale
	}
	return comfyUIH3NearestMultiple(width, 32), comfyUIH3NearestMultiple(height, 32)
}

func comfyUIH3PDDDimensions(size string, imageCount int) (int, int) {
	width, height := 864, 480
	if imageCount >= 7 {
		width, height = 608, 352
	} else if imageCount >= 4 {
		width, height = 736, 416
	}
	if strings.EqualFold(strings.TrimSpace(size), "9:16") {
		return height, width
	}
	return width, height
}

func comfyUIH3DimensionPair(value string) (int, int, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(parts[0]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(parts[1]))
	return width, height, widthErr == nil && heightErr == nil && width > 0 && height > 0
}

func comfyUIH3NearestMultiple(value float64, multiple int) int {
	return max(multiple, int(math.Round(value/float64(multiple)))*multiple)
}

func comfyUIH3FrameCount(seconds int) int {
	frames := max(5, seconds*comfyUIH3FramesPerSecond)
	for frames%17 != 5 {
		frames++
	}
	return frames
}

func isTransientComfyUIH3QueryError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *providerHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= 500 || httpErr.StatusCode == http.StatusTooManyRequests
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) || errors.Is(err, io.ErrUnexpectedEOF)
}
