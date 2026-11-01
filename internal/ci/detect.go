// Package ci works out, from the CI platform's environment, what kind of build is running: a pull request
// (open, closed or merged), a branch build, or something the agent cannot classify.
package ci

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Platform is the CI platform, sent to Graphwake as context.platform.
type Platform string

const (
	GitHub    Platform = "github"
	GitLab    Platform = "gitlab"
	Azure     Platform = "azure"
	Bitbucket Platform = "bitbucket"
	Jenkins   Platform = "jenkins"
	Other     Platform = "other"
)

// Event is what the build is about.
type Event int

const (
	// EventUnknown: neither a pull request nor a build of a known branch. Nothing is sent unless flags say otherwise.
	EventUnknown Event = iota
	// EventBranch: a build of Info.Branch (push, schedule, manual run ...).
	EventBranch
	// EventPullRequest: a pull request was opened or updated.
	EventPullRequest
	// EventPRClosed: a pull request was closed without merging (GitHub only).
	EventPRClosed
	// EventPRMerged: a pull request was merged (GitHub only). The default-branch build reports the merge.
	EventPRMerged
)

// Info is everything the agent learned from the environment.
type Info struct {
	Platform Platform
	Event    Event

	// Pull request fields (EventPullRequest, EventPRClosed, EventPRMerged).
	PRID, PRURL, Author string

	// Branch is the branch being built for EventBranch ("" when unknown).
	Branch string
	// CommitBranch is the branch the commit is on, sent as context.branch: the branch being built, or a pull
	// request's source branch ("" when unknown, e.g. a tag build). Unlike Branch it is metadata only.
	CommitBranch string
	// DefaultBranch is the repository's default branch when the platform says it ("" otherwise).
	DefaultBranch string

	CommitSHA     string
	CommitTime    string // RFC3339 when known
	CommitMessage string

	// GitHub only: used for the tip-of-branch check and the merged-PR lookup.
	Repo, APIURL, Token string

	// Problem explains why a build that looks like a pull request could not be read ("" when fine).
	Problem string

	prURLBase string
}

// PRURLFor builds the link of pull request id on this repository ("" when the platform gives no base URL).
func (i Info) PRURLFor(id string) string {
	if i.prURLBase == "" || id == "" {
		return ""
	}
	return i.prURLBase + id
}

// Getenv reads an environment variable; ReadFile reads a file (the GitHub event payload).
type (
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
)

// Detect inspects the environment of the running CI job.
func Detect(getenv Getenv, readFile ReadFile) Info {
	info := detect(getenv, readFile)
	info.CommitBranch = commitBranch(getenv, info.Platform)
	return info
}

func detect(getenv Getenv, readFile ReadFile) Info {
	switch {
	case getenv("GITHUB_ACTIONS") == "true":
		return detectGitHub(getenv, readFile)
	case getenv("GITLAB_CI") != "":
		return detectGitLab(getenv)
	case strings.EqualFold(getenv("TF_BUILD"), "true"):
		return detectAzure(getenv)
	case getenv("BITBUCKET_BUILD_NUMBER") != "" || getenv("BITBUCKET_COMMIT") != "":
		return detectBitbucket(getenv)
	case getenv("JENKINS_URL") != "" || getenv("JENKINS_HOME") != "":
		return detectJenkins(getenv)
	}
	return Info{Platform: Other, Event: EventUnknown}
}

