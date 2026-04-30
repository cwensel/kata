package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoot_HelpListsUniversalFlags(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	out := buf.String()
	assert.Contains(t, out, "--json")
	assert.Contains(t, out, "--quiet")
	assert.Contains(t, out, "--as")
	assert.Contains(t, out, "--workspace")
}

// TestExitCodeFor_PureMapping pins the exit-code decision logic so a future
// refactor can't silently revert ExitUsage vs ExitInternal classification.
func TestExitCodeFor_PureMapping(t *testing.T) {
	assert.Equal(t, ExitUsage, exitCodeFor(assert.AnError, false),
		"cobra parse error (RunE never entered) maps to ExitUsage")
	assert.Equal(t, ExitInternal, exitCodeFor(assert.AnError, true),
		"plain RunE failure (runEEntered=true) maps to ExitInternal")
}

// TestRunEEntered_FalseOnUnknownCommand verifies cobra rejects an unknown
// command before PersistentPreRunE fires.
func TestRunEEntered_FalseOnUnknownCommand(t *testing.T) {
	resetRunEEntered(t)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"this-command-does-not-exist"})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.False(t, runEEntered, "PersistentPreRunE must not fire on unknown command")
	assert.Equal(t, ExitUsage, exitCodeFor(err, runEEntered))
}

// TestRunEEntered_FalseOnNoArgsViolation confirms the cobra.NoArgs validator
// on whoami short-circuits before PersistentPreRunE.
func TestRunEEntered_FalseOnNoArgsViolation(t *testing.T) {
	resetRunEEntered(t)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"whoami", "unexpected-positional-arg"})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.False(t, runEEntered, "NoArgs rejection must short-circuit before PersistentPreRunE")
	assert.Equal(t, ExitUsage, exitCodeFor(err, runEEntered))
}

// TestRunEEntered_TrueOnSuccessfulRunE confirms PersistentPreRunE fires when
// args/flags are valid. whoami needs no daemon, so it's a clean witness.
func TestRunEEntered_TrueOnSuccessfulRunE(t *testing.T) {
	resetRunEEntered(t)
	resetFlags(t)
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"whoami", "--as", "test-actor"})
	cmd.SetContext(context.Background())

	require.NoError(t, cmd.Execute())
	assert.True(t, runEEntered, "PersistentPreRunE should fire before whoami's RunE")
}

// TestRoot_Plan2VerbsAdvertised pins the new top-level verbs against
// cmd.Commands() (not raw help substrings) so paired commands like
// link/unlink can't mask each other's missing registration. A help-string
// substring match for "link" passes if only "unlink" is registered.
func TestRoot_Plan2VerbsAdvertised(t *testing.T) {
	cmd := newRootCmd()
	registered := make(map[string]struct{}, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		registered[sub.Name()] = struct{}{}
	}
	for _, verb := range []string{
		"link", "unlink", "parent", "unparent",
		"block", "unblock", "relate", "unrelate",
		"label", "labels",
		"assign", "unassign",
		"ready",
	} {
		_, ok := registered[verb]
		assert.Truef(t, ok, "root must register subcommand %q", verb)
	}
}

// resetRunEEntered restores the package-level sentinel via t.Cleanup so tests
// don't leak state across the shuffled order.
func resetRunEEntered(t *testing.T) {
	t.Helper()
	saved := runEEntered
	runEEntered = false
	t.Cleanup(func() { runEEntered = saved })
}
