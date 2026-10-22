package main

// Council: every model answers on its own, then reads the others and revises,
// then a chair merges the best of all of them into one answer.
// Good for writing, research, decisions, brainstorming and second opinions.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Attachment struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type CouncilOpts struct {
	Question    string
	Agents      []string
	Rounds      int
	Chair       string
	Attachments []Attachment
}

func RunCouncil(r *Run, o CouncilOpts) (*RunResult, error) {
	members := []string{}
	for _, id := range o.Agents {
		if r.CanUse(id, "read") {
			members = append(members, id)
		}
	}
	if len(members) == 0 {
		return nil, errors.New("No agents are ready. Open the Setup checklist to see what needs installing or signing in.")
	}
	if len(members) == 1 {
		r.Note(fmt.Sprintf("Only %s is ready, so this is a solo answer rather than a council.", r.Label(members[0])), "warn")
	}
	context := ""
	if len(o.Attachments) > 0 {
		var b strings.Builder
		b.WriteString("\n\nReference material:\n")
		for _, a := range o.Attachments {
			fmt.Fprintf(&b, "\n--- %s ---\n%s\n", a.Name, a.Text)
		}
		context = b.String()
	}
	labels := []string{}
	for _, id := range members {
		labels = append(labels, r.Label(id))
	}
	roster := strings.Join(labels, ", ")

	r.Phase("Independent answers", roster)
	answers := parallelSteps(r, members, func(id string) StepOpts {
		return StepOpts{Title: "Answer", Prefs: []string{id}, NoFallback: true, Prompt: fmt.Sprintf(
			`You are %s, one member of a panel of AI models (%s) answering the same request. Answer independently and as well as you can. Be concrete. Say where you are unsure rather than guessing. Do not create or edit any files; reply in plain markdown only.

REQUEST:
%s%s`, r.Label(id), roster, o.Question, context)}
	})
	if r.Cancelled() {
		return nil, errCancelled
	}
	if len(answers) == 0 {
		return nil, errors.New("No agent produced an answer.")
	}

	for round := 2; round <= o.Rounds && len(answers) > 1; round++ {
		r.Phase(fmt.Sprintf("Cross-review round %d", round-1), "Each model reads the others and revises")
		prior := answers
		ids := []string{}
		for _, a := range prior {
			ids = append(ids, a.Agent)
		}
		revised := parallelSteps(r, ids, func(id string) StepOpts {
			mine := "(you did not answer last round)"
			others := []string{}
			for _, a := range prior {
				if a.Agent == id {
					mine = a.Output
				} else {
					others = append(others, "### "+a.Label+"\n"+a.Output)
				}
			}
			return StepOpts{Title: fmt.Sprintf("Revise (round %d)", round-1), Prefs: []string{id}, NoFallback: true, Prompt: fmt.Sprintf(
				`You are %s on a panel of AI models. Below is the original request, your previous answer, and the other models' answers.

1. Under "## Critique", list concrete errors, gaps or weak reasoning in the OTHER answers (and in your own). Be specific and fair; credit what they got right.
2. Under "## Revised answer", give your improved full answer, adopting anything better from the others.
Do not create or edit files; reply in markdown only.

REQUEST:
%s%s

YOUR PREVIOUS ANSWER:
%s

OTHER ANSWERS:
%s`, r.Label(id), o.Question, context, mine, strings.Join(others, "\n\n"))}
		})
		if r.Cancelled() {
			return nil, errCancelled
		}
		if len(revised) > 0 {
			answers = revised
		}
	}

	r.Phase("Synthesis", "The chair merges everything")
	var final StepResult
	if len(answers) == 1 {
		final = answers[0]
	} else {
		prefs := append([]string{o.Chair}, r.Config.Roles["chair"]...)
		prefs = append(prefs, members...)
		panel := []string{}
		for _, a := range answers {
			panel = append(panel, "### "+a.Label+"\n"+a.Output)
		}
		var err error
		final, err = r.Step(StepOpts{Title: "Final synthesis", Prefs: prefs, Prompt: fmt.Sprintf(
			`You are chairing a panel of AI models (%s). Write the single best final answer to the request below, using the panel's answers. Keep what is correct and well supported, drop what was refuted, and resolve disagreements explicitly. Do not mention the panel process except in the last section.

Format:
- The final answer, complete and ready to use.
- A short final section "## Where the models disagreed" listing any open disagreements and which view you went with and why (write "None of note." if none).

Do not create or edit files; reply in markdown only.

REQUEST:
%s%s

PANEL ANSWERS:
%s`, roster, o.Question, context, strings.Join(panel, "\n\n"))})
		if err != nil {
			return nil, err
		}
	}
	md := fmt.Sprintf("# %s\n\n%s\n\n---\n*Council: %s. Synthesized by %s.*\n", r.Title, final.Output, roster, final.Label)
	_ = os.WriteFile(filepath.Join(r.RunDir, "final.md"), []byte(md), 0o644)
	return &RunResult{Markdown: md}, nil
}

// parallelSteps runs one step per agent at the same time and keeps the
// successful results in the original order. Failures are noted, not fatal.
func parallelSteps(r *Run, ids []string, mk func(id string) StepOpts) []StepResult {
	results := make([]*StepResult, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			res, err := r.Step(mk(id))
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = &res
		}(i, id)
	}
	wg.Wait()
	out := []StepResult{}
	for i, res := range results {
		if res != nil {
			out = append(out, *res)
		} else if errs[i] != nil && !r.Cancelled() {
			r.Note(errs[i].Error(), "warn")
		}
	}
	return out
}
