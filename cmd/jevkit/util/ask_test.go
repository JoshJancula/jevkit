package util

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/cmd/jevkit/internal/testkit"
	"github.com/JoshJancula/jevkit/internal/jev"
)

func TestAskDetailedChoiceCriteria(t *testing.T) {
	a, _, fj := cliApp(t)
	mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"answer": jev.ChoiceAnswer{Choice: "billing", Confidence: 0.8}}}
	out, _ := mustRun(t, a, "", 0, "ask", "choice",
		"--state", "customer email: token="+testkit.SecretKey,
		"--question", "route to which team?",
		"--criteria-json", `{"billing":"Payment, invoice or refund issues: `+testkit.SecretKey+`","technical":"Product defects","sales":"Pricing questions"}`)
	if !strings.Contains(out, "choice: billing") {
		t.Fatalf("out=%q", out)
	}
	choice, ok := fj.Req.Questions["answer"].(jev.ChoiceQuestion)
	if !ok || len(choice.Criteria) != 3 {
		t.Fatalf("wrong question: %#v", fj.Req.Questions["answer"])
	}
	if string(choice.Criteria["technical"]) != `"Product defects"` {
		t.Errorf("technical criteria = %s", choice.Criteria["technical"])
	}
	if strings.Contains(string(choice.Criteria["billing"]), testkit.SecretKey) {
		t.Errorf("criteria value not redacted: %s", choice.Criteria["billing"])
	}
	if !strings.Contains(string(choice.Criteria["billing"]), "[REDACTED]") {
		t.Errorf("criteria value missing redaction marker: %s", choice.Criteria["billing"])
	}

	// --options and --criteria-json are mutually exclusive.
	if code, _, _ := run(a, "", "ask", "choice", "--state", "x", "--question", "q", "--options", "a,b", "--criteria-json", `{"a":null}`); code != app.ExitUsage {
		t.Errorf("options+criteria-json: exit=%d", code)
	}
	// Malformed JSON is rejected before any question is built.
	if code, _, _ := run(a, "", "ask", "choice", "--state", "x", "--question", "q", "--criteria-json", `{not json`); code != app.ExitUsage {
		t.Errorf("malformed criteria-json: exit=%d", code)
	}
	// An empty object is not a valid choice criteria set.
	if code, _, _ := run(a, "", "ask", "choice", "--state", "x", "--question", "q", "--criteria-json", `{}`); code != app.ExitUsage {
		t.Errorf("empty criteria-json: exit=%d", code)
	}
}

func TestAskDetailedScoreCriteria(t *testing.T) {
	a, _, fj := cliApp(t)
	mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"answer": jev.ScoreAnswer{Score: 2, Confidence: 0.7}}}
	out, _ := mustRun(t, a, "", 0, "ask", "score",
		"--state", "diff stats",
		"--question", "how risky?",
		"--criteria-json", `[{"label":"low","rubric":"docs only"},{"label":"medium","rubric":"app code"},{"label":"high","rubric":"auth/billing"}]`)
	if !strings.Contains(out, "score: 2.000000") {
		t.Fatalf("out=%q", out)
	}
	score, ok := fj.Req.Questions["answer"].(jev.ScoreQuestion)
	if !ok || len(score.Criteria) != 3 {
		t.Fatalf("wrong question: %#v", fj.Req.Questions["answer"])
	}
	var first map[string]string
	if err := json.Unmarshal(score.Criteria[0], &first); err != nil || first["label"] != "low" {
		t.Errorf("first level = %s", score.Criteria[0])
	}

	if code, _, _ := run(a, "", "ask", "score", "--state", "x", "--question", "q", "--levels", "a,b", "--criteria-json", `["a","b"]`); code != app.ExitUsage {
		t.Errorf("levels+criteria-json: exit=%d", code)
	}
	if code, _, _ := run(a, "", "ask", "score", "--state", "x", "--question", "q", "--criteria-json", `["only-one"]`); code != app.ExitUsage {
		t.Errorf("score criteria as non-array-min: exit=%d", code)
	}
}

