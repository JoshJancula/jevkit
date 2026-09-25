package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OWNER/jevkit/internal/globmatch"
)

// Guard is a path-reference heuristic, not an operating-system sandbox.
type Guard struct {
	Workspace string
	AllowRead []string
}

var pathReference = regexp.MustCompile(`(?:^|[\s"'=;|()])((?:/|~/|\.\./|\./)[^\s"';&|()]*)`)
var parentReference = regexp.MustCompile(`(?:^|[\s"'=;|()])([^\s"';&|()]*\.\.[^\s"';&|()]*)`)

func (g Guard) Check(text string) (string, bool) {
	seen := map[string]bool{}
	for _, match := range pathReference.FindAllStringSubmatch(text, -1) {
		raw := strings.TrimRight(match[1], ",:")
		if raw == "" {
			continue
		}
		seen[raw] = true
		if violation, ok := g.CheckPath(raw); !ok {
			return violation, false
		}
	}
	for _, match := range parentReference.FindAllStringSubmatch(text, -1) {
		raw := strings.TrimRight(match[1], ",:")
		if seen[raw] || !hasParentSegment(raw) {
			continue
		}
		if violation, ok := g.CheckPath(raw); !ok {
			return violation, false
		}
	}
	return "", true
}

func hasParentSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func (g Guard) CheckPath(path string) (string, bool) {
	workspace, err := resolvePath(g.Workspace)
	if err != nil || workspace == "" {
		return "workspace is unavailable", false
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path, false
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		path = workspace + string(filepath.Separator) + path
	}
	absolute, err := resolvePath(path)
	if err != nil {
		return path, false
	}
	if within(workspace, absolute) {
		return "", true
	}
	for _, allowed := range g.AllowRead {
		if allowed == "" {
			continue
		}
		resolved := allowed
		if strings.HasPrefix(resolved, "~/") {
			home, _ := os.UserHomeDir()
			resolved = filepath.Join(home, resolved[2:])
		}
		if strings.ContainsAny(resolved, "*?") {
			re, err := globmatch.Compile(resolved)
			if err == nil && re.MatchString(absolute) {
				return "", true
			}
		} else if allow, err := resolvePath(resolved); err == nil && within(allow, absolute) {
			return "", true
		}
	}
	return fmt.Sprintf("path %q escapes workspace %q", absolute, workspace), false
}

func (g Guard) CheckWorkDir(dir string) (string, bool) {
	workspace, err := resolvePath(g.Workspace)
	if err != nil {
		return "workspace is unavailable", false
	}
	absolute, err := resolvePath(dir)
	if err != nil || !within(workspace, absolute) {
		return fmt.Sprintf("working directory %q escapes workspace %q", dir, workspace), false
	}
	return "", true
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolvePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	absolute := path
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// String concatenation preserves parent segments until symlink
		// resolution; filepath.Join would clean them too early.
		absolute = wd + string(filepath.Separator) + path
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	rest := strings.TrimPrefix(absolute, current)
	for _, segment := range strings.Split(rest, string(filepath.Separator)) {
		switch segment {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
		default:
			next := filepath.Join(current, segment)
			if real, err := filepath.EvalSymlinks(next); err == nil {
				current = real
			} else {
				current = next
			}
		}
	}
	return filepath.Clean(current), nil
}
