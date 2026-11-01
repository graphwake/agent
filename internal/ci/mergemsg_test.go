package ci

import "testing"

func TestMergedPRFromMessage(t *testing.T) {
	tests := []struct {
		name     string
		platform Platform
		message  string
		want     string
	}{
		{"github merge commit", GitHub, "Merge pull request #123 from acme/feature\n\nAdd orders", "123"},
		{"github squash", GitHub, "Add orders endpoint (#124)\n\n* first\n* second", "124"},
		{"github squash single line", GitHub, "Add orders endpoint (#125)", "125"},
		{"github issue reference in body is not a merge", GitHub, "Fix bug\n\nSee (#99)", ""},
		{"github direct push", GitHub, "Fix typo", ""},
		{"gitlab merge commit", GitLab, "Merge branch 'feature' into 'main'\n\nAdd orders\n\nSee merge request acme/orders!7", "7"},
		{"gitlab nested group", GitLab, "Merge branch 'x'\n\nSee merge request acme/team/orders!81", "81"},
		{"gitlab ignores github pattern", GitLab, "Add orders (#5)", ""},
		{"azure merged PR", Azure, "Merged PR 15: Add orders endpoint", "15"},
		{"azure not at start", Azure, "Revert \"Merged PR 15: x\"", ""},
		{"bitbucket merge", Bitbucket, "Merged in feature/orders (pull request #9)\n\nAdd orders", "9"},
		{"jenkins tries every platform: gitlab", Jenkins, "See merge request acme/orders!3", "3"},
		{"jenkins tries every platform: github", Jenkins, "Merge pull request #4 from a/b", "4"},
		{"unknown CI tries every platform: azure", Other, "Merged PR 6: x", "6"},
		{"empty message", GitHub, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MergedPRFromMessage(tt.platform, tt.message); got != tt.want {
				t.Errorf("MergedPRFromMessage(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}
}
