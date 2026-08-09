package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	miniMaxH3PollInterval          = 10 * time.Second
	miniMaxH3RetryInterval         = 10 * time.Second
	miniMaxH3MaxQueryRetries       = 3
	miniMaxH3MaxImageBytes   int64 = 30 << 20
	miniMaxH3MaxVideoBytes   int64 = 50 << 20
	miniMaxH3MaxAudioBytes   int64 = 15 << 20
	miniMaxH3MaxBodyBytes          = 64 << 20
)

type miniMaxH3TaskFailure struct {
	TaskID string
	Reason string
}

func (e *miniMaxH3TaskFailure) Error() string {
	return fmt.Sprintf("MiniMax H3 视频生成失败（任务 %s）：%s", e.TaskID, defaultString(e.Reason, "上游返回 failed"))
}

func runMiniMaxH3Task(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	id := resumedProviderRequestID(ctx)
	if id == "" {
		body, err := miniMaxH3Body(input)
		if err != nil {
			return nil, err
		}
		var created map[string]interface{}
		if err := miniMaxH3JSON(ctx, input.Config, http.MethodPost, "/video_generation", body, &created); err != nil {
			return nil, err
		}
		if err := miniMaxH3PayloadError(created); err != nil {
			return nil, err
		}
		id = miniMaxH3TaskID(created)
	}
	if id == "" {
		return nil, errors.New("MiniMax H3 接口没有返回任务 ID")
	}

	consecutiveQueryFailures := 0
	for deadline := providerPollingDeadline(ctx); time.Now().Before(deadline); {
		result, _, err := queryMiniMaxH3Task(ctx, input, id)
		if err != nil {
			if !isTransientMiniMaxH3QueryError(err) || consecutiveQueryFailures >= miniMaxH3MaxQueryRetries {
				return nil, err
			}
			consecutiveQueryFailures++
			logMiniMaxH3QueryRetry(ctx, id, consecutiveQueryFailures, err)
			if err := sleepContext(ctx, miniMaxH3RetryInterval); err != nil {
				return nil, err
			}
			continue
		}
		consecutiveQueryFailures = 0
		if result != nil {
			return result, nil
		}
		if err := sleepContext(ctx, miniMaxH3PollInterval); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("MiniMax H3 视频生成超时（任务 %s）", id)
}

func miniMaxH3Body(input canvasGenerationInput) (map[string]interface{}, error) {
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return nil, errors.New("MiniMax H3 需要非空视频提示词")
	}
	if utf8.RuneCountInString(prompt) > 7000 {
		return nil, errors.New("MiniMax H3 视频提示词不能超过 7000 个字符")
	}
	if !strings.EqualFold(strings.TrimSpace(input.Config.Model), "MiniMax-H3") {
		return nil, errors.New("MiniMax H3 协议仅支持模型 MiniMax-H3")
	}

	frames, err := miniMaxH3FrameImages(input)
	if err != nil {
		return nil, err
	}
	content := []map[string]interface{}{{"type": "text", "text": prompt}}
	if len(frames) > 0 {
		if len(input.ReferenceImages) != len(frames) || len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
			return nil, errors.New("MiniMax H3 的首尾帧模式不能混用普通参考图、参考视频或参考音频")
		}
		for _, frame := range frames {
			value, err := miniMaxH3MediaURL(frame.media, "image", miniMaxH3MaxImageBytes, "图片")
			if err != nil {
				return nil, err
			}
			content = append(content, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": value}, "role": frame.role})
		}
	} else {
		if err := validateMiniMaxH3References(input); err != nil {
			return nil, err
		}
		// 只有画布中显式选中的节点才是首尾帧，其他连接素材保持参考语义，避免静默改变生成模式。
		for _, image := range input.ReferenceImages {
			value, err := miniMaxH3MediaURL(image, "image", miniMaxH3MaxImageBytes, "参考图")
			if err != nil {
				return nil, err
			}
			content = append(content, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": value}, "role": "reference_image"})
		}
		for _, video := range input.ReferenceVideos {
			value, err := miniMaxH3MediaURL(video, "video", miniMaxH3MaxVideoBytes, "参考视频")
			if err != nil {
				return nil, err
			}
			content = append(content, map[string]interface{}{"type": "video_url", "video_url": map[string]interface{}{"url": value}, "role": "reference_video"})
		}
		for _, audio := range input.ReferenceAudios {
			value, err := miniMaxH3MediaURL(audio, "audio", miniMaxH3MaxAudioBytes, "参考音频")
			if err != nil {
				return nil, err
			}
			content = append(content, map[string]interface{}{"type": "audio_url", "audio_url": map[string]interface{}{"url": value}, "role": "reference_audio"})
		}
	}

	duration, err := miniMaxH3Duration(input.Config.VideoSeconds)
	if err != nil {
		return nil, err
	}
	hasFrames := len(frames) > 0
	hasReferences := len(input.ReferenceImages) > 0 || len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0
	body := map[string]interface{}{
		"model":      "MiniMax-H3",
		"content":    content,
		"resolution": miniMaxH3Resolution(input.Config.VQuality),
		"duration":   duration,
		"ratio":      miniMaxH3Ratio(input.Config.Size, hasFrames, hasReferences),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(encoded) > miniMaxH3MaxBodyBytes {
		return nil, errors.New("MiniMax H3 请求体不能超过 64MB，请将参考素材上传到 OSS")
	}
	return body, nil
}

type miniMaxH3FrameImage struct {
	media providerMedia
	role  string
}

func miniMaxH3FrameImages(input canvasGenerationInput) ([]miniMaxH3FrameImage, error) {
	startFrameID := metadataString(input.Metadata, "videoStartFrameNodeId")
	endFrameID := metadataString(input.Metadata, "videoEndFrameNodeId")
	if endFrameID != "" && startFrameID == "" {
		return nil, errors.New("MiniMax H3 尾帧必须同时指定首帧")
	}
	if startFrameID != "" && startFrameID == endFrameID {
		return nil, errors.New("MiniMax H3 的首帧和尾帧不能选择同一张参考图")
	}
	frames := make([]miniMaxH3FrameImage, 0, 2)
	appendFrame := func(id string, role string, label string) error {
		if id == "" {
			return nil
		}
		for _, image := range input.ReferenceImages {
			if image.ID == id {
				frames = append(frames, miniMaxH3FrameImage{media: image, role: role})
				return nil
			}
		}
		return fmt.Errorf("MiniMax H3 已配置的%s参考图未连接或不可用，请重新选择后再生成", label)
	}
	if err := appendFrame(startFrameID, "first_frame", "首帧"); err != nil {
		return nil, err
	}
	if err := appendFrame(endFrameID, "last_frame", "尾帧"); err != nil {
		return nil, err
	}
	return frames, nil
}

func validateMiniMaxH3References(input canvasGenerationInput) error {
	if len(input.ReferenceImages) > 9 || len(input.ReferenceVideos) > 3 || len(input.ReferenceAudios) > 3 {
		return errors.New("MiniMax H3 最多支持 9 张参考图、3 个参考视频和 3 个参考音频")
	}
	total := len(input.ReferenceImages) + len(input.ReferenceVideos) + len(input.ReferenceAudios)
	if total > 12 {
		return fmt.Errorf("MiniMax H3 最多支持 12 个参考素材，当前连接了 %d 个", total)
	}
	if len(input.ReferenceAudios) > 0 && len(input.ReferenceImages) == 0 && len(input.ReferenceVideos) == 0 {
		return errors.New("MiniMax H3 的参考音频需要同时连接至少一张参考图或一个参考视频")
	}
	return nil
}

func miniMaxH3MediaURL(media providerMedia, expectedType string, maxBytes int64, label string) (string, error) {
	value := ""
	if strings.HasPrefix(media.StorageKey, "resource:") {
		value = strings.TrimSpace(media.DataURL)
	}
	if value == "" {
		value = strings.TrimSpace(media.URL)
	}
	if isPublicMediaURL(value) {
		return value, nil
	}
	if !strings.HasPrefix(strings.ToLower(value), "data:") {
		value = strings.TrimSpace(media.DataURL)
	}
	if !strings.HasPrefix(strings.ToLower(value), "data:"+expectedType+"/") {
		return "", fmt.Errorf("MiniMax H3 %s需要公网 URL 或 data:%s/*;base64 数据", label, expectedType)
	}
	raw, _, err := mediaBytes(providerMedia{DataURL: value, Type: media.Type})
	if err != nil {
		return "", fmt.Errorf("读取 MiniMax H3 %s失败：%w", label, err)
	}
	if int64(len(raw)) > maxBytes {
		return "", fmt.Errorf("MiniMax H3 %s不能超过 %dMB", label, maxBytes>>20)
	}
	if expectedType == "image" {
		value, raw, err = miniMaxH3EvenImage(value, raw)
		if err != nil {
			return "", fmt.Errorf("规范化 MiniMax H3 %s失败：%w", label, err)
		}
		if int64(len(raw)) > maxBytes {
			return "", fmt.Errorf("MiniMax H3 %s规范化后不能超过 %dMB", label, maxBytes>>20)
		}
	}
	return value, nil
}

func miniMaxH3EvenImage(value string, raw []byte) (string, []byte, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (config.Width%2 == 0 && config.Height%2 == 0) {
		return value, raw, nil
	}
	if format != "png" && format != "jpeg" {
		return value, raw, nil
	}
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", nil, err
	}
	bounds := decoded.Bounds()
	targetBounds := image.Rect(0, 0, bounds.Dx()-bounds.Dx()%2, bounds.Dy()-bounds.Dy()%2)
	if targetBounds.Empty() {
		return "", nil, errors.New("图片尺寸无效")
	}
	target := image.NewRGBA(targetBounds)
	draw.Draw(target, targetBounds, decoded, bounds.Min, draw.Src)
	var encoded bytes.Buffer
	mimeType := "image/png"
	if format == "jpeg" {
		mimeType = "image/jpeg"
		err = jpeg.Encode(&encoded, target, &jpeg.Options{Quality: 95})
	} else {
		err = png.Encode(&encoded, target)
	}
	if err != nil {
		return "", nil, err
	}
	return dataURL(mimeType, encoded.Bytes()), encoded.Bytes(), nil
}

func miniMaxH3Duration(value string) (int, error) {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 4 || seconds > 15 {
		return 0, errors.New("MiniMax H3 视频时长需要是 4-15 秒的整数")
	}
	return seconds, nil
}

func miniMaxH3Resolution(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "2k", "4k", "2160", "2160p":
		return "2K"
	default:
		return "768P"
	}
}

