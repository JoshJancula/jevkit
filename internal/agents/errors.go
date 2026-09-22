package agents

import "fmt"

func errUnknownAgent(name string) error {
	return fmt.Errorf("unknown agent %q", name)
}

func errNeedWorkDir(name string) error {
	return fmt.Errorf("%s install: project scope requires WorkDir", name)
}

func errNeedHome(name string) error {
	return fmt.Errorf("%s install: user scope requires ConfigDir (home)", name)
}

func errUnknownScope(name, scope string) error {
	return fmt.Errorf("%s install: unknown scope %q", name, scope)
}
