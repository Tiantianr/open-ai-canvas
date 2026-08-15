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
const comfyUIH3TestAudio = "data:audio/wav;base64,UklGRiQAAABXQVZFZm10IBAAAAABAAEAQB8AAIA+AAACABAAZGF0YQAAAAA="

func TestComfyUIH3WorkflowBuildsStaticReferenceGraph(t *testing.T) {
	workflow := comfyUIH3Workflow("Use <Picture 1>, <Picture 2>, and <Audio 1>.", []string{"canvas/one.png", "canvas/two.png"}, []string{"canvas/voice.wav"}, 1344, 768, 124, 42, false)
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
	if inputs["ref_audios.ref_audio_0"].([]interface{})[0] != "146" || workflow["146"].(map[string]interface{})["inputs"].(map[string]interface{})["audio"] != "canvas/voice.wav" {
		t.Fatalf("reference audio graph: inputs=%#v load=%#v", inputs, workflow["146"])
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
	if profile.References.MaxImages != 9 || profile.References.MaxVideos != 0 || profile.References.MaxAudios != 3 || profile.References.MaxAudioBytes != 15*1024*1024 || profile.References.MaxAudioDuration != 15 || profile.Duration.Min != 5 || profile.DefaultOperation != "image_to_video" || !profile.GenerateAudio.Supported {
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

func TestComfyUIH3PDDWorkflowUsesFixedContract(t *testing.T) {
	workflow := comfyUIH3PDDWorkflow("Use <Picture 1>, <Picture 2>, and <Audio 1>.", []string{"canvas/one.png", "canvas/two.png"}, []string{"canvas/voice.wav"}, 864, 480, 124, 42, true)
	if len(workflow) != 21 {
		t.Fatalf("PDD graph node count = %d", len(workflow))
	}
	h3Inputs := workflow["7"].(map[string]interface{})["inputs"].(map[string]interface{})
	if h3Inputs["width"] != 864 || h3Inputs["height"] != 480 || h3Inputs["length"] != 124 || h3Inputs["ref_images.ref_image_1"].([]interface{})[0] != "101" {
		t.Fatalf("PDD H3 inputs = %#v", h3Inputs)
	}
	if h3Inputs["ref_audios.ref_audio_0"].([]interface{})[0] != "110" || workflow["110"].(map[string]interface{})["inputs"].(map[string]interface{})["audio"] != "canvas/voice.wav" {
		t.Fatalf("PDD reference audio graph: inputs=%#v load=%#v", h3Inputs, workflow["110"])
	}
	lora := workflow["8"].(map[string]interface{})["inputs"].(map[string]interface{})
	if lora["lora_name"] != comfyUIH3PDDLoraName || lora["strength_model"] != 2.0 {
		t.Fatalf("PDD LoRA = %#v", lora)
	}
	shift := workflow["9"].(map[string]interface{})["inputs"].(map[string]interface{})
	patch := workflow["11"].(map[string]interface{})["inputs"].(map[string]interface{})
	scheduler := workflow["15"].(map[string]interface{})["inputs"].(map[string]interface{})
	if shift["shift_video"] != 12.0 || shift["shift_audio"] != 3.0 || patch["contract"] != "enforce" || patch["mode"] != "exact_euler_step" || scheduler["mode"] != "trained_blocks" || scheduler["steps"] != 4 {
		t.Fatalf("PDD sampling contract: shift=%#v patch=%#v scheduler=%#v", shift, patch, scheduler)
	}
	createVideo := workflow["19"].(map[string]interface{})["inputs"].(map[string]interface{})
	if createVideo["audio"].([]interface{})[0] != "18" || workflow["20"].(map[string]interface{})["inputs"].(map[string]interface{})["format"] != "mp4" {
		t.Fatalf("PDD output graph: create=%#v save=%#v", createVideo, workflow["20"])
	}
	for _, test := range []struct {
		images        int
		size          string
		width, height int
	}{{2, "16:9", 864, 480}, {4, "16:9", 736, 416}, {7, "16:9", 608, 352}, {9, "9:16", 352, 608}} {
		if width, height := comfyUIH3PDDDimensions(test.size, test.images); width != test.width || height != test.height {
			t.Fatalf("PDD dimensions for %d %s = %dx%d", test.images, test.size, width, height)
		}
	}
	if !isComfyUIH3Model("MiniMax-H3-R2V-PDD-4Step") || !isComfyUIH3PDDModel("MINIMAX-H3-R2V-PDD-4STEP") {
		t.Fatal("PDD model was not recognized")
	}
	for seconds, want := range map[int]int{5: 124, 7: 175, 10: 243, 15: 362} {
		if frames := comfyUIH3FrameCount(seconds); frames != want {
			t.Fatalf("PDD %d-second frame count = %d, want %d", seconds, frames, want)
		}
	}
}

func TestComfyUIH3PDDPromptRejectsUnsupportedSettings(t *testing.T) {
	base := canvasGenerationInput{
		Prompt:          "Use <Picture 1>.",
		Config:          providerConfig{Model: "MiniMax-H3-R2V-PDD-4Step", VideoSeconds: "5", Size: "16:9", VQuality: "480P"},
		ReferenceImages: []providerMedia{{DataURL: comfyUIH3TestImage}},
	}
	for name, mutate := range map[string]func(*canvasGenerationInput){
		"duration":   func(input *canvasGenerationInput) { input.Config.VideoSeconds = "6" },
		"ratio":      func(input *canvasGenerationInput) { input.Config.Size = "1:1" },
		"resolution": func(input *canvasGenerationInput) { input.Config.VQuality = "768P" },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, err := comfyUIH3PromptBody(context.Background(), input); err == nil {
				t.Fatal("unsupported PDD setting was accepted")
			}
		})
	}
}

func TestComfyUIH3PromptRejectsInvalidAudioReferences(t *testing.T) {
	base := canvasGenerationInput{
		Prompt:          "Use <Picture 1> and <Audio 1>.",
		Config:          providerConfig{Model: "MiniMax-H3-R2V", VideoSeconds: "5", Size: "16:9", VQuality: "768P"},
		ReferenceImages: []providerMedia{{DataURL: comfyUIH3TestImage}},
	}
	for name, audios := range map[string][]providerMedia{
		"too many":       {{DurationMs: 2000}, {DurationMs: 2000}, {DurationMs: 2000}, {DurationMs: 2000}},
		"too short":      {{DurationMs: 1000}},
		"total too long": {{DurationMs: 8000}, {DurationMs: 8000}},
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.ReferenceAudios = audios
			if _, err := comfyUIH3PromptBody(context.Background(), input); err == nil {
				t.Fatal("invalid reference audio was accepted")
			}
		})
	}
}

