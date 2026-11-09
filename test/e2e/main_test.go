//go:build e2e

// Package e2e runs the demo scenario against two k3d clusters that share
// one table. The build tag keeps it out of the unit tests; scripts/e2e.sh
// runs it.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/phoban01/solas/pkg/controller/scheme"
)

var (
	testenv env.Environment
	root    = mustAbs("../..")
)

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return a
}

func TestMain(m *testing.M) {
	// SOLAS_STORE picks the shared store, dynamodb or etcd. join.sh reads
	// it, so both clusters use the same store.
	switch s := os.Getenv("SOLAS_STORE"); s {
	case "", "dynamodb", "etcd":
		fmt.Printf("e2e: store %s\n", map[bool]string{true: "dynamodb", false: s}[s == ""])
	default:
		fmt.Fprintf(os.Stderr, "e2e: SOLAS_STORE=%q is not dynamodb or etcd\n", s)
		os.Exit(2)
	}
	testenv = env.New()
	testenv.Setup(
		script("scripts/images.sh"),
		script("demo/k3d/mesh.sh", "up"),
		script("demo/k3d/join.sh", "a"),
		script("demo/k3d/join.sh", "b"),
	)
	// down.sh deletes only the e2e- clusters, the gatekeeper, and the mesh.
	testenv.Finish(script("demo/k3d/down.sh"))
	os.Exit(testenv.Run(m))
}

// script runs a repository script as a setup or finish step.
func script(path string, args ...string) env.Func {
	return func(ctx context.Context, _ *envconf.Config) (context.Context, error) {
		cmd := exec.CommandContext(ctx, filepath.Join(root, path), args...)
		cmd.Dir = root
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return ctx, fmt.Errorf("%s %v: %w", path, args, err)
		}
		return ctx, nil
	}
}

func kubeconfig(cluster string) string {
	return filepath.Join(root, "demo/k3d/.kube", cluster)
}

// clientFor returns a client for one member cluster.
func clientFor(t *testing.T, cluster string) client.Client {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig(cluster))
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme.New()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// run runs a command with the repository as the working directory.
func run(t *testing.T, stdin string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	if stdin != "" {
		cmd.Stdin = stringsReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// execCommand runs a command with the repository as the working directory
// and returns its combined output.
func execCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// execCommandStdin runs a command with stdin and the repository as the
// working directory, and returns its combined output.
func execCommandStdin(stdin, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	cmd.Stdin = stringsReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
