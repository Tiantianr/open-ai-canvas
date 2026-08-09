package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	asyncVideoGenerationsPollInterval           = 5 * time.Second
	asyncVideoGenerationsRetryInterval          = 10 * time.Second
	asyncVideoGenerationsMaxQueryRetries        = 3
	asyncVideoGenerationsMaxImageBytes    int64 = 10 << 20
	asyncVideoGenerationsDefaultMaxImages       = 12
)

var asyncVideoGenerationsModelDurations = map[string]int{
	"sora-2-4s":             4,
	"sora-2-8s":             8,
	"sora-2-12s":            12,
	"seedance-1.5-pro-5s":   5,
	"seedance-1.5-pro-10s":  10,
	"seedance-1.5-pro-12s":  12,
	"seedance-2.0":          8,
	"seedance-2.0-mini":     8,
	"seedance-2.0-fast-5s":  5,
	"seedance-2.0-fast-10s": 10,
	"seedance-2.0-fast-15s": 15,
}

var asyncVideoGenerationsModelMaxImages = map[string]int{
	"sora-2-4s":             1,
	"sora-2-8s":             1,
	"sora-2-12s":            1,
	"seedance-1.5-pro-5s":   1,
	"seedance-1.5-pro-10s":  1,
	"seedance-1.5-pro-12s":  1,
	"seedance-2.0":          asyncVideoGenerationsDefaultMaxImages,
	"seedance-2.0-mini":     asyncVideoGenerationsDefaultMaxImages,
	"seedance-2.0-fast-5s":  asyncVideoGenerationsDefaultMaxImages,
	"seedance-2.0-fast-10s": asyncVideoGenerationsDefaultMaxImages,
	"seedance-2.0-fast-15s": asyncVideoGenerationsDefaultMaxImages,
}

type asyncVideoGenerationsResponseError struct {
	Code    string
	Message string
}

func (e *asyncVideoGenerationsResponseError) Error() string {
	return fmt.Sprintf("异步视频任务查询失败（%s）：%s", defaultString(e.Code, "unknown"), defaultString(e.Message, "上游返回失败"))
}

// asyncVideoGenerationsTaskFailure 只表示供应商明确给出的 failed 终态。
// 该协议约定 failed 不计费，和超时、断连等不确定异常必须区分开。
type asyncVideoGenerationsTaskFailure struct {
	TaskID string
	Reason string
}

func (e *asyncVideoGenerationsTaskFailure) Error() string {
	return fmt.Sprintf("异步视频生成失败（任务 %s）：%s", e.TaskID, defaultString(e.Reason, "上游返回 failed"))
}

func runAsyncVideoGenerationsTask(ctx context.Context, input canvasGenerationInput) (map[string]interface{}, error) {
	ctx = withAsyncVideoGenerationsDuration(ctx, input.Config.Model)
	id := resumedProviderRequestID(ctx)
	if id == "" {
		body, err := asyncVideoGenerationsBody(input)
		if err != nil {
			return nil, err
		}
		var created map[string]interface{}
		if err := postJSON(ctx, input.Config, "/videos/generations", body, &created); err != nil {
			return nil, err
		}
		if err := asyncVideoGenerationsPayloadError(created); err != nil {
			return nil, err
		}
		id = asyncVideoGenerationsTaskID(created)
	}
	if id == "" {
		return nil, errors.New("异步视频接口没有返回任务 ID")
	}

	consecutiveQueryFailures := 0
	for deadline := providerPollingDeadline(ctx); time.Now().Before(deadline); {
		result, _, err := queryAsyncVideoGenerationsTask(ctx, input, id)
		if err != nil {
			if !isTransientAsyncVideoGenerationsQueryError(err) || consecutiveQueryFailures >= asyncVideoGenerationsMaxQueryRetries {
				return nil, err
			}
			consecutiveQueryFailures++
			logAsyncVideoGenerationsQueryRetry(ctx, id, consecutiveQueryFailures, err)
			if err := sleepContext(ctx, asyncVideoGenerationsRetryInterval); err != nil {
				return nil, err
			}
			continue
		}
		consecutiveQueryFailures = 0
		if result != nil {
			return result, nil
		}
		if err := sleepContext(ctx, asyncVideoGenerationsPollInterval); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("异步视频生成超时（任务 %s）", id)
}

func asyncVideoGenerationsBody(input canvasGenerationInput) (map[string]interface{}, error) {
	if len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
		return nil, errors.New("异步视频接口不支持参考视频或参考音频")
	}
	if metadataString(input.Metadata, "videoEndFrameNodeId") != "" {
		return nil, errors.New("异步视频接口不支持尾帧，请移除尾帧选择")
	}
	references, err := asyncVideoGenerationsOrderedReferenceImages(input)
	if err != nil {
		return nil, err
	}
	if limit := asyncVideoGenerationsMaxReferenceImages(input.Config.Model); len(references) > limit {
		return nil, fmt.Errorf("异步视频模型 %s 最多支持 %d 张参考图，当前连接了 %d 张", input.Config.Model, limit, len(references))
	}
	body := map[string]interface{}{
		"model":  input.Config.Model,
		"prompt": strings.TrimSpace(input.Prompt),
	}
	if len(references) > 0 {
		// images[0] 是供应商定义的首图；其余图片保留画布连接顺序。
		images := make([]string, 0, len(references))
		for index, reference := range references {
			image, err := asyncVideoGenerationsReferenceImage(reference)
			if err != nil {
				return nil, fmt.Errorf("第 %d 张异步视频参考图无效：%w", index+1, err)
			}
			images = append(images, image)
		}
		body["images"] = images
	}
	return body, nil
}

