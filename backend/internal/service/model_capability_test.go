package service

import (
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestValidateVideoTaskAcceptsLegacyMiniMaxH3Resolution(t *testing.T) {
	profile := DefaultModelCapabilityConfig(string(model.ChannelInterfaceMiniMaxH3)).Video
	input := canvasGenerationInput{
		Prompt: "test",
		Config: providerConfig{
			InterfaceType: string(model.ChannelInterfaceMiniMaxH3),
			VideoSeconds:  "9",
			Size:          "16:9",
			VQuality:      "720",
		},
	}

	if err := validateVideoTask(profile, input); err != nil {
		t.Fatalf("validateVideoTask() error = %v", err)
	}
}

func TestValidateVideoTaskAcceptsMiniMaxH3ReferenceVideoOperation(t *testing.T) {
	profile := DefaultModelCapabilityConfig(string(model.ChannelInterfaceMiniMaxH3)).Video
	input := canvasGenerationInput{
		Prompt: "test",
		Config: providerConfig{
			InterfaceType: string(model.ChannelInterfaceMiniMaxH3),
			VideoSeconds:  "11",
			Size:          "16:9",
			VQuality:      "768P",
		},
		ReferenceVideos: []providerMedia{{}},
		Metadata:        map[string]interface{}{"videoEditOperation": "extend"},
	}

	if err := validateVideoTask(profile, input); err != nil {
		t.Fatalf("validateVideoTask() error = %v", err)
	}
}
