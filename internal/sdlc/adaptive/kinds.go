package adaptive

// TaskKinds are routing contexts for the adaptive loop, not fixed graphs.
var TaskKinds = []string{"bugfix", "feature", "release", "review"}

var taskKindDescriptions = map[string]string{
	"bugfix":  "Investigate a defect, implement a fix, and assess the resulting change.",
	"feature": "Plan and implement a new capability, then assess the resulting change.",
	"release": "Prepare release work, implement needed changes, and assess the result.",
	"review":  "Examine an existing change or review request and assess a supplied diff when available.",
}

var taskKindSummaries = map[string]string{
	"bugfix":  "Fix broken behavior",
	"feature": "Build a new capability",
	"release": "Prepare a release",
	"review":  "Review an existing change",
}

func TaskKindDescription(kind string) string { return taskKindDescriptions[kind] }
func TaskKindSummary(kind string) string     { return taskKindSummaries[kind] }
