package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunMiniMaxH3TaskPollsAndDownloadsResult(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v2/video_generation":
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Errorf("Authorization = %q", got)
			}
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			content, ok := body["content"].([]interface{})
			if !ok || len(content) != 2 || body["model"] != "MiniMax-H3" || body["resolution"] != "2K" || body["duration"] != float64(5) || body["ratio"] != "adaptive" {
				t.Errorf("body = %#v", body)
			}
			frame, _ := content[1].(map[string]interface{})
			if frame["role"] != "first_frame" {
				t.Errorf("frame = %#v", frame)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task_id":"task-minimax"}`))
		case "GET /v2/query/video_generation/task-minimax":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task":{"id":"task-minimax","status":"succeeded","content":{"url":"` + server.URL + `/video.mp4"}}}`))
		case "GET /video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make the ocean move",
		Config: providerConfig{
			BaseURL:       server.URL,
			APIKey:        "test-key",
			Model:         "MiniMax-H3",
			InterfaceType: "minimax-h3",
			VideoSeconds:  "5",
			VQuality:      "2K",
			Size:          "9:16",
		},
		ReferenceImages: []providerMedia{{ID: "first", DataURL: testReferenceImageDataURL}},
		Metadata:        map[string]interface{}{"videoStartFrameNodeId": "first"},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video := result["video"].(map[string]interface{})
	if video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", video)
	}
	if got, want := strings.Join(paths, ","), "POST /v2/video_generation,GET /v2/query/video_generation/task-minimax,GET /video.mp4"; got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestMiniMaxH3BodyValidatesModesAndLimits(t *testing.T) {
	body, err := miniMaxH3Body(canvasGenerationInput{
		Prompt: "make a transition",
		Config: providerConfig{Model: "MiniMax-H3", VideoSeconds: "6", VQuality: "720", Size: "9:16"},
		ReferenceImages: []providerMedia{
			{ID: "first", URL: "https://example.com/first.png"},
			{ID: "last", URL: "https://example.com/last.png"},
		},
		Metadata: map[string]interface{}{"videoStartFrameNodeId": "first", "videoEndFrameNodeId": "last"},
	})
	if err != nil {
		t.Fatalf("miniMaxH3Body() error = %v", err)
	}
	content := body["content"].([]map[string]interface{})
	if body["resolution"] != "768P" || body["ratio"] != "adaptive" || content[1]["role"] != "first_frame" || content[2]["role"] != "last_frame" {
		t.Fatalf("body = %#v", body)
	}

	referenceBody, err := miniMaxH3Body(canvasGenerationInput{
		Prompt:          "match the supplied references",
		Config:          providerConfig{Model: "MiniMax-H3", VideoSeconds: "5", Size: "16:9"},
		ReferenceImages: []providerMedia{{URL: "https://example.com/reference.png"}},
		ReferenceVideos: []providerMedia{{URL: "https://example.com/reference.mp4"}},
		ReferenceAudios: []providerMedia{{URL: "https://example.com/reference.mp3"}},
	})
	if err != nil {
		t.Fatalf("reference miniMaxH3Body() error = %v", err)
	}
	referenceContent := referenceBody["content"].([]map[string]interface{})
	if referenceBody["ratio"] != "16:9" || referenceContent[1]["role"] != "reference_image" || referenceContent[2]["role"] != "reference_video" || referenceContent[3]["role"] != "reference_audio" {
		t.Fatalf("reference body = %#v", referenceBody)
	}

	privateResourceBody, err := miniMaxH3Body(canvasGenerationInput{
		Prompt: "use the private reference",
		Config: providerConfig{Model: "MiniMax-H3", VideoSeconds: "5", Size: "16:9"},
		ReferenceImages: []providerMedia{{
			URL:        "https://example.com/stale-signed-reference.png",
			DataURL:    testReferenceImageDataURL,
			StorageKey: "resource:private-reference",
		}},
	})
	if err != nil {
		t.Fatalf("private-resource miniMaxH3Body() error = %v", err)
	}
	privateContent := privateResourceBody["content"].([]map[string]interface{})
	privateImage := privateContent[1]["image_url"].(map[string]interface{})
	if privateImage["url"] != testReferenceImageDataURL {
		t.Fatalf("private resource URL = %#v", privateImage["url"])
	}

	_, err = miniMaxH3Body(canvasGenerationInput{
		Prompt:          "make a transition",
		Config:          providerConfig{Model: "MiniMax-H3", VideoSeconds: "5"},
		ReferenceImages: []providerMedia{{ID: "last", DataURL: testReferenceImageDataURL}},
		Metadata:        map[string]interface{}{"videoEndFrameNodeId": "last"},
	})
	if err == nil || !strings.Contains(err.Error(), "尾帧必须同时指定首帧") {
		t.Fatalf("end-frame error = %v", err)
	}

	_, err = miniMaxH3Body(canvasGenerationInput{
		Prompt: "make a transition",
		Config: providerConfig{Model: "MiniMax-H3", VideoSeconds: "5"},
		ReferenceImages: []providerMedia{
			{ID: "first", DataURL: testReferenceImageDataURL},
			{ID: "reference", DataURL: testReferenceImageDataURL},
		},
		Metadata: map[string]interface{}{"videoStartFrameNodeId": "first"},
	})
	if err == nil || !strings.Contains(err.Error(), "不能混用") {
		t.Fatalf("mixed-mode error = %v", err)
	}

	_, err = miniMaxH3Body(canvasGenerationInput{
		Prompt:          "make a transition",
		Config:          providerConfig{Model: "MiniMax-H3", VideoSeconds: "5"},
		ReferenceAudios: []providerMedia{{DataURL: "data:audio/mp3;base64,AA=="}},
	})
	if err == nil || !strings.Contains(err.Error(), "参考音频需要") {
		t.Fatalf("audio-only error = %v", err)
	}

	for name, audios := range map[string][]providerMedia{
		"too short":      {{URL: "https://example.com/short.mp3", DurationMs: 1000}},
		"total too long": {{URL: "https://example.com/one.mp3", DurationMs: 8000}, {URL: "https://example.com/two.mp3", DurationMs: 8000}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := miniMaxH3Body(canvasGenerationInput{
				Prompt:          "match the supplied voice",
				Config:          providerConfig{Model: "MiniMax-H3", VideoSeconds: "5"},
				ReferenceImages: []providerMedia{{URL: "https://example.com/reference.png"}},
				ReferenceAudios: audios,
			})
			if err == nil {
				t.Fatal("invalid reference audio was accepted")
			}
		})
	}

	if got, want := miniMaxH3URL("https://metaso.cn/api/minimax/v2/video_generation", "/query/video_generation/task-1"), "https://metaso.cn/api/minimax/v2/query/video_generation/task-1"; got != want {
		t.Fatalf("miniMaxH3URL() = %q, want %q", got, want)
	}
}

