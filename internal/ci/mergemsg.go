package ci

import "regexp"

// Default merge-commit messages of each platform, searched for the merged pull request's number.
var (
	githubMerge  = regexp.MustCompile(`\AMerge pull request #(\d+)\b`)
	githubSquash = regexp.MustCompile(`\A[^\n]*\(#(\d+)\)[ \t]*(?:\r?\n|\z)`)
	gitlabMerge  = regexp.MustCompile(`See merge request [^\s!]*!(\d+)\b`)
	azureMerge   = regexp.MustCompile(`\AMerged PR (\d+):`)
	bitbucketPR  = regexp.MustCompile(`\(pull request #(\d+)\)`)
)

func patternsFor(p Platform) []*regexp.Regexp {
	switch p {
	case GitHub:
		return []*regexp.Regexp{githubMerge, githubSquash}
	case GitLab:
		return []*regexp.Regexp{gitlabMerge}
	case Azure:
		return []*regexp.Regexp{azureMerge}
	case Bitbucket:
		return []*regexp.Regexp{bitbucketPR}
	}
	// Jenkins and unknown CIs can sit in front of any host.
	return []*regexp.Regexp{gitlabMerge, azureMerge, bitbucketPR, githubMerge, githubSquash}
}

// MergedPRFromMessage finds the merged pull request's number in a merge commit message ("" when none).
func MergedPRFromMessage(p Platform, message string) string {
	for _, re := range patternsFor(p) {
		if m := re.FindStringSubmatch(message); m != nil {
			return m[1]
		}
	}
	return ""
}