func TestComfyUIH3PromptRejectsUnexpandedTextReferencesBeforeUpload(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		uploads++
		writeComfyUIH3TestJSON(w, map[string]interface{}{"name": "ref.png", "subfolder": "canvas", "type": "input"})
	}))
	defer server.Close()

	input := canvasGenerationInput{
		Config:          providerConfig{BaseURL: server.URL, Model: "MiniMax-H3-R2V", VideoSeconds: "5", Size: "16:9", VQuality: "768P"},
		ReferenceImages: []providerMedia{{DataURL: comfyUIH3TestImage}},
	}
	for _, prompt := range []string{"任务说明：@[node:shot-text]", "任务说明：【文本1】", "任务说明：【文本1】\n\n【文本1】\n\n"} {
		input.Prompt = prompt
		if _, err := comfyUIH3PromptBody(context.Background(), input); err == nil || !strings.Contains(err.Error(), "未展开的文本节点引用") {
			t.Fatalf("prompt %q error = %v", prompt, err)
		}
	}
	if uploads != 0 {
		t.Fatalf("invalid prompts uploaded %d images", uploads)
	}

	input.Prompt = "任务说明：【文本1】\n\n【文本1】\n镜头的真实正文"
	body, err := comfyUIH3PromptBody(context.Background(), input)
	if err != nil {
		t.Fatalf("expanded prompt error = %v", err)
	}
	if uploads != 1 {
		t.Fatalf("expanded prompt uploaded %d images", uploads)
	}
	graph := body["prompt"].(map[string]interface{})
	h3Inputs := graph["136"].(map[string]interface{})["inputs"].(map[string]interface{})
	if h3Inputs["prompt"] != input.Prompt {
		t.Fatalf("submitted prompt = %#v", h3Inputs["prompt"])
	}
}