func asyncVideoGenerationsOrderedReferenceImages(input canvasGenerationInput) ([]providerMedia, error) {
	startFrameID := metadataString(input.Metadata, "videoStartFrameNodeId")
	if startFrameID == "" {
		return input.ReferenceImages, nil
	}
	for index, reference := range input.ReferenceImages {
		if reference.ID != startFrameID {
			continue
		}
		ordered := make([]providerMedia, 0, len(input.ReferenceImages))
		ordered = append(ordered, reference)
		ordered = append(ordered, input.ReferenceImages[:index]...)
		ordered = append(ordered, input.ReferenceImages[index+1:]...)
		return ordered, nil
	}
	return nil, errors.New("异步视频首帧未连接或不可用，请重新选择后再生成")
}

func asyncVideoGenerationsReferenceImage(media providerMedia) (string, error) {
	if value := strings.TrimSpace(media.URL); strings.HasPrefix(strings.ToLower(value), "https://") {
		return value, nil
	}
	value := strings.TrimSpace(media.DataURL)
	if strings.HasPrefix(value, "data:") {
		raw, _, err := mediaBytes(media)
		if err != nil {
			return "", fmt.Errorf("异步视频参考图读取失败：%w", err)
		}
		if int64(len(raw)) > asyncVideoGenerationsMaxImageBytes {
			return "", errors.New("异步视频参考图不能超过 10MB")
		}
		return value, nil
	}
	return "", errors.New("异步视频参考图需要 data URL 或 HTTPS URL")
}

func asyncVideoGenerationsMaxReferenceImages(modelName string) int {
	if limit, ok := asyncVideoGenerationsModelMaxImages[strings.ToLower(strings.TrimSpace(modelName))]; ok {
		return limit
	}
	return asyncVideoGenerationsDefaultMaxImages
}

// 单次查询只读取既有上游任务，不创建新任务；自动轮询和人工恢复共用这条边界。
func queryAsyncVideoGenerationsTask(ctx context.Context, input canvasGenerationInput, id string) (map[string]interface{}, string, error) {
	var payload map[string]interface{}
	if err := getAsyncVideoGenerationsJSON(ctx, input.Config, "/videos/generations/"+id, &payload); err != nil {
		return nil, "", err
	}
	state := payload
	if data, ok := payload["data"].(map[string]interface{}); ok {
		state = data
	}
	if err := asyncVideoGenerationsPayloadError(state); err != nil {
		return nil, "", err
	}
	status := strings.ToLower(strings.TrimSpace(stringField(state, "status")))
	switch status {
	case "succeeded", "completed", "success":
		videoURL := asyncVideoGenerationsResultURL(state)
		if videoURL == "" {
			return nil, status, fmt.Errorf("异步视频任务 %s 已成功但没有返回视频地址", id)
		}
		data, mimeType, err := getExternalBinary(withProviderRequestKind(ctx, "download"), videoURL)
		if err != nil {
			return nil, status, fmt.Errorf("异步视频结果下载失败（任务 %s）：%w", id, err)
		}
		mimeType = normalizedMediaMimeType(mimeType, data)
		return map[string]interface{}{"mode": "video", "video": map[string]interface{}{"dataUrl": dataURL(mimeType, data), "mimeType": mimeType}}, status, nil
	case "failed":
		return nil, status, &asyncVideoGenerationsTaskFailure{TaskID: id, Reason: asyncVideoGenerationsFailureReason(state)}
	case "cancelled", "canceled", "expired":
		return nil, status, fmt.Errorf("异步视频任务 %s 已%s", id, status)
	case "queued", "processing", "running", "pending", "in_progress", "":
		return nil, status, nil
	default:
		return nil, status, fmt.Errorf("异步视频任务 %s 返回未知状态：%s", id, status)
	}
}