func miniMaxH3Ratio(value string, hasFrames bool, hasReferences bool) string {
	if hasFrames {
		return "adaptive"
	}
	value = strings.TrimSpace(value)
	if value == "" || value == "auto" || value == "adaptive" {
		if hasReferences {
			return "adaptive"
		}
		return "16:9"
	}
	allowed := []struct {
		value string
		ratio float64
	}{
		{"21:9", 21.0 / 9.0}, {"16:9", 16.0 / 9.0}, {"4:3", 4.0 / 3.0},
		{"1:1", 1}, {"3:4", 3.0 / 4.0}, {"9:16", 9.0 / 16.0},
	}
	for _, item := range allowed {
		if value == item.value {
			return value
		}
	}
	parts := strings.SplitN(value, "x", 2)
	if len(parts) != 2 {
		if hasReferences {
			return "adaptive"
		}
		return "16:9"
	}
	width, widthErr := strconv.ParseFloat(parts[0], 64)
	height, heightErr := strconv.ParseFloat(parts[1], 64)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		if hasReferences {
			return "adaptive"
		}
		return "16:9"
	}
	target := width / height
	best := allowed[0]
	for _, item := range allowed[1:] {
		if absFloat(item.ratio-target) < absFloat(best.ratio-target) {
			best = item
		}
	}
	return best.value
}

