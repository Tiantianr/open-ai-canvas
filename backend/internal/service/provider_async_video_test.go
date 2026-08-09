package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunAsyncVideoGenerationsTaskPollsAndDownloadsResult(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/videos/generations":
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Errorf("Authorization = %q", got)
			}
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			images, ok := body["images"].([]interface{})
			if body["model"] != "sora-2-8s" || body["prompt"] != "make a short ocean wave video" || !ok || len(images) != 1 || images[0] != testReferenceImageDataURL {
				t.Errorf("body = %#v", body)
			}
			for _, key := range []string{"duration", "seconds", "aspect_ratio", "resolution", "generate_audio", "image", "ref_images"} {
				if _, exists := body[key]; exists {
					t.Errorf("body unexpectedly contains %q: %#v", key, body)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"task-async","status":"queued"}`))
		case "GET /v1/videos/generations/task-async":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"task-async","status":"succeeded","url":"` + server.URL + `/video.mp4"}`))
		case "GET /video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make a short ocean wave video",
		Config: providerConfig{
			BaseURL:       server.URL,
			APIKey:        "test-key",
			Model:         "sora-2-8s",
			InterfaceType: "async-video-generations",
			VideoSeconds:  "15",
			Size:          "9:16",
			VQuality:      "1080",
		},
		ReferenceImages: []providerMedia{{ID: "first-frame", DataURL: testReferenceImageDataURL}},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video := result["video"].(map[string]interface{})
	if video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", video)
	}
	if got, want := strings.Join(paths, ","), "POST /v1/videos/generations,GET /v1/videos/generations/task-async,GET /video.mp4"; got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestAsyncVideoGenerationsUsesReferenceImageArray(t *testing.T) {
	body, err := asyncVideoGenerationsBody(canvasGenerationInput{
		Config: providerConfig{Model: "seedance-2.0-fast-5s"},
		ReferenceImages: []providerMedia{
			{ID: "reference", DataURL: testReferenceImageDataURL},
			{ID: "first-frame", URL: "https://example.com/first.png", DataURL: testReferenceImageDataURL},
		},
		Metadata: map[string]interface{}{"videoStartFrameNodeId": "first-frame"},
	})
	if err != nil {
		t.Fatalf("asyncVideoGenerationsBody() error = %v", err)
	}
	images, ok := body["images"].([]string)
	if !ok || len(images) != 2 || images[0] != "https://example.com/first.png" || images[1] != testReferenceImageDataURL {
		t.Fatalf("images = %#v", body["images"])
	}
	if _, exists := body["image"]; exists {
		t.Fatalf("body unexpectedly contains image: %#v", body)
	}

	_, err = asyncVideoGenerationsBody(canvasGenerationInput{
		Config:          providerConfig{Model: "sora-2-4s"},
		ReferenceVideos: []providerMedia{{URL: "https://example.com/reference.mp4"}},
	})
	if err == nil || !strings.Contains(err.Error(), "不支持参考视频") {
		t.Fatalf("asyncVideoGenerationsBody() error = %v", err)
	}
}

func TestAsyncVideoGenerationsValidatesReferenceImageContract(t *testing.T) {
	images := []providerMedia{{ID: "first", DataURL: testReferenceImageDataURL}, {ID: "second", DataURL: testReferenceImageDataURL}}
	_, err := asyncVideoGenerationsBody(canvasGenerationInput{Config: providerConfig{Model: "sora-2-4s"}, ReferenceImages: images})
	if err == nil || !strings.Contains(err.Error(), "最多支持 1 张") {
		t.Fatalf("single-image model error = %v", err)
	}

	_, err = asyncVideoGenerationsBody(canvasGenerationInput{
		Config:          providerConfig{Model: "seedance-2.0"},
		ReferenceImages: images,
		Metadata:        map[string]interface{}{"videoEndFrameNodeId": "second"},
	})
	if err == nil || !strings.Contains(err.Error(), "不支持尾帧") {
		t.Fatalf("end-frame error = %v", err)
	}

	if got := asyncVideoGenerationsMaxReferenceImages("unknown-video"); got != 12 {
		t.Fatalf("asyncVideoGenerationsMaxReferenceImages() = %d, want 12", got)
	}
	if got := asyncVideoGenerationsMaxReferenceImages("seedance-1.5-pro-5s"); got != 1 {
		t.Fatalf("asyncVideoGenerationsMaxReferenceImages() = %d, want 1", got)
	}
	if err := asyncVideoGenerationsPayloadError(map[string]interface{}{"code": "API_KEY_REQUIRED", "message": "missing key"}); err == nil || !strings.Contains(err.Error(), "API_KEY_REQUIRED") {
		t.Fatalf("asyncVideoGenerationsPayloadError() = %v", err)
	}
}

func TestAsyncVideoGenerationsFailedTaskIsConfirmedNoCharge(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/videos/generations/task-failed" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task-failed","status":"failed","fail_reason":"content rejected"}`))
	}))
	defer server.Close()

	_, status, err := queryAsyncVideoGenerationsTask(context.Background(), canvasGenerationInput{Config: providerConfig{BaseURL: server.URL, APIKey: "test-key"}}, "task-failed")
	if status != "failed" {
		t.Fatalf("status = %q", status)
	}
	var failure *asyncVideoGenerationsTaskFailure
	if !errors.As(err, &failure) || failure.Reason != "content rejected" {
		t.Fatalf("error = %#v", err)
	}
	if !providerConfirmedNoChargeFailure(err) {
		t.Fatal("providerConfirmedNoChargeFailure() = false")
	}
}

func TestAsyncVideoGenerationsModelSeconds(t *testing.T) {
	if got := asyncVideoGenerationsModelSeconds("sora-2-12s"); got != 12 {
		t.Fatalf("asyncVideoGenerationsModelSeconds() = %d", got)
	}
	if got := asyncVideoGenerationsModelSeconds("seedance-2.0-mini"); got != 8 {
		t.Fatalf("asyncVideoGenerationsModelSeconds() = %d", got)
	}
	if got := asyncVideoGenerationsModelSeconds("unknown-video"); got != 0 {
		t.Fatalf("asyncVideoGenerationsModelSeconds() = %d", got)
	}
}
