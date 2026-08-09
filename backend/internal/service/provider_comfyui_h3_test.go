package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const comfyUIH3TestImage = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestComfyUIH3WorkflowBuildsStaticReferenceGraph(t *testing.T) {
	workflow := comfyUIH3Workflow("Use <Picture 1> and <Picture 2>.", []string{"canvas/one.png", "canvas/two.png"}, 1344, 768, 124, 42, false)
	h3 := workflow["136"].(map[string]interface{})
	inputs := h3["inputs"].(map[string]interface{})
	if inputs["clip"].([]interface{})[0] != "128" || inputs["vae"].([]interface{})[0] != "119" || inputs["audio_vae"].([]interface{})[0] != "120" {
		t.Fatalf("H3 loader links = %#v", inputs)
	}
	if inputs["ref_images.ref_image_0"].([]interface{})[0] != "137" || inputs["ref_images.ref_image_1"].([]interface{})[0] != "138" {
		t.Fatalf("reference links = %#v", inputs)
	}
	if workflow["137"].(map[string]interface{})["inputs"].(map[string]interface{})["image"] != "canvas/one.png" {
		t.Fatalf("first LoadImage = %#v", workflow["137"])
	}
	createVideo := workflow["130"].(map[string]interface{})["inputs"].(map[string]interface{})
	if _, exists := createVideo["audio"]; exists {
		t.Fatalf("CreateVideo unexpectedly includes audio: %#v", createVideo)
	}
	if got := comfyUIH3FrameCount(5); got != 124 || got%17 != 5 {
		t.Fatalf("comfyUIH3FrameCount(5) = %d", got)
	}
	if width, height := comfyUIH3Dimensions("16:9", "768P", providerMedia{}); width != 1344 || height != 768 {
		t.Fatalf("768P landscape dimensions = %dx%d", width, height)
	}
	if width, height := comfyUIH3Dimensions("9:16", "768P", providerMedia{}); width != 768 || height != 1344 {
		t.Fatalf("768P portrait dimensions = %dx%d", width, height)
	}
	profile := DefaultModelCapabilityConfig("comfyui-h3").Video
	if profile.References.MaxImages != 9 || profile.References.MaxVideos != 0 || profile.Duration.Min != 5 || profile.DefaultOperation != "image_to_video" || !profile.GenerateAudio.Supported {
		t.Fatalf("ComfyUI H3 capability = %#v", profile)
	}
	_, err := comfyUIH3PromptBody(context.Background(), canvasGenerationInput{
		Prompt:          "test",
		Config:          providerConfig{Model: "MiniMax-H3-R2V", VideoSeconds: "4"},
		ReferenceImages: []providerMedia{{DataURL: comfyUIH3TestImage}},
	})
	if err == nil || !strings.Contains(err.Error(), "5 到 15 秒") {
		t.Fatalf("4-second validation error = %v", err)
	}
}

func TestRunComfyUIH3TaskUploadsSubmitsPollsAndDownloads(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 5)
	uploads := 0
	var submitted map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q", got)
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /upload/image":
			file, _, err := r.FormFile("image")
			if err != nil {
				t.Fatalf("read uploaded image: %v", err)
			}
			data, _ := io.ReadAll(file)
			_ = file.Close()
			if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
				t.Fatalf("uploaded data is not PNG: %x", data)
			}
			uploads++
			writeComfyUIH3TestJSON(w, map[string]interface{}{"name": fmt.Sprintf("ref-%d.png", uploads), "subfolder": "canvas", "type": "input"})
		case "POST /prompt":
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Fatalf("decode prompt: %v", err)
			}
			writeComfyUIH3TestJSON(w, map[string]interface{}{"prompt_id": "prompt-1", "number": 1, "node_errors": map[string]interface{}{}})
		case "GET /history/prompt-1":
			writeComfyUIH3TestJSON(w, comfyUIH3TestHistory("prompt-1"))
		case "GET /view":
			if r.URL.Query().Get("filename") != "result.mp4" || r.URL.Query().Get("subfolder") != "Canvas" || r.URL.Query().Get("type") != "output" {
				t.Errorf("view query = %q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt:          "Use <Picture 1> and <Picture 2>.",
		Config:          providerConfig{BaseURL: server.URL, Model: "MiniMax-H3-R2V", InterfaceType: "comfyui-h3", VideoSeconds: "5", Size: "16:9", VQuality: "768P", VideoGenerateAudio: "true"},
		ReferenceImages: []providerMedia{{ID: "one", DataURL: comfyUIH3TestImage}, {ID: "two", DataURL: comfyUIH3TestImage}},
		Metadata:        map[string]interface{}{"videoEditOperation": "image_to_video"},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video := result["video"].(map[string]interface{})
	if result["mode"] != "video" || video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("result = %#v", result)
	}
	graph := submitted["prompt"].(map[string]interface{})
	h3Inputs := graph["136"].(map[string]interface{})["inputs"].(map[string]interface{})
	if h3Inputs["width"] != float64(1344) || h3Inputs["height"] != float64(768) || h3Inputs["length"] != float64(124) {
		t.Fatalf("H3 dimensions/length = %#v", h3Inputs)
	}
	if got, want := strings.Join(paths, ","), "POST /upload/image,POST /upload/image,POST /prompt,GET /history/prompt-1,GET /view"; got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestRunComfyUIH3TaskResumesWithoutSubmission(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /history/prompt-existing":
			writeComfyUIH3TestJSON(w, comfyUIH3TestHistory("prompt-existing"))
		case "GET /view":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			t.Fatalf("recovery unexpectedly called %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	ctx := context.WithValue(context.Background(), providerAnalyticsKey{}, providerAnalyticsContext{ProviderRequestID: "prompt-existing"})
	_, err := runComfyUIH3Task(ctx, canvasGenerationInput{Config: providerConfig{BaseURL: server.URL, Model: "MiniMax-H3-R2V", InterfaceType: "comfyui-h3"}})
	if err != nil {
		t.Fatalf("runComfyUIH3Task() error = %v", err)
	}
	if got, want := strings.Join(paths, ","), "GET /history/prompt-existing,GET /view"; got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestRunComfyUIH3TaskDoesNotResubmitUncertainPrompt(t *testing.T) {
	ctx := context.WithValue(context.Background(), providerAnalyticsKey{}, providerAnalyticsContext{PollStage: "submitting"})
	_, err := runComfyUIH3Task(ctx, canvasGenerationInput{})
	if err == nil || !strings.Contains(err.Error(), "停止自动重提") {
		t.Fatalf("runComfyUIH3Task() error = %v", err)
	}
}

func comfyUIH3TestHistory(promptID string) map[string]interface{} {
	return map[string]interface{}{promptID: map[string]interface{}{
		"status":  map[string]interface{}{"status_str": "success", "completed": true, "messages": []interface{}{}},
		"outputs": map[string]interface{}{"92": map[string]interface{}{"videos": []interface{}{map[string]interface{}{"filename": "result.mp4", "subfolder": "Canvas", "type": "output"}}}},
	}}
}

func writeComfyUIH3TestJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
