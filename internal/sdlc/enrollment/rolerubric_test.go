package enrollment

import "testing"

func TestRoleRubricFallsBackToGeneralRubric(t *testing.T) {
	c := []Candidate{{Agent: Agent{ID: "multi", Rubric: "General API work", Roles: []string{"planner", "assessor"}, RoleRubrics: map[string]string{"assessor": "Review API failures"}}}}
	if got := Rubrics(c, "assessor")["multi"]; got != "Review API failures" {
		t.Fatal(got)
	}
	if got := Rubrics(c, "planner")["multi"]; got != "General API work" {
		t.Fatal(got)
	}
}