func TestAskDetailedNoulCriteria(t *testing.T) {
	a, _, fj := cliApp(t)
	mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"answer": jev.NoulAnswer{Noul: 0.9}}}
	out, _ := mustRun(t, a, "", 0, "ask", "noul",
		"--state", "ci output",
		"--question", "safe to deploy?",
		"--criteria-json", `{"true":{"rubric":"no failures"}}`)
	if !strings.Contains(out, "noul: 0.900000") {
		t.Fatalf("out=%q", out)
	}
	noul, ok := fj.Req.Questions["answer"].(jev.NoulQuestion)
	if !ok || len(noul.Criteria) != 1 {
		t.Fatalf("wrong question: %#v", fj.Req.Questions["answer"])
	}
	if string(noul.Criteria["true"]) != `{"rubric":"no failures"}` {
		t.Errorf("true criteria = %s", noul.Criteria["true"])
	}

	if code, _, _ := run(a, "", "ask", "noul", "--state", "x", "--question", "q", "--true-criteria", "y", "--criteria-json", `{"true":"y"}`); code != app.ExitUsage {
		t.Errorf("true-criteria+criteria-json: exit=%d", code)
	}
}

func TestAskStructuredStateAndInstructions(t *testing.T) {
	a, _, fj := cliApp(t)
	mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"answer": jev.NoulAnswer{Noul: 0.5}}}
	mustRun(t, a, "", 0, "ask", "noul",
		"--state-json", `{"tests":42,"secret":"`+testkit.SecretKey+`"}`,
		"--instructions-json", `["Did it pass?","Explain briefly."]`)
	if strings.Contains(fj.Req.State, testkit.SecretKey) {
		t.Fatalf("state leaked the secret: %q", fj.Req.State)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(fj.Req.State), &state); err != nil {
		t.Fatalf("state is not valid JSON: %q: %v", fj.Req.State, err)
	}
	if state["tests"] != float64(42) {
		t.Errorf("state shape changed: %v", state)
	}
	noul, ok := fj.Req.Questions["answer"].(jev.NoulQuestion)
	if !ok {
		t.Fatalf("wrong question: %#v", fj.Req.Questions["answer"])
	}
	var instr []string
	if err := json.Unmarshal([]byte(noul.Instructions), &instr); err != nil || len(instr) != 2 {
		t.Fatalf("instructions not a JSON array: %q", noul.Instructions)
	}

	// --state and --state-json are mutually exclusive.
	if code, _, _ := run(a, "", "ask", "noul", "--state", "x", "--state-json", `{"a":1}`, "--question", "q"); code != app.ExitUsage {
		t.Errorf("state+state-json: exit=%d", code)
	}
	// Neither --state nor a variant supplied.
	if code, _, _ := run(a, "", "ask", "noul", "--question", "q"); code != app.ExitUsage {
		t.Errorf("missing state: exit=%d", code)
	}
	// Malformed --state-json is rejected before transport.
	if code, _, _ := run(a, "", "ask", "noul", "--state-json", `{not json`, "--question", "q"); code != app.ExitUsage {
		t.Errorf("malformed state-json: exit=%d", code)
	}
}

func TestAskStateAndInstructionsFromFile(t *testing.T) {
	a, _, fj := cliApp(t)
	mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")
	fj.Resp = &jev.Response{Answers: map[string]jev.Answer{"answer": jev.NoulAnswer{Noul: 0.5}}}
	stateFile := filepath.Join(t.TempDir(), "state.txt")
	testkit.WriteFile(t, stateFile, "line one\nline two token="+testkit.SecretKey)
	mustRun(t, a, "", 0, "ask", "noul", "--state-file", stateFile, "--question", "safe?")
	if strings.Contains(fj.Req.State, testkit.SecretKey) || !strings.Contains(fj.Req.State, "[REDACTED]") {
		t.Fatalf("state-file not redacted: %q", fj.Req.State)
	}

	// stdin via "-".
	a2, _, fj2 := cliApp(t)
	mustRun(t, a2, testkit.SecretKey+"\n", 0, "key", "set")
	fj2.Resp = &jev.Response{Answers: map[string]jev.Answer{"answer": jev.NoulAnswer{Noul: 0.5}}}
	mustRun(t, a2, "state from stdin", 0, "ask", "noul", "--state-file", "-", "--question", "safe?")
	if !strings.Contains(fj2.Req.State, "state from stdin") {
		t.Fatalf("stdin state not used: %q", fj2.Req.State)
	}
}

