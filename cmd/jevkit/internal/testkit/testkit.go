package testkit

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JoshJancula/jevkit/cmd/jevkit/app"
	"github.com/JoshJancula/jevkit/internal/jev"
	"github.com/JoshJancula/jevkit/internal/keystore"
	"github.com/spf13/cobra"
)

// noNetwork fails the test if anything uses the default HTTP transport.
type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("unexpected network call to %s", r.URL)
	return nil, errors.New("network is forbidden in this test")
}

func NewApp(t *testing.T) *app.App {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = noNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = old })
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	return &app.App{
		Stdin:              strings.NewReader(""),
		Environ:            []string{"HOME=" + home},
		WorkDir:            work,
		HomeDir:            home,
		ConfigDir:          filepath.Join(root, "cfg"),
		StateDir:           filepath.Join(root, "state"),
		Version:            "test",
		Binary:             "jevkit",
		SdlcSpecialistNeed: func(context.Context, string, string, string) (bool, error) { return false, nil },
	}
}

// run executes one command with fresh buffers.
// runWith executes one command with fresh buffers. commands builds the
// top-level commands after the buffers are installed.
func RunWith(a *app.App, commands func() []*cobra.Command, stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	a.Stdout, a.Stderr, a.Stdin = &out, &errb, strings.NewReader(stdin)
	code = a.Run(args, commands()...)
	return code, out.String(), errb.String()
}

func WriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func MustRunWith(t *testing.T, a *app.App, commands func() []*cobra.Command, stdin string, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	code, out, errs := RunWith(a, commands, stdin, args...)
	if code != want {
		t.Fatalf("jevkit %v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, code, want, out, errs)
	}
	return out, errs
}

const SecretKey = "sk-test-SECRET-0123456789"

// FakeKeyring is an in-memory keychain.
type FakeKeyring struct {
	Items       map[string]string
	Unavailable bool
}

func (k *FakeKeyring) id(service, account string) string { return service + "/" + account }

func (k *FakeKeyring) Get(service, account string) (string, error) {
	if k.Unavailable {
		return "", errors.New("no keychain")
	}
	v, ok := k.Items[k.id(service, account)]
	if !ok {
		return "", keystore.ErrKeyringNotFound
	}
	return v, nil
}

func (k *FakeKeyring) Set(service, account, secret string) error {
	if k.Unavailable {
		return errors.New("no keychain")
	}
	if k.Items == nil {
		k.Items = map[string]string{}
	}
	k.Items[k.id(service, account)] = secret
	return nil
}

func (k *FakeKeyring) Delete(service, account string) error {
	delete(k.Items, k.id(service, account))
	return nil
}

// fakeJev records calls and returns a canned result.
type FakeJev struct {
	Calls int
	Keys  []string
	Err   error
	Resp  *jev.Response
	Req   jev.Request
}

func (f *FakeJev) factory(t *testing.T) func(jev.Config, func() (string, error)) app.Asker {
	return func(_ jev.Config, key func() (string, error)) app.Asker {
		k, err := key()
		if err != nil {
			t.Fatalf("key func: %v", err)
		}
		f.Keys = append(f.Keys, k)
		return f
	}
}

func (f *FakeJev) Ask(_ context.Context, req jev.Request) (*jev.Response, error) {
	f.Calls++
	f.Req = req
	if f.Err != nil {
		return nil, f.Err
	}
	if f.Resp != nil {
		return f.Resp, nil
	}
	return &jev.Response{}, nil
}

// cliApp is newApp plus a fake keychain and jev client.
func CLIApp(t *testing.T) (*app.App, *FakeKeyring, *FakeJev) {
	t.Helper()
	a := NewApp(t)
	kr := &FakeKeyring{}
	fj := &FakeJev{}
	a.Keyring = kr
	a.NewJev = fj.factory(t)
	a.ReadSecret = func() ([]byte, bool, error) { return nil, false, nil }
	return a, kr, fj
}

func NoSecret(t *testing.T, what string, outs ...string) {
	t.Helper()
	for _, o := range outs {
		if strings.Contains(o, SecretKey) {
			t.Errorf("%s leaked the key:\n%s", what, o)
		}
	}
}
