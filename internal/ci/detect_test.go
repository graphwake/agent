package ci

import (
	"os"
	"testing"
)

func envOf(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func TestDetect(t *testing.T) {
	gh := func(event, fixture string, extra map[string]string) map[string]string {
		m := map[string]string{
			"GITHUB_ACTIONS":    "true",
			"GITHUB_EVENT_NAME": event,
			"GITHUB_REPOSITORY": "acme/orders",
			"GITHUB_SHA":        "ffffffffffffffffffffffffffffffffffffffff",
			"GITHUB_TOKEN":      "ghs_token",
		}
		if fixture != "" {
			m["GITHUB_EVENT_PATH"] = "testdata/github/" + fixture
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	head := "1111111111111111111111111111111111111111"

	tests := []struct {
		name string
		env  map[string]string
		want Info
	}{
		{
			name: "github pull request opened/updated",
			env: gh("pull_request", "pr_opened.json", map[string]string{"GITHUB_REF": "refs/pull/42/merge",
				"GITHUB_HEAD_REF": "feature/orders"}),
			want: Info{CommitBranch: "feature/orders", Platform: GitHub, Event: EventPullRequest, PRID: "42", PRURL: "https://github.com/acme/orders/pull/42",
				Author: "octocat", CommitSHA: head, DefaultBranch: "main"},
		},
		{
			name: "github pull_request_target counts as a pull request",
			env:  gh("pull_request_target", "pr_opened.json", nil),
			want: Info{Platform: GitHub, Event: EventPullRequest, PRID: "42", PRURL: "https://github.com/acme/orders/pull/42",
				Author: "octocat", CommitSHA: head, DefaultBranch: "main"},
		},
		{
			name: "github pull request closed without merge",
			env:  gh("pull_request", "pr_closed.json", nil),
			want: Info{Platform: GitHub, Event: EventPRClosed, PRID: "42", PRURL: "https://github.com/acme/orders/pull/42",
				Author: "octocat", CommitSHA: head, DefaultBranch: "main"},
		},
		{
			name: "github pull request merged",
			env:  gh("pull_request", "pr_merged.json", nil),
			want: Info{Platform: GitHub, Event: EventPRMerged, PRID: "42", PRURL: "https://github.com/acme/orders/pull/42",
				Author: "octocat", CommitSHA: head, DefaultBranch: "main"},
		},
		{
			name: "github pull request without a readable payload",
			env:  gh("pull_request", "missing.json", nil),
			want: Info{Platform: GitHub, Event: EventUnknown, CommitSHA: "ffffffffffffffffffffffffffffffffffffffff",
				Problem: "could not read the pull request event payload"},
		},
		{
			name: "github push",
			env:  gh("push", "push.json", map[string]string{"GITHUB_REF": "refs/heads/trunk"}),
			want: Info{CommitBranch: "trunk", Platform: GitHub, Event: EventBranch, Branch: "trunk", DefaultBranch: "trunk",
				CommitSHA:     "ffffffffffffffffffffffffffffffffffffffff",
				CommitMessage: "Merge pull request #42 from acme/feature\n\nAdd orders endpoint",
				CommitTime:    "2026-10-30T10:00:00+03:00"},
		},
		{
			name: "github tag push is not a branch build",
			env: gh("push", "", map[string]string{"GITHUB_REF": "refs/tags/v1.0.0", "GITHUB_REF_NAME": "v1.0.0",
				"GITHUB_REF_TYPE": "tag"}),
			want: Info{Platform: GitHub, Event: EventUnknown, CommitSHA: "ffffffffffffffffffffffffffffffffffffffff"},
		},
		{
			name: "github manual run on a branch",
			env:  gh("workflow_dispatch", "", map[string]string{"GITHUB_REF": "refs/heads/main"}),
			want: Info{CommitBranch: "main", Platform: GitHub, Event: EventBranch, Branch: "main", CommitSHA: "ffffffffffffffffffffffffffffffffffffffff"},
		},
		{
			name: "github scheduled run on a branch",
			env:  gh("schedule", "", map[string]string{"GITHUB_REF": "refs/heads/main"}),
			want: Info{CommitBranch: "main", Platform: GitHub, Event: EventBranch, Branch: "main", CommitSHA: "ffffffffffffffffffffffffffffffffffffffff"},
		},
		{
			name: "github issue_comment on the default branch ref is not a branch build",
			env:  gh("issue_comment", "", map[string]string{"GITHUB_REF": "refs/heads/main"}),
			want: Info{CommitBranch: "main", Platform: GitHub, Event: EventUnknown, CommitSHA: "ffffffffffffffffffffffffffffffffffffffff"},
		},
		{
			name: "github workflow_run is not a branch build",
			env:  gh("workflow_run", "", map[string]string{"GITHUB_REF": "refs/heads/main"}),
			want: Info{CommitBranch: "main", Platform: GitHub, Event: EventUnknown, CommitSHA: "ffffffffffffffffffffffffffffffffffffffff"},
		},
		{
			name: "github check_suite is not a branch build",
			env:  gh("check_suite", "", map[string]string{"GITHUB_REF": "refs/heads/main"}),
			want: Info{CommitBranch: "main", Platform: GitHub, Event: EventUnknown, CommitSHA: "ffffffffffffffffffffffffffffffffffffffff"},
		},
		{
			name: "github deployment is not a branch build",
			env:  gh("deployment", "", map[string]string{"GITHUB_REF": "refs/heads/main"}),
			want: Info{CommitBranch: "main", Platform: GitHub, Event: EventUnknown, CommitSHA: "ffffffffffffffffffffffffffffffffffffffff"},
		},
		{
			name: "gitlab merge request pipeline",
			env: map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "7", "CI_PROJECT_URL": "https://gitlab.com/acme/orders",
				"CI_MERGE_REQUEST_PROJECT_URL": "https://gitlab.com/acme/orders", "GITLAB_USER_LOGIN": "dev",
				"CI_DEFAULT_BRANCH": "main", "CI_COMMIT_SHA": "abc", "CI_COMMIT_TIMESTAMP": "2026-10-30T07:00:00+00:00",
				"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME": "feature/x", "CI_COMMIT_REF_NAME": "feature/x"},
			want: Info{CommitBranch: "feature/x", Platform: GitLab, Event: EventPullRequest, PRID: "7", PRURL: "https://gitlab.com/acme/orders/-/merge_requests/7",
				Author: "dev", DefaultBranch: "main", CommitSHA: "abc", CommitTime: "2026-10-30T07:00:00+00:00"},
		},
		{
			name: "gitlab default branch pipeline",
			env: map[string]string{"GITLAB_CI": "true", "CI_COMMIT_BRANCH": "main", "CI_DEFAULT_BRANCH": "main",
				"CI_COMMIT_SHA": "abc", "CI_COMMIT_MESSAGE": "Merge branch 'x' into 'main'\n\nSee merge request acme/orders!7"},
			want: Info{CommitBranch: "main", Platform: GitLab, Event: EventBranch, Branch: "main", DefaultBranch: "main", CommitSHA: "abc",
				CommitMessage: "Merge branch 'x' into 'main'\n\nSee merge request acme/orders!7"},
		},
		{
			name: "gitlab tag pipeline has no branch",
			env: map[string]string{"GITLAB_CI": "true", "CI_COMMIT_TAG": "v1", "CI_COMMIT_REF_NAME": "v1",
				"CI_DEFAULT_BRANCH": "main"},
			want: Info{Platform: GitLab, Event: EventUnknown, DefaultBranch: "main"},
		},
		{
			name: "azure repos pull request",
			env: map[string]string{"TF_BUILD": "True", "BUILD_REASON": "PullRequest", "SYSTEM_PULLREQUEST_PULLREQUESTID": "15",
				"BUILD_REPOSITORY_PROVIDER": "TfsGit", "BUILD_REPOSITORY_URI": "https://dev.azure.com/acme/shop/_git/orders",
				"BUILD_REQUESTEDFOR": "Jane Doe", "BUILD_SOURCEVERSION": "merge", "SYSTEM_PULLREQUEST_SOURCECOMMITID": "head",
				"SYSTEM_PULLREQUEST_SOURCEBRANCH": "refs/heads/feature/orders", "BUILD_SOURCEBRANCH": "refs/pull/15/merge"},
			want: Info{CommitBranch: "feature/orders", Platform: Azure, Event: EventPullRequest, PRID: "15", PRURL: "https://dev.azure.com/acme/shop/_git/orders/pullrequest/15",
				Author: "Jane Doe", CommitSHA: "head"},
		},
		{
			name: "azure github-hosted pull request uses the GitHub number",
			env: map[string]string{"TF_BUILD": "True", "BUILD_REASON": "PullRequest", "SYSTEM_PULLREQUEST_PULLREQUESTID": "998877",
				"SYSTEM_PULLREQUEST_PULLREQUESTNUMBER": "42", "BUILD_REPOSITORY_PROVIDER": "GitHub",
				"BUILD_REPOSITORY_URI": "https://github.com/acme/orders.git"},
			want: Info{Platform: Azure, Event: EventPullRequest, PRID: "42", PRURL: "https://github.com/acme/orders/pull/42"},
		},
		{
			name: "azure main build",
			env: map[string]string{"TF_BUILD": "True", "BUILD_REASON": "IndividualCI", "BUILD_SOURCEBRANCH": "refs/heads/main",
				"BUILD_SOURCEVERSION": "abc", "BUILD_SOURCEVERSIONMESSAGE": "Merged PR 15: Add orders"},
			want: Info{CommitBranch: "main", Platform: Azure, Event: EventBranch, Branch: "main", CommitSHA: "abc", CommitMessage: "Merged PR 15: Add orders"},
		},
		{
			name: "bitbucket pull request",
			env: map[string]string{"BITBUCKET_BUILD_NUMBER": "3", "BITBUCKET_PR_ID": "9", "BITBUCKET_REPO_FULL_NAME": "acme/orders",
				"BITBUCKET_COMMIT": "abc", "BITBUCKET_BRANCH": "feature"},
			want: Info{CommitBranch: "feature", Platform: Bitbucket, Event: EventPullRequest, PRID: "9", PRURL: "https://bitbucket.org/acme/orders/pull-requests/9", CommitSHA: "abc"},
		},
		{
			name: "bitbucket branch",
			env:  map[string]string{"BITBUCKET_BUILD_NUMBER": "3", "BITBUCKET_BRANCH": "master", "BITBUCKET_COMMIT": "abc"},
			want: Info{CommitBranch: "master", Platform: Bitbucket, Event: EventBranch, Branch: "master", CommitSHA: "abc"},
		},
		{
			name: "jenkins multibranch change request",
			env: map[string]string{"JENKINS_URL": "https://ci", "CHANGE_ID": "5", "CHANGE_URL": "https://github.com/acme/orders/pull/5",
				"CHANGE_AUTHOR": "dev", "BRANCH_NAME": "PR-5", "GIT_COMMIT": "abc", "CHANGE_BRANCH": "feature/y"},
			want: Info{CommitBranch: "feature/y", Platform: Jenkins, Event: EventPullRequest, PRID: "5", PRURL: "https://github.com/acme/orders/pull/5", Author: "dev", CommitSHA: "abc"},
		},
		{
			name: "jenkins branch from GIT_BRANCH",
			env:  map[string]string{"JENKINS_HOME": "/var/jenkins", "GIT_BRANCH": "origin/main"},
			want: Info{CommitBranch: "main", Platform: Jenkins, Event: EventBranch, Branch: "main"},
		},
		{
			name: "azure branch with slashes keeps the full name",
			env: map[string]string{"TF_BUILD": "True", "BUILD_REASON": "IndividualCI",
				"BUILD_SOURCEBRANCH": "refs/heads/release/2026", "BUILD_SOURCEBRANCHNAME": "2026"},
			want: Info{Platform: Azure, Event: EventBranch, Branch: "release/2026", CommitBranch: "release/2026"},
		},
		{
			name: "azure tag build has no branch",
			env: map[string]string{"TF_BUILD": "True", "BUILD_REASON": "IndividualCI",
				"BUILD_SOURCEBRANCH": "refs/tags/v1", "BUILD_SOURCEBRANCHNAME": "v1"},
			want: Info{Platform: Azure, Event: EventUnknown},
		},
		{
			name: "unknown CI",
			env:  map[string]string{"CI": "true"},
			want: Info{Platform: Other, Event: EventUnknown},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Detect(envOf(tt.env), os.ReadFile)
			got.prURLBase = ""
			got.Repo, got.APIURL, got.Token = "", "", ""
			if got != tt.want {
				t.Errorf("Detect() =\n %+v\nwant\n %+v", got, tt.want)
			}
		})
	}
}

func TestDetectGitHubAPIFields(t *testing.T) {
	info := Detect(envOf(map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "acme/orders", "GITHUB_TOKEN": "t",
		"GITHUB_API_URL": "https://ghe.example/api/v3/", "GITHUB_SERVER_URL": "https://ghe.example",
	}), os.ReadFile)
	if info.APIURL != "https://ghe.example/api/v3" || info.Repo != "acme/orders" || info.Token != "t" {
		t.Fatalf("unexpected API fields %+v", info)
	}
	if got := info.PRURLFor("3"); got != "https://ghe.example/acme/orders/pull/3" {
		t.Fatalf("PRURLFor = %q", got)
	}
}
