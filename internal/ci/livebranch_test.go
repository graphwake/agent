package ci

import (
	"reflect"
	"testing"
)

func TestLiveBranches(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		info    Info
		want    []string
	}{
		{"setting wins", " main, release/* ,", Info{DefaultBranch: "trunk"}, []string{"main", "release/*"}},
		{"platform default branch", "", Info{DefaultBranch: "trunk"}, []string{"trunk"}},
		{"fallback main and master", "", Info{}, []string{"main", "master"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LiveBranches(tt.setting, tt.info); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LiveBranches = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsLiveBranch(t *testing.T) {
	tests := []struct {
		branch   string
		patterns []string
		want     bool
	}{
		{"main", []string{"main", "master"}, true},
		{"master", []string{"main", "master"}, true},
		{"feature/x", []string{"main", "master"}, false},
		{"hotfix", []string{"main"}, false},
		{"release/1.2", []string{"main", "release/*"}, true},
		{"release/1.2/rc", []string{"release/*"}, false},
		{"production", []string{"production"}, true},
		{"", []string{"main"}, false},
		{"main", []string{"[invalid"}, false},
	}
	for _, tt := range tests {
		if got := IsLiveBranch(tt.branch, tt.patterns); got != tt.want {
			t.Errorf("IsLiveBranch(%q, %v) = %v, want %v", tt.branch, tt.patterns, got, tt.want)
		}
	}
}
