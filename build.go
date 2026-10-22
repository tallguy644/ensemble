package main

// Build: research → plan → build → review → fix, passing work between models.
// Default casting: Perplexity researches, Claude plans, Codex builds, Gemini
// reviews, Claude fixes. Work happens on a fresh git branch so nothing touches
// your main branch, and every step is saved under <project>/.ensemble/runs/.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const inlineDiffLimit = 60000

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	PrepareCmd(cmd)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func gitOK(dir string, args ...string) bool {
	_, err := git(dir, args...)
	return err == nil
}

var ensembleIdentity = []string{"-c", "user.name=Ensemble", "-c", "user.email=ensemble@localhost"}

type GitInfo struct {
	Created        bool
	Branch         string
	OriginalBranch string
	StartRef       string
}

func PrepareGit(dir, taskSlug, runID string, allowDirty, newBranch bool) (*GitInfo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("Build needs git. Install it from git-scm.com, then try again.")
	}
	info := &GitInfo{}
	if !gitOK(dir, "rev-parse", "--is-inside-work-tree") {
		if _, err := git(dir, "init", "-q"); err != nil {
			return nil, err
		}
		info.Created = true
	}
	gitDir, err := git(dir, "rev-parse", "--git-dir")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	exclude := filepath.Join(gitDir, "info", "exclude")
	_ = os.MkdirAll(filepath.Dir(exclude), 0o755)
	ex, _ := os.ReadFile(exclude)
	if !regexp.MustCompile(`(?m)^\.ensemble/\r?$`).Match(ex) {
		prefix := ""
		if len(ex) > 0 && !bytes.HasSuffix(ex, []byte("\n")) {
			prefix = "\n"
		}
		f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(prefix + ".ensemble/\n")
			f.Close()
		}
	}
	hasCommits := gitOK(dir, "rev-parse", "--verify", "HEAD")
	dirty, _ := git(dir, "status", "--porcelain")
	if !hasCommits {
		// Baseline commit so the models' work can be diffed and undone.
		if _, err := git(dir, "add", "-A"); err != nil {
			return nil, err
		}
		if _, err := git(dir, append(ensembleIdentity, "commit", "-q", "--allow-empty", "-m", "Baseline before Ensemble")...); err != nil {
			return nil, err
		}
	} else if dirty != "" && !allowDirty {
		return nil, errors.New(`This project has uncommitted changes. Commit or stash them first (or tick "Allow uncommitted changes").`)
	}
	if info.StartRef, err = git(dir, "rev-parse", "HEAD"); err != nil {
		return nil, err
	}
	if gitOK(dir, "symbolic-ref", "-q", "HEAD") {
		info.OriginalBranch, _ = git(dir, "symbolic-ref", "--short", "HEAD")
	}
	if newBranch {
		suffix := runID
		if len(suffix) > 4 {
			suffix = suffix[len(suffix)-4:]
		}
		info.Branch = "ensemble/" + taskSlug + "-" + suffix
		if _, err := git(dir, "checkout", "-q", "-b", info.Branch); err != nil {
			return nil, err
		}
	} else {
		info.Branch = info.OriginalBranch
	}
	return info, nil
}

func currentDiff(dir, startRef string) (stat, patch string) {
	_, _ = git(dir, "add", "-A")
	stat, _ = git(dir, "diff", "--cached", "--stat", startRef)
	patch, _ = git(dir, "diff", "--cached", startRef)
	return
}

type testResult struct {
	OK     bool
	Code   int
	Output string
}

func runTests(line, dir string) *testResult {
	if line == "" {
		return nil
	}
	cmd := ShellCommand(line)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	PrepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		return &testResult{Code: -1, Output: err.Error()}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		KillTree(cmd)
		<-done
	}
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	s := strings.TrimSpace(strings.ToValidUTF8(out.String(), "�"))
	if len(s) > 12000 {
		s = "...(trimmed)\n" + s[len(s)-12000:]
	}
	return &testResult{OK: code == 0, Code: code, Output: s}
}

