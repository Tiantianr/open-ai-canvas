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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	comfyUIH3MaxImages       = 9
	comfyUIH3MaxImageBytes   = 30 * 1024 * 1024
	comfyUIH3MaxPromptRunes  = 7000
	comfyUIH3FramesPerSecond = 24
	comfyUIH3OutputNodeID    = "92"
)

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
	Gifs   []comfyUIH3OutputFile `json:"gifs"`
}

type comfyUIH3OutputFile struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

func isComfyUIH3Model(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "minimax-h3" || value == "minimax-h3-r2v"
}

func testComfyUIH3Connection(ctx context.Context, config providerConfig) error {
	var stats map[string]interface{}
	if err := comfyUIH3JSON(ctx, config, http.MethodGet, "/system_stats", nil, &stats); err != nil {
		return fmt.Errorf("ComfyUI system_stats 检查失败：%w", err)
	}
	var node map[string]interface{}
	if err := comfyUIH3JSON(ctx, config, http.MethodGet, "/object_info/MiniMaxH3ReferenceToVideo", nil, &node); err != nil {
		return fmt.Errorf("ComfyUI 未提供 MiniMaxH3ReferenceToVideo 节点：%w", err)
	}
	if len(node) == 0 {
		return errors.New("ComfyUI 未提供 MiniMaxH3ReferenceToVideo 节点")
	}
	return nil
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
	if modelName := strings.ToLower(strings.TrimSpace(input.Config.Model)); modelName != "minimax-h3" && modelName != "minimax-h3-r2v" {
		return nil, errors.New("ComfyUI H3 协议仅支持模型 MiniMax-H3-R2V")
	}
	if len(input.ReferenceImages) == 0 {
		return nil, errors.New("ComfyUI H3 Ref2VA 至少需要一张参考图")
	}
	if len(input.ReferenceImages) > comfyUIH3MaxImages {
		return nil, fmt.Errorf("ComfyUI H3 Ref2VA 最多支持 %d 张参考图", comfyUIH3MaxImages)
	}
	if len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
		return nil, errors.New("ComfyUI H3 首版仅支持静态图片 Ref2VA，请移除参考视频和参考音频")
	}
	if operation := metadataString(input.Metadata, "videoEditOperation"); operation != "" && operation != "image_to_video" {
		return nil, errors.New("ComfyUI H3 首版仅支持 image_to_video")
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(input.Config.VideoSeconds))
	if err != nil || seconds < 5 || seconds > 15 {
		return nil, errors.New("ComfyUI H3 视频时长必须为 5 到 15 秒")
	}

	filenames := make([]string, 0, len(input.ReferenceImages))
	for index, reference := range input.ReferenceImages {
		filename, err := uploadComfyUIH3Image(ctx, input.Config, reference, index)
		if err != nil {
			return nil, err
		}
		filenames = append(filenames, filename)
	}
	width, height := comfyUIH3Dimensions(input.Config.Size, input.Config.VQuality, input.ReferenceImages[0])
	return map[string]interface{}{
		"prompt": comfyUIH3Workflow(input.Prompt, filenames, width, height, comfyUIH3FrameCount(seconds), time.Now().UnixNano()&0x7fffffffffffffff, parseBool(input.Config.VideoGenerateAudio, true)),
	}, nil
}

func comfyUIH3Workflow(prompt string, filenames []string, width, height, frames int, seed int64, generateAudio bool) map[string]interface{} {
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
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filename := providerMediaFilename(media, mimeType)
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
		return "", errors.New("ComfyUI 上传参考图后没有返回文件名")
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
		return nil, "", errors.New("参考图需要 data URL 或可访问 URL")
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
		for _, file := range append(output.Videos, output.Gifs...) {
			if strings.TrimSpace(file.Filename) != "" {
				return file, true
			}
		}
	}
	for _, output := range entry.Outputs {
		for _, file := range append(output.Videos, output.Gifs...) {
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