func getAsyncVideoGenerationsJSON(ctx context.Context, config providerConfig, path string, target interface{}) error {
	data, mimeType, err := getBinary(ctx, config, path)
	if err != nil {
		return err
	}
	if !strings.Contains(mimeType, "json") && !json.Valid(data) {
		return fmt.Errorf("异步视频任务查询返回非 JSON 内容：%s", mimeType)
	}
	return json.Unmarshal(data, target)
}

func asyncVideoGenerationsTaskID(payload map[string]interface{}) string {
	state := payload
	if data, ok := payload["data"].(map[string]interface{}); ok {
		state = data
	}
	return firstNonEmptyString(stringField(state, "id"), stringField(state, "task_id"), stringField(state, "request_id"))
}

func asyncVideoGenerationsPayloadError(payload map[string]interface{}) error {
	if status := strings.TrimSpace(stringField(payload, "status")); status != "" {
		return nil
	}
	code, message := providerFailureDetails(payload)
	if code == "" && message == "" {
		return nil
	}
	if code == "0" || strings.EqualFold(code, "ok") || strings.EqualFold(code, "success") {
		return nil
	}
	return &asyncVideoGenerationsResponseError{Code: code, Message: message}
}

func asyncVideoGenerationsResultURL(state map[string]interface{}) string {
	if value := strings.TrimSpace(stringField(state, "url")); isPublicMediaURL(value) {
		return value
	}
	results, _ := state["results"].([]interface{})
	for _, item := range results {
		result, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if value := strings.TrimSpace(stringField(result, "url")); isPublicMediaURL(value) {
			return value
		}
	}
	return ""
}

func asyncVideoGenerationsFailureReason(state map[string]interface{}) string {
	code, message := providerFailureDetails(state)
	return firstNonEmptyString(stringField(state, "fail_reason"), message, code)
}

func isTransientAsyncVideoGenerationsQueryError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var responseErr *asyncVideoGenerationsResponseError
	if errors.As(err, &responseErr) {
		return strings.EqualFold(responseErr.Code, "rate_limited") || strings.EqualFold(responseErr.Code, "service_unavailable")
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

func logAsyncVideoGenerationsQueryRetry(ctx context.Context, providerTaskID string, retry int, err error) {
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok || metadata.Service == nil || metadata.TaskID == "" {
		return
	}
	payload := fmt.Sprintf("供应商任务 %s，第 %d/%d 次重试：%s", providerTaskID, retry, asyncVideoGenerationsMaxQueryRetries, safeProviderLogError(err))
	_ = metadata.Service.log(metadata.UserID, metadata.TaskID, "warn", "上游异步视频任务查询失败，10 秒后重试", payload)
}

func withAsyncVideoGenerationsDuration(ctx context.Context, modelName string) context.Context {
	seconds := asyncVideoGenerationsModelSeconds(modelName)
	if seconds <= 0 {
		return ctx
	}
	metadata, ok := ctx.Value(providerAnalyticsKey{}).(providerAnalyticsContext)
	if !ok {
		return ctx
	}
	metadata.VideoSeconds = seconds
	return context.WithValue(ctx, providerAnalyticsKey{}, metadata)
}

func asyncVideoGenerationsModelSeconds(modelName string) int {
	return asyncVideoGenerationsModelDurations[strings.ToLower(strings.TrimSpace(modelName))]
}