func TestMiniMaxH3MediaURLCropsOddImageDimension(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 258, 257))); err != nil {
		t.Fatal(err)
	}
	value, err := miniMaxH3MediaURL(providerMedia{
		Type:       "image/png",
		DataURL:    dataURL("image/png", encoded.Bytes()),
		StorageKey: "resource:odd-image",
	}, "image", miniMaxH3MaxImageBytes, "参考图")
	if err != nil {
		t.Fatalf("miniMaxH3MediaURL() error = %v", err)
	}
	raw, _, err := mediaBytes(providerMedia{DataURL: value})
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width != 258 || config.Height != 256 {
		t.Fatalf("normalized dimensions = %dx%d", config.Width, config.Height)
	}
}

func TestMiniMaxH3FailedTaskRequiresBillingReview(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/query/video_generation/task-failed" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task":{"id":"task-failed","status":"failed","error":{"code":"1026","message":"content rejected"}}}`))
	}))
	defer server.Close()

	_, status, err := queryMiniMaxH3Task(context.Background(), canvasGenerationInput{Config: providerConfig{BaseURL: server.URL, APIKey: "test-key"}}, "task-failed")
	if status != "failed" {
		t.Fatalf("status = %q", status)
	}
	var failure *miniMaxH3TaskFailure
	if !errors.As(err, &failure) || failure.Reason != "content rejected" {
		t.Fatalf("error = %#v", err)
	}
	if providerConfirmedNoChargeFailure(err) {
		t.Fatal("providerConfirmedNoChargeFailure() = true")
	}
}