// 单次查询只读取已创建的上游任务；失败、超时或断连时绝不回到创建接口重新提交。
func queryMiniMaxH3Task(ctx context.Context, input canvasGenerationInput, id string) (map[string]interface{}, string, error) {
	var payload map[string]interface{}
	if err := miniMaxH3JSON(withProviderRequestKind(ctx, "poll"), input.Config, http.MethodGet, "/query/video_generation/"+url.PathEscape(id), nil, &payload); err != nil {
		return nil, "", err
	}
	state, ok := payload["task"].(map[string]interface{})
	if !ok {
		if err := miniMaxH3PayloadError(payload); err != nil {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("MiniMax H3 任务 %s 查询响应缺少 task", id)
	}
	status := strings.ToLower(strings.TrimSpace(stringField(state, "status")))
	switch status {
	case "succeeded":
		content, _ := state["content"].(map[string]interface{})
		videoURL := strings.TrimSpace(stringField(content, "url"))
		if !isPublicMediaURL(videoURL) {
			return nil, status, fmt.Errorf("MiniMax H3 任务 %s 已成功但没有返回视频地址", id)
		}
		data, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), videoURL)
		if err != nil {
			return nil, status, fmt.Errorf("MiniMax H3 视频结果下载失败（任务 %s）：%w", id, err)
		}
		mimeType = normalizedMediaMimeType(mimeType, data)
		return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, status, nil
	case "failed":
		return nil, status, &miniMaxH3TaskFailure{TaskID: id, Reason: miniMaxH3FailureReason(state)}
	case "cancelled", "canceled":
		return nil, status, fmt.Errorf("MiniMax H3 任务 %s 已取消", id)
	case "queued", "running", "":
		return nil, status, nil
	default:
		return nil, status, fmt.Errorf("MiniMax H3 任务 %s 返回未知状态：%s", id, status)
	}
}