// commitBranch is the branch the built commit is on: a pull request's source branch, else the branch being
// built. Tags and detached builds give "".
func commitBranch(getenv Getenv, platform Platform) string {
	first := func(names ...string) string {
		for _, n := range names {
			if v := strings.TrimSpace(getenv(n)); v != "" {
				return v
			}
		}
		return ""
	}
	heads := func(ref string) string {
		branch, _ := strings.CutPrefix(ref, "refs/heads/")
		if strings.HasPrefix(branch, "refs/") {
			return ""
		}
		return branch
	}
	switch platform {
	case GitHub:
		// GITHUB_HEAD_REF: the source branch on pull_request events; GITHUB_REF_NAME is also set for tags.
		if b := getenv("GITHUB_HEAD_REF"); b != "" {
			return b
		}
		if b, ok := strings.CutPrefix(getenv("GITHUB_REF"), "refs/heads/"); ok {
			return b
		}
		if getenv("GITHUB_REF_TYPE") == "branch" {
			return getenv("GITHUB_REF_NAME")
		}
	case GitLab:
		// CI_COMMIT_REF_NAME is the tag name on tag pipelines, so only without CI_COMMIT_TAG.
		if b := first("CI_MERGE_REQUEST_SOURCE_BRANCH_NAME", "CI_COMMIT_BRANCH"); b != "" {
			return b
		}
		if getenv("CI_COMMIT_TAG") == "" {
			return getenv("CI_COMMIT_REF_NAME")
		}
	case Azure:
		if b := heads(getenv("SYSTEM_PULLREQUEST_SOURCEBRANCH")); b != "" {
			return b
		}
		if ref := getenv("BUILD_SOURCEBRANCH"); ref != "" {
			return heads(ref)
		}
		// BUILD_SOURCEBRANCHNAME is only the last path segment ("feature/x" gives "x"): a last resort.
		return getenv("BUILD_SOURCEBRANCHNAME")
	case Bitbucket:
		// Set on branch and pull request pipelines (the source branch); absent on tag pipelines.
		return getenv("BITBUCKET_BRANCH")
	case Jenkins:
		// On a change request BRANCH_NAME is "PR-5"; CHANGE_BRANCH is the source branch.
		if getenv("CHANGE_ID") != "" {
			return getenv("CHANGE_BRANCH")
		}
		if b := getenv("BRANCH_NAME"); b != "" {
			return b
		}
		return strings.TrimPrefix(getenv("GIT_BRANCH"), "origin/")
	}
	return ""
}

type githubEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest *struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	HeadCommit *struct {
		Message   string `json:"message"`
		Timestamp string `json:"timestamp"`
	} `json:"head_commit"`
}

func detectGitHub(getenv Getenv, readFile ReadFile) Info {
	server := strings.TrimSuffix(getenv("GITHUB_SERVER_URL"), "/")
	if server == "" {
		server = "https://github.com"
	}
	api := strings.TrimSuffix(getenv("GITHUB_API_URL"), "/")
	if api == "" {
		api = "https://api.github.com"
	}
	info := Info{
		Platform:  GitHub,
		Repo:      getenv("GITHUB_REPOSITORY"),
		APIURL:    api,
		Token:     getenv("GITHUB_TOKEN"),
		CommitSHA: getenv("GITHUB_SHA"),
	}
	if info.Repo != "" {
		info.prURLBase = server + "/" + info.Repo + "/pull/"
	}

	var ev githubEvent
	evOK := false
	if path := getenv("GITHUB_EVENT_PATH"); path != "" {
		if data, err := readFile(path); err == nil && json.Unmarshal(data, &ev) == nil {
			evOK = true
		}
	}
	if evOK {
		info.DefaultBranch = ev.Repository.DefaultBranch
	}

	name := getenv("GITHUB_EVENT_NAME")
	if name == "pull_request" || name == "pull_request_target" {
		if !evOK || ev.PullRequest == nil {
			info.Event = EventUnknown
			info.Problem = "could not read the pull request event payload"
			return info
		}
		pr := ev.PullRequest
		number := pr.Number
		if number == 0 {
			number = ev.Number
		}
		info.PRID = itoa(number)
		if info.PRID == "" {
			info.Event = EventUnknown
			info.Problem = "the pull request event has no pull request number"
			return info
		}
		info.PRURL = pr.HTMLURL
		info.Author = pr.User.Login
		if pr.Head.SHA != "" {
			// GITHUB_SHA is GitHub's temporary merge commit on pull_request events; the PR's own commit is the head.
			info.CommitSHA = pr.Head.SHA
		}
		switch {
		case ev.Action == "closed" && pr.Merged:
			info.Event = EventPRMerged
		case ev.Action == "closed":
			info.Event = EventPRClosed
		default:
			info.Event = EventPullRequest
		}
		return info
	}

	if evOK && ev.HeadCommit != nil {
		info.CommitMessage = ev.HeadCommit.Message
		info.CommitTime = ev.HeadCommit.Timestamp
	}
	// Only events that build the branch itself may publish the live spec. Others that run on the default
	// branch's ref (issue_comment, workflow_run, check_suite, deployment, ...) often build a pull request's code.
	switch name {
	case "push", "workflow_dispatch", "schedule":
	default:
		return info
	}
	if branch, ok := strings.CutPrefix(getenv("GITHUB_REF"), "refs/heads/"); ok && branch != "" {
		info.Branch = branch
		info.Event = EventBranch
	}
	return info
}