func TestAskRequestMultiQuestion(t *testing.T) {
	body := `{
		"model": "jev-1.13.0",
		"state": "42 tests passed; 0 failed; token=` + testkit.SecretKey + `",
		"questions": {
			"outcome": {"type": "choice", "instructions": "What is the result?",
				"criteria": {"pass": null, "fail": null}},
			"risk": {"type": "score", "instructions": "How risky is this ` + testkit.SecretKey + `?",
				"criteria": ["low", "medium", "high"]},
			"flaky": {"type": "noul", "instructions": "Any flaky tests?"}
		}
	}`

	t.Run("from a file", func(t *testing.T) {
		a, _, fj := cliApp(t)
		mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")
		fj.Resp = &jev.Response{
			Model: "jev-1.13.0",
			Usage: jev.Usage{InputTokens: 10, OutputTokens: 2},
			Answers: map[string]jev.Answer{
				"outcome": jev.ChoiceAnswer{Choice: "pass", Confidence: 0.9, Probabilities: map[string]float64{"pass": 0.9, "fail": 0.1}},
				"risk":    jev.ScoreAnswer{Score: 1, Confidence: 0.8, Legend: []string{"low", "medium", "high"}, Distribution: map[string]float64{"low": 0.1, "medium": 0.7, "high": 0.2}},
				"flaky":   jev.NoulAnswer{Noul: 0.05},
			},
		}
		file := filepath.Join(t.TempDir(), "request.json")
		testkit.WriteFile(t, file, body)
		out, errs := mustRun(t, a, "", 0, "ask", "request", "--file", file)
		testkit.NoSecret(t, "ask request", out, errs)
		for _, want := range []string{"outcome:", "choice: pass", "risk:", "score: 1.000000", "legend: low, medium, high", "flaky:", "noul: 0.050000"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if fj.Req.Model != "jev-1.13.0" {
			t.Errorf("model = %q", fj.Req.Model)
		}
		if strings.Contains(fj.Req.State, testkit.SecretKey) || !strings.Contains(fj.Req.State, "[REDACTED]") {
			t.Fatalf("unredacted state: %q", fj.Req.State)
		}
		risk, ok := fj.Req.Questions["risk"].(jev.ScoreQuestion)
		if !ok || strings.Contains(risk.Instructions, testkit.SecretKey) {
			t.Fatalf("unredacted instructions: %#v", fj.Req.Questions["risk"])
		}
		outcome, ok := fj.Req.Questions["outcome"].(jev.ChoiceQuestion)
		if !ok || len(outcome.Criteria) != 2 {
			t.Fatalf("wrong outcome question: %#v", fj.Req.Questions["outcome"])
		}
		flaky, ok := fj.Req.Questions["flaky"].(jev.NoulQuestion)
		if !ok || len(flaky.Criteria) != 0 {
			t.Fatalf("wrong flaky question: %#v", fj.Req.Questions["flaky"])
		}
	})

	t.Run("model flag overrides the file", func(t *testing.T) {
		a, _, fj := cliApp(t)
		mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")
		fj.Resp = &jev.Response{Answers: map[string]jev.Answer{
			"outcome": jev.ChoiceAnswer{Choice: "pass"}, "risk": jev.ScoreAnswer{Score: 1}, "flaky": jev.NoulAnswer{Noul: 0},
		}}
		file := filepath.Join(t.TempDir(), "request.json")
		testkit.WriteFile(t, file, body)
		mustRun(t, a, "", 0, "ask", "request", "--file", file, "--model", "jev-override")
		if fj.Req.Model != "jev-override" {
			t.Errorf("model = %q", fj.Req.Model)
		}
	})

	t.Run("from stdin", func(t *testing.T) {
		a, _, fj := cliApp(t)
		mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")
		fj.Resp = &jev.Response{Model: "jev-1.13.0", Answers: map[string]jev.Answer{
			"outcome": jev.ChoiceAnswer{Choice: "pass"}, "risk": jev.ScoreAnswer{Score: 1}, "flaky": jev.NoulAnswer{Noul: 0},
		}}
		out, errs := mustRun(t, a, body, 0, "ask", "request", "--file", "-", "--format", "json")
		testkit.NoSecret(t, "ask request stdin", out, errs)
		var payload map[string]any
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("bad json output: %v: %s", err, out)
		}
		if payload["model"] != "jev-1.13.0" {
			t.Errorf("model = %v", payload["model"])
		}
		answers, ok := payload["answers"].(map[string]any)
		if !ok || len(answers) != 3 {
			t.Fatalf("answers = %#v", payload["answers"])
		}
		outcome, ok := answers["outcome"].(map[string]any)
		if !ok || outcome["type"] != "choice" || outcome["choice"] != "pass" {
			t.Errorf("outcome = %#v", outcome)
		}
	})

	t.Run("rejects malformed and missing input", func(t *testing.T) {
		a, _, _ := cliApp(t)
		mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")

		if code, _, _ := run(a, "", "ask", "request"); code != app.ExitUsage {
			t.Errorf("missing --file: exit=%d", code)
		}
		cases := map[string]string{
			"not json at all":         `not json`,
			"unknown top-level field": `{"state":"s","questions":{"a":{"type":"noul","instructions":"i"}},"bogus":1}`,
			"unknown question field":  `{"state":"s","questions":{"a":{"type":"noul","instructions":"i","bogus":1}}}`,
			"missing state":           `{"questions":{"a":{"type":"noul","instructions":"i"}}}`,
			"no questions":            `{"state":"s","questions":{}}`,
			"unknown question type":   `{"state":"s","questions":{"a":{"type":"essay","instructions":"i"}}}`,
			"choice without criteria": `{"state":"s","questions":{"a":{"type":"choice","instructions":"i"}}}`,
			"score without criteria":  `{"state":"s","questions":{"a":{"type":"score","instructions":"i"}}}`,
			"empty instructions":      `{"state":"s","questions":{"a":{"type":"noul","instructions":""}}}`,
		}
		for name, in := range cases {
			file := filepath.Join(t.TempDir(), "bad.json")
			testkit.WriteFile(t, file, in)
			if code, _, _ := run(a, "", "ask", "request", "--file", file); code != app.ExitUsage {
				t.Errorf("%s: exit=%d, want %d", name, code, app.ExitUsage)
			}
		}
	})
}