func miniMaxH3TaskID(payload map[string]interface{}) string {
	if id := strings.TrimSpace(stringField(payload, "task_id")); id != "" {
		return id
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return strings.TrimSpace(stringField(data, "task_id"))
	}
	return ""
}

func miniMaxH3PayloadError(payload map[string]interface{}) error {
	if miniMaxH3TaskID(payload) != "" {
		return nil
	}
	if _, ok := payload["task"].(map[string]interface{}); ok {
		return nil
	}
	code, message := providerFailureDetails(payload)
	if code == "" && message == "" {
		return nil
	}
	if code == "0" || strings.EqualFold(code, "ok") || strings.EqualFold(code, "success") {
		return nil
	}
	return fmt.Errorf("MiniMax H3 接口返回失败（%s）：%s", defaultString(code, "unknown"), defaultString(message, "上游返回失败"))
}

func miniMaxH3FailureReason(state map[string]interface{}) string {
	if failure, ok := state["error"].(map[string]interface{}); ok {
		code, message := providerFailureDetails(failure)
		return firstNonEmptyString(message, code)
	}
	return "上游返回 failed"
}

func isTransientMiniMaxH3QueryError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr providerHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusRequestTimeout || httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= http.StatusInternalServerError
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func logMiniMaxH3QueryRetry(ctx context.Context, providerTaskID string, retry int, err error) {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil || metadata.TaskID == "" {
		return
	}
	payload := fmt.Sprintf("供应商任务 %s，第 %d/%d 次重试：%s", providerTaskID, retry, miniMaxH3MaxQueryRetries, safeProviderLogError(err))
	_ = metadata.Service.log(metadata.UserID, metadata.TaskID, "warn", "MiniMax H3 上游任务查询失败，10 秒后重试", payload)
}

func miniMaxH3JSON(ctx context.Context, config providerConfig, method string, path string, body interface{}, target interface{}) error {
	data := []byte(nil)
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, miniMaxH3URL(config.BaseURL, path), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	ApplyOutboundHeaders(req, config.Headers)
	return doJSON(req, target)
}

func miniMaxH3URL(baseURL string, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	lowerBase := strings.ToLower(base)
	for _, suffix := range []string{"/v2/video_generation", "/v2/query/video_generation"} {
		if strings.HasSuffix(lowerBase, suffix) {
			base = base[:len(base)-len(suffix)]
			lowerBase = strings.ToLower(base)
			break
		}
	}
	if strings.HasSuffix(lowerBase, "/v2") {
		base = base[:len(base)-len("/v2")]
	}
	return base + "/v2/" + strings.TrimLeft(path, "/")
}
