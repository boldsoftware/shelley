package exeenv

import "testing"

func TestFromHostnameBuildsEnvironmentURLs(t *testing.T) {
	tests := []struct {
		name           string
		hostname       string
		reflectionURL  string
		personalLLMURL string
		teamLLMURL     string
	}{
		{
			name:           "production",
			hostname:       "box.exe.xyz",
			reflectionURL:  "https://reflection.int.exe.xyz",
			personalLLMURL: "https://llm.int.exe.xyz",
			teamLLMURL:     "https://llm.team.exe.xyz",
		},
		{
			name:           "development",
			hostname:       "box.exe.cloud",
			reflectionURL:  "http://reflection.int.exe.cloud",
			personalLLMURL: "http://llm.int.exe.cloud",
			teamLLMURL:     "http://llm.team.exe.cloud",
		},
		{
			name:           "development subdomain",
			hostname:       "box.shelley.exe.cloud",
			reflectionURL:  "http://reflection.int.exe.cloud",
			personalLLMURL: "http://llm.int.exe.cloud",
			teamLLMURL:     "http://llm.team.exe.cloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := FromHostname(tt.hostname)
			if got := env.ReflectionURL(); got != tt.reflectionURL {
				t.Errorf("ReflectionURL() = %q, want %q", got, tt.reflectionURL)
			}
			if got := env.IntegrationURL("llm", false); got != tt.personalLLMURL {
				t.Errorf("IntegrationURL(personal) = %q, want %q", got, tt.personalLLMURL)
			}
			if got := env.IntegrationURL("llm", true); got != tt.teamLLMURL {
				t.Errorf("IntegrationURL(team) = %q, want %q", got, tt.teamLLMURL)
			}
		})
	}
}