func detectGitLab(getenv Getenv) Info {
	project := strings.TrimSuffix(getenv("CI_PROJECT_URL"), "/")
	info := Info{
		Platform:      GitLab,
		DefaultBranch: getenv("CI_DEFAULT_BRANCH"),
		CommitSHA:     getenv("CI_COMMIT_SHA"),
		CommitTime:    getenv("CI_COMMIT_TIMESTAMP"),
		CommitMessage: getenv("CI_COMMIT_MESSAGE"),
	}
	if project != "" {
		info.prURLBase = project + "/-/merge_requests/"
	}
	if iid := getenv("CI_MERGE_REQUEST_IID"); iid != "" {
		info.Event = EventPullRequest
		info.PRID = iid
		if mrProject := strings.TrimSuffix(getenv("CI_MERGE_REQUEST_PROJECT_URL"), "/"); mrProject != "" {
			info.PRURL = mrProject + "/-/merge_requests/" + iid
		} else {
			info.PRURL = info.PRURLFor(iid)
		}
		info.Author = getenv("GITLAB_USER_LOGIN")
		return info
	}
	if branch := getenv("CI_COMMIT_BRANCH"); branch != "" {
		info.Event = EventBranch
		info.Branch = branch
	}
	return info
}

func detectAzure(getenv Getenv) Info {
	info := Info{
		Platform:      Azure,
		CommitSHA:     getenv("BUILD_SOURCEVERSION"),
		CommitMessage: getenv("BUILD_SOURCEVERSIONMESSAGE"),
	}
	repoURI := strings.TrimSuffix(getenv("BUILD_REPOSITORY_URI"), "/")
	switch provider := getenv("BUILD_REPOSITORY_PROVIDER"); {
	case repoURI == "":
	case provider == "TfsGit":
		info.prURLBase = repoURI + "/pullrequest/"
	case provider == "GitHub" || provider == "GitHubEnterprise":
		info.prURLBase = strings.TrimSuffix(repoURI, ".git") + "/pull/"
	}
	if getenv("BUILD_REASON") == "PullRequest" {
		// PULLREQUESTNUMBER is the GitHub number for GitHub-hosted repos; PULLREQUESTID is the Azure Repos id.
		id := getenv("SYSTEM_PULLREQUEST_PULLREQUESTNUMBER")
		if id == "" {
			id = getenv("SYSTEM_PULLREQUEST_PULLREQUESTID")
		}
		if id == "" {
			info.Problem = "pull request build without a pull request id"
			return info
		}
		info.Event = EventPullRequest
		info.PRID = id
		info.PRURL = info.PRURLFor(id)
		info.Author = getenv("BUILD_REQUESTEDFOR")
		if head := getenv("SYSTEM_PULLREQUEST_SOURCECOMMITID"); head != "" {
			info.CommitSHA = head
		}
		return info
	}
	if branch, ok := strings.CutPrefix(getenv("BUILD_SOURCEBRANCH"), "refs/heads/"); ok && branch != "" {
		info.Event = EventBranch
		info.Branch = branch
	}
	return info
}

func detectBitbucket(getenv Getenv) Info {
	info := Info{Platform: Bitbucket, CommitSHA: getenv("BITBUCKET_COMMIT")}
	if repo := getenv("BITBUCKET_REPO_FULL_NAME"); repo != "" {
		info.prURLBase = "https://bitbucket.org/" + repo + "/pull-requests/"
	}
	if id := getenv("BITBUCKET_PR_ID"); id != "" {
		info.Event = EventPullRequest
		info.PRID = id
		info.PRURL = info.PRURLFor(id)
		return info
	}
	if branch := getenv("BITBUCKET_BRANCH"); branch != "" {
		info.Event = EventBranch
		info.Branch = branch
	}
	return info
}

func detectJenkins(getenv Getenv) Info {
	info := Info{Platform: Jenkins, CommitSHA: getenv("GIT_COMMIT")}
	if id := getenv("CHANGE_ID"); id != "" {
		info.Event = EventPullRequest
		info.PRID = id
		info.PRURL = getenv("CHANGE_URL")
		info.Author = getenv("CHANGE_AUTHOR")
		return info
	}
	branch := getenv("BRANCH_NAME")
	if branch == "" {
		branch = strings.TrimPrefix(getenv("GIT_BRANCH"), "origin/")
	}
	if branch != "" {
		info.Event = EventBranch
		info.Branch = branch
	}
	return info
}

func itoa(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}
