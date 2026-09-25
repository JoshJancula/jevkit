package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sdlcProjectInputs lets a task file inside a repository identify the project
// when the command was launched from a non-repository parent directory.
func (a *App) sdlcProjectInputs(name, taskFile string, files []string) (string, string, []string, func(), error) {
	old := a.WorkDir
	root, err := gitWorktreeRoot(old)
	if err == nil {
		return name, taskFile, files, func() {}, nil
	}
	hint := taskFile
	if (hint == "" || hint == "-") && len(files) > 0 {
		hint, _, _ = strings.Cut(files[0], "=")
	}
	if hint != "" && hint != "-" {
		absolute := hint
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(old, absolute)
		}
		if _, statErr := os.Stat(absolute); statErr == nil {
			root, err = gitWorktreeRoot(filepath.Dir(absolute))
			if err == nil {
				if name != "" && !filepath.IsAbs(name) {
					if _, statErr := os.Stat(filepath.Join(old, name)); statErr == nil {
						name = filepath.Join(old, name)
					}
				}
				resolved := make([]string, len(files))
				for i, raw := range files {
					path, artifact, named := strings.Cut(raw, "=")
					if !filepath.IsAbs(path) {
						path = filepath.Join(old, path)
					}
					resolved[i] = path
					if named {
						resolved[i] += "=" + artifact
					}
				}
				a.WorkDir = root
				resolvedTask := taskFile
				if taskFile != "" && taskFile != "-" {
					resolvedTask = absolute
				}
				return name, resolvedTask, resolved, func() { a.WorkDir = old }, nil
			}
		}
	}
	return name, taskFile, files, func() {}, nil
}

func gitWorktreeRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitHasHEAD(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = dir
	return cmd.Run() == nil
}