// TestAskHelpExamplesParse runs every literal `jevkit ask ...` command shown
// in each subcommand's help Example against a fake client, so the documented
// shorthand and detailed-criteria forms stay runnable as the CLI evolves.
func TestAskHelpExamplesParse(t *testing.T) {
	a, _, fj := cliApp(t)
	mustRun(t, a, testkit.SecretKey+"\n", 0, "key", "set")

	reqFile := filepath.Join(t.TempDir(), "request.json")
	testkit.WriteFile(t, reqFile, `{
		"model": "jev-latest",
		"state": "42 tests passed; 0 failed",
		"questions": {
			"outcome": {"type": "choice", "instructions": "What is the result?",
				"criteria": {"pass": null, "fail": null}},
			"risk": {"type": "score", "instructions": "How risky is this?",
				"criteria": ["low", "medium", "high"]}
		}
	}`)

	examples := map[string]string{
		"noul":    a.askTypeCmd("noul").Example,
		"choice":  a.askTypeCmd("choice").Example,
		"score":   a.askTypeCmd("score").Example,
		"request": a.askRequestCmd().Example,
	}
	for kind, example := range examples {
		t.Run(kind, func(t *testing.T) {
			fj.Resp = exampleResponse(kind)
			for _, args := range exampleCommands(t, example) {
				args = substituteArg(args, "request.json", reqFile)
				code, out, errs := run(a, "", args...)
				if code != app.ExitOK {
					t.Fatalf("%v: exit %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errs)
				}
				testkit.NoSecret(t, strings.Join(args, " "), out, errs)
			}
		})
	}
}

func exampleResponse(kind string) *jev.Response {
	switch kind {
	case "noul":
		return &jev.Response{Answers: map[string]jev.Answer{"answer": jev.NoulAnswer{Noul: 0.5}}}
	case "choice":
		return &jev.Response{Answers: map[string]jev.Answer{"answer": jev.ChoiceAnswer{Choice: "pass", Confidence: 0.9}}}
	case "score":
		return &jev.Response{Answers: map[string]jev.Answer{"answer": jev.ScoreAnswer{Score: 1, Confidence: 0.5}}}
	case "request":
		return &jev.Response{Answers: map[string]jev.Answer{
			"outcome": jev.ChoiceAnswer{Choice: "pass", Confidence: 0.9},
			"risk":    jev.ScoreAnswer{Score: 1, Confidence: 0.5},
		}}
	default:
		return &jev.Response{}
	}
}

// exampleCommands extracts every literal "jevkit ask ..." command line from
// a help Example string (joining backslash line continuations, skipping
// comments and any shell-piped line), tokenized shell-style.
func exampleCommands(t *testing.T, example string) [][]string {
	t.Helper()
	joined := strings.ReplaceAll(example, "\\\n", " ")
	var cmds [][]string
	for _, line := range strings.Split(joined, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "jevkit ") {
			continue
		}
		cmds = append(cmds, splitShellLikeExampleLine(strings.TrimPrefix(line, "jevkit ")))
	}
	return cmds
}

func splitShellLikeExampleLine(line string) []string {
	var args []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
		}
	}
	for _, r := range line {
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\'':
			inSingle = true
		case r == '"':
			inDouble = true
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return args
}

func substituteArg(args []string, from, to string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		if arg == from {
			out[i] = to
		} else {
			out[i] = arg
		}
	}
	return out
}