var verdictRe = regexp.MustCompile(`(?i)VERDICT:\s*\**\s*(APPROVED|CHANGES[_ ]REQUESTED)`)

func parseVerdict(text string) string {
	m := verdictRe.FindStringSubmatch(text)
	if m == nil {
		return "unclear"
	}
	if strings.HasPrefix(strings.ToUpper(m[1]), "APPROVED") {
		return "approved"
	}
	return "changes"
}

type BuildOpts struct {
	Dir             string
	Task            string
	Roles           map[string]string
	MaxReviewRounds int
	TestCommand     string
	Research        bool
	Git             *GitInfo
}

func RunBuild(r *Run, o BuildOpts) (*RunResult, error) {
	pref := func(role string) []string {
		list := []string{}
		if id := o.Roles[role]; id != "" {
			list = append(list, id)
		}
		list = append(list, r.Config.Roles[role]...)
		out := []string{}
		for _, id := range list {
			// Every role except research needs to read the project folder.
			if a := r.Config.Agents[id]; a != nil && (role == "researcher" || !a.NoFiles) {
				out = append(out, id)
			}
		}
		return out
	}
	ensDir := filepath.Join(o.Dir, ".ensemble")
	_ = os.MkdirAll(ensDir, 0o755)
	write := func(name, s string) { _ = os.WriteFile(filepath.Join(ensDir, name), []byte(s), 0o644) }
	rounds := o.MaxReviewRounds

	if o.Git.Created {
		r.Note("This folder was not a git repository, so Ensemble created one with a baseline commit.", "")
	}
	if o.Git.Branch != "" {
		r.Note("Working on branch "+o.Git.Branch+". Your other branches are untouched.", "")
	}

	// 0. Research (optional): a web-searching model gathers current facts first.
	var findings *StepResult
	researchers := []string{}
	for _, id := range pref("researcher") {
		if r.CanUse(id, "read") {
			researchers = append(researchers, id)
		}
	}
	if o.Research && len(researchers) > 0 {
		r.Phase("Research", "Current docs, versions and pitfalls from the web")
		res, err := r.Step(StepOpts{Title: "Research", Prefs: researchers, Prompt: `You are the researcher on a team of AI coding agents. Another agent will plan and build the task below; they can read the project but cannot search the web. Search the web and give them what they need that may be newer than their training: current stable versions of the libraries or tools involved, relevant API changes or deprecations, the recommended approach today, and known pitfalls. Be concise and practical, use bullet points, and cite sources. Do not write the implementation. If the task needs no outside facts, say so in one line.

TASK:
` + o.Task})
		if err == errCancelled {
			return nil, err
		}
		if err != nil {
			r.Note("Skipping research: "+err.Error(), "warn")
		} else {
			findings = &res
			write("RESEARCH.md", fmt.Sprintf("# Research (by %s)\n\n%s\n", res.Label, res.Output))
		}
	}
	researchBlock := ""
	if findings != nil {
		researchBlock = fmt.Sprintf("\n\nWEB RESEARCH (from %s; prefer it over your own memory for versions and APIs):\n%s", findings.Label, findings.Output)
	}

	// 1. Plan
	r.Phase("Plan", "")
	plan, err := r.Step(StepOpts{Title: "Plan", Prefs: pref("planner"), Prompt: `You are the lead planner on a team of AI coding agents (Claude, ChatGPT/Codex, Gemini, DeepSeek). Another agent will implement your plan, and a third will review it.

Explore the project in the current folder (read files as needed; do not modify anything), then write an implementation plan for the task below.

Reply with markdown containing:
## Summary – one paragraph.
## Steps – numbered, each naming the files to create or change and what to do.
## Acceptance criteria – a checklist the reviewer can verify.
## Risks – anything the builder must be careful about.

If the folder is empty, plan the project from scratch (structure, stack, files).

TASK:
` + o.Task + researchBlock})
	if err != nil {
		return nil, err
	}
	write("PLAN.md", fmt.Sprintf("# Plan (by %s)\n\nTask: %s\n\n%s\n", plan.Label, o.Task, plan.Output))

	// 2. Build
	r.Phase("Build", "")
	build, err := r.Step(StepOpts{Title: "Build", Prefs: pref("builder"), Mode: "write", Prompt: `You are the builder on a team of AI coding agents. Implement the plan below in the current folder by creating and editing files. Follow the plan; if a step is wrong, do the right thing and say so. Do not commit to git and do not touch the .ensemble folder.

When finished, reply with a short summary of what you changed and anything left undone.

TASK:
` + o.Task + `

PLAN (also saved at .ensemble/PLAN.md):
` + plan.Output})
	if err != nil {
		return nil, err
	}

	// 3. Review / fix loop
	verdict := "unclear"
	var lastReview *StepResult
	var tests *testResult
	fixers := []string{}
	for round := 1; round <= rounds+1; round++ {
		tests = runTests(o.TestCommand, o.Dir)
		if tests != nil {
			if tests.OK {
				r.Note(fmt.Sprintf("Tests (%s): passed", o.TestCommand), "")
			} else {
				r.Note(fmt.Sprintf("Tests (%s): failed (exit %d)", o.TestCommand, tests.Code), "warn")
			}
		}
		stat, patch := currentDiff(o.Dir, o.Git.StartRef)
		if patch == "" {
			r.Note("No file changes were made.", "warn")
			verdict = "no-changes"
			break
		}
		diffFile := filepath.Join(r.RunDir, fmt.Sprintf("diff-round%d.patch", round))
		_ = os.WriteFile(diffFile, []byte(patch), 0o644)
		rel, _ := filepath.Rel(o.Dir, diffFile)
		diffBlock := "DIFF:\n```diff\n" + patch + "\n```"
		if len(patch) > inlineDiffLimit {
			diffBlock = "The diff is large; read it from " + filepath.ToSlash(rel) + ".\nDIFF STAT:\n" + stat
		}
		testBlock := ""
		if tests != nil {
			testBlock = fmt.Sprintf("\nTEST RESULT (%s, exit %d):\n```\n%s\n```\n", o.TestCommand, tests.Code, tests.Output)
		}

		r.Phase(fmt.Sprintf("Review %d", round), "")
		review, err := r.Step(StepOpts{Title: fmt.Sprintf("Review %d", round), Prefs: pref("reviewer"), Prompt: `You are the code reviewer on a team of AI coding agents. Review the changes below against the task and plan. You may read any file in the current folder; do not modify anything.

Check: correctness, bugs, missing acceptance criteria, security problems, and anything that would not run. Ignore pure style nitpicks.

Reply in markdown:
## Findings – numbered list, most important first, each with file and what to change. Write "None." if there are none.
## Verdict – end with exactly one line: "VERDICT: APPROVED" or "VERDICT: CHANGES_REQUESTED".

TASK:
` + o.Task + `

PLAN:
` + plan.Output + "\n" + testBlock + "\n" + diffBlock})
		if err != nil {
			return nil, err
		}
		lastReview = &review
		write("REVIEW.md", fmt.Sprintf("# Review %d (by %s)\n\n%s\n", round, review.Label, review.Output))
		verdict = parseVerdict(review.Output)
		r.Emit(Event{Type: "verdict", Round: round, Verdict: verdict, By: review.Label})
		if verdict == "approved" && (tests == nil || tests.OK) {
			break
		}
		if round > rounds {
			plural := "s"
			if rounds == 1 {
				plural = ""
			}
			r.Note(fmt.Sprintf("Stopped after %d fix round%s; the last review still had findings.", rounds, plural), "warn")
			break
		}

		failing := ""
		if tests != nil && !tests.OK {
			failing = fmt.Sprintf("\nFAILING TESTS (%s):\n```\n%s\n```", o.TestCommand, tests.Output)
		}
		r.Phase(fmt.Sprintf("Fix %d", round), "")
		fix, err := r.Step(StepOpts{Title: fmt.Sprintf("Fix %d", round), Prefs: pref("fixer"), Mode: "write", Prompt: `You are fixing code on a team of AI coding agents. A reviewer found problems with the current changes in this folder. Address every valid finding by editing files. If you disagree with a finding, leave it and explain why. Do not commit to git and do not touch the .ensemble folder.

Reply with a short list of what you fixed and what you deliberately left.

TASK:
` + o.Task + `

PLAN:
` + plan.Output + `

REVIEW:
` + review.Output + "\n" + failing})
		if err != nil {
			return nil, err
		}
		if !contains(fixers, fix.Label) {
			fixers = append(fixers, fix.Label)
		}
	}

	// 4. Commit on the work branch
	commit := ""
	stat, _ := currentDiff(o.Dir, o.Git.StartRef)
	if stat != "" && !gitOK(o.Dir, "diff", "--cached", "--quiet") {
		firstLine := strings.SplitN(o.Task, "\n", 2)[0]
		if len(firstLine) > 60 {
			firstLine = firstLine[:60]
		}
		reviewed := ""
		if lastReview != nil {
			reviewed = fmt.Sprintf(", reviewed by %s (%s)", lastReview.Label, verdict)
		}
		msg := fmt.Sprintf("Ensemble: %s\n\nPlanned by %s, built by %s%s.", firstLine, plan.Label, build.Label, reviewed)
		if _, err := git(o.Dir, append(ensembleIdentity, "commit", "-q", "-m", msg)...); err != nil {
			r.Note("Could not commit: "+err.Error(), "warn")
		} else {
			commit, _ = git(o.Dir, "rev-parse", "--short", "HEAD")
		}
	}

	verdictText := map[string]string{
		"approved":   "Approved by the reviewer.",
		"changes":    "The reviewer still requested changes; see the last review.",
		"unclear":    "The reviewer did not give a clear verdict.",
		"no-changes": "No files were changed.",
	}[verdict]
	var b strings.Builder
	fmt.Fprintf(&b, "# Build: %s\n\n**Result:** %s", r.Title, verdictText)
	if tests != nil {
		if tests.OK {
			b.WriteString(" Tests pass.")
		} else {
			b.WriteString(" Tests fail.")
		}
	}
	branch := o.Git.Branch
	if branch == "" {
		branch = "(current)"
	}
	fmt.Fprintf(&b, "\n**Branch:** `%s`", branch)
	if commit != "" {
		fmt.Fprintf(&b, " · commit `%s`", commit)
	}
	b.WriteString("\n\n| Step | Model |\n|---|---|\n")
	if findings != nil {
		fmt.Fprintf(&b, "| Research | %s |\n", findings.Label)
	}
	reviewer := "—"
	if lastReview != nil {
		reviewer = lastReview.Label
	}
	fmt.Fprintf(&b, "| Plan | %s |\n| Build | %s |\n| Review | %s |\n", plan.Label, build.Label, reviewer)
	if len(fixers) > 0 {
		fmt.Fprintf(&b, "| Fix | %s |\n", strings.Join(fixers, ", "))
	}
	if stat == "" {
		stat = "none"
	}
	fmt.Fprintf(&b, "\n## Files changed\n```\n%s\n```\n\n## Builder's notes\n%s\n\n", stat, build.Output)
	if lastReview != nil {
		demoted := regexp.MustCompile(`(?m)^#{1,3} `).ReplaceAllString(lastReview.Output, "#### ")
		fmt.Fprintf(&b, "## Last review\n%s\n\n", demoted)
	}
	b.WriteString("## Next steps\n")
	if o.Git.Branch != "" && o.Git.OriginalBranch != "" && o.Git.Branch != o.Git.OriginalBranch {
		fmt.Fprintf(&b, "Look over the branch, then merge it: `git checkout %s && git merge %s`, or push it and open a pull request.\n", o.Git.OriginalBranch, o.Git.Branch)
	} else {
		b.WriteString("Look over the changes in your project folder.\n")
	}
	return &RunResult{Markdown: b.String(), Verdict: verdict, Branch: o.Git.Branch, Commit: commit}, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
