package cli

import (
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFailOnAndNoComment(t *testing.T) {
	tests := []struct {
		name          string
		env           map[string]string
		args          []string
		wantFailOn    string
		wantNoComment bool
	}{
		{"defaults", nil, nil, "", false},
		{"--fail-on is trimmed and lowercased", nil, []string{"--fail-on", " AFFECTED "}, "affected", false},
		{"GRAPHWAKE_FAIL_ON", map[string]string{"GRAPHWAKE_FAIL_ON": "breaking"}, nil, "breaking", false},
		{"flag wins over GRAPHWAKE_FAIL_ON", map[string]string{"GRAPHWAKE_FAIL_ON": "breaking"}, []string{"--fail-on", "affected"}, "affected", false},
		{"unknown --fail-on is passed on (the server answers invalid)", nil, []string{"--fail-on", "Sometimes"}, "sometimes", false},
		{"--no-comment", nil, []string{"--no-comment"}, "", true},
		{"GRAPHWAKE_COMMENT=false", map[string]string{"GRAPHWAKE_COMMENT": "false"}, nil, "", true},
		{"GRAPHWAKE_COMMENT=0", map[string]string{"GRAPHWAKE_COMMENT": "0"}, nil, "", true},
		{"GRAPHWAKE_COMMENT=Off", map[string]string{"GRAPHWAKE_COMMENT": "Off"}, nil, "", true},
		{"GRAPHWAKE_COMMENT=true", map[string]string{"GRAPHWAKE_COMMENT": "true"}, nil, "", false},
		{"GRAPHWAKE_COMMENT empty", map[string]string{"GRAPHWAKE_COMMENT": ""}, nil, "", false},
		{"set --no-comment=false wins over GRAPHWAKE_COMMENT=false", map[string]string{"GRAPHWAKE_COMMENT": "false"},
			[]string{"--no-comment=false"}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := parseUpload(tt.args, envOf(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			if c.FailOn != tt.wantFailOn || c.NoComment != tt.wantNoComment {
				t.Fatalf("FailOn = %q NoComment = %v, want %q %v", c.FailOn, c.NoComment, tt.wantFailOn, tt.wantNoComment)
			}
		})
	}
}

func TestUploadUsageNamesNewFlags(t *testing.T) {
	if !strings.Contains(uploadUsage, "[--fail-on breaking|affected] [--no-comment]") {
		t.Fatalf("usage = %q", uploadUsage)
	}
}