func TestComfyUIH3PDDPromptAcceptsPublishedDurations(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeComfyUIH3TestJSON(w, map[string]interface{}{"name": "ref.png", "subfolder": "canvas", "type": "input"})
	}))
	defer server.Close()

	for seconds, wantFrames := range map[string]int{"10": 243, "15": 362} {
		body, err := comfyUIH3PromptBody(context.Background(), canvasGenerationInput{
			Prompt:          "Use <Picture 1>.",
			Config:          providerConfig{BaseURL: server.URL, Model: "MiniMax-H3-R2V-PDD-4Step", VideoSeconds: seconds, Size: "16:9", VQuality: "480P"},
			ReferenceImages: []providerMedia{{DataURL: comfyUIH3TestImage}},
		})
		if err != nil {
			t.Fatalf("PDD %s-second prompt: %v", seconds, err)
		}
		graph := body["prompt"].(map[string]interface{})
		inputs := graph["7"].(map[string]interface{})["inputs"].(map[string]interface{})
		if inputs["length"] != wantFrames {
			t.Fatalf("PDD %s-second length = %v, want %d", seconds, inputs["length"], wantFrames)
		}
	}
}

func TestComfyUIH3ConnectionValidatesPDDAssets(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	missingHeads := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system_stats" {
			writeComfyUIH3TestJSON(w, map[string]interface{}{"system": map[string]interface{}{"comfyui_version": "0.31.1"}})
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/object_info/")
		info := map[string]interface{}{"input": map[string]interface{}{}}
		switch name {
		case "MiniMaxH3ReferenceToVideo":
			info["input"] = map[string]interface{}{"optional": map[string]interface{}{"ref_audios": map[string]interface{}{"prefix": "ref_audio_"}}}
		case "LoraLoaderModelOnly":
			info["input"] = map[string]interface{}{"required": map[string]interface{}{"lora_name": []interface{}{[]interface{}{comfyUIH3PDDLoraName}}}}
		case "MiniMaxH3PDDHeadsLoader":
			options := []interface{}{}
			if !missingHeads {
				options = append(options, comfyUIH3PDDHeadsName)
			}
			info["input"] = map[string]interface{}{"required": map[string]interface{}{"heads_name": []interface{}{"COMBO", map[string]interface{}{"options": options}}}}
		}
		writeComfyUIH3TestJSON(w, map[string]interface{}{name: info})
	}))
	defer server.Close()

	config := providerConfig{BaseURL: server.URL, Model: "MiniMax-H3-R2V-PDD-4Step"}
	if err := testComfyUIH3Connection(context.Background(), config); err != nil {
		t.Fatalf("PDD connection test = %v", err)
	}
	missingHeads = true
	if err := testComfyUIH3Connection(context.Background(), config); err == nil || !strings.Contains(err.Error(), comfyUIH3PDDHeadsName) {
		t.Fatalf("missing PDD heads error = %v", err)
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
			file, header, err := r.FormFile("image")
			if err != nil {
				t.Fatalf("read uploaded image: %v", err)
			}
			data, _ := io.ReadAll(file)
			_ = file.Close()
			uploads++
			name := fmt.Sprintf("ref-%d.png", uploads)
			if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
				if !strings.HasSuffix(header.Filename, ".wav") {
					t.Fatalf("uploaded WAV filename = %q", header.Filename)
				}
				name = "voice.wav"
			} else if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
				t.Fatalf("uploaded data is not PNG: %x", data)
			}
			writeComfyUIH3TestJSON(w, map[string]interface{}{"name": name, "subfolder": "canvas", "type": "input"})
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
		ReferenceAudios: []providerMedia{{ID: "voice", DataURL: comfyUIH3TestAudio, DurationMs: 5000}},
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
	if h3Inputs["ref_audios.ref_audio_0"].([]interface{})[0] != "146" || graph["146"].(map[string]interface{})["inputs"].(map[string]interface{})["audio"] != "canvas/voice.wav" {
		t.Fatalf("submitted audio graph: inputs=%#v load=%#v", h3Inputs, graph["146"])
	}
	if got, want := strings.Join(paths, ","), "POST /upload/image,POST /upload/image,POST /upload/image,POST /prompt,GET /history/prompt-1,GET /view"; got != want {
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
		"outputs": map[string]interface{}{"92": map[string]interface{}{"images": []interface{}{map[string]interface{}{"filename": "result.mp4", "subfolder": "Canvas", "type": "output"}}, "animated": []interface{}{true}}},
	}}
}

func writeComfyUIH3TestJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
