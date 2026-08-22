package cli

import (
	"strings"
	"testing"
)

// --secret and --clear-secret are the two non-default arms of the three-way
// secret (omit keeps, string rotates, clear sends null); both at once has no
// meaning and must be a usage error before any credential is consulted.
func TestEventWebhookSetRejectsSecretAndClear(t *testing.T) {
	for _, scope := range []eventWebhookScope{eventWebhookMailbox, eventWebhookDomain} {
		a := &app{out: newPrinter(false, true)}
		cmd := newEventWebhookSetCmd(a, scope)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		args := []string{"--url", "https://recv.example/h", "--secret", "s", "--clear-secret"}
		if scope == eventWebhookDomain {
			args = append([]string{"acme.example"}, args...)
		}
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("%v: err = %v; want the flags refused as mutually exclusive", scope, err)
		}
	}
}

// `set` without --url is a usage error: a hook IS its URL, and "keep the URL"
// is not a state the PUT can express.
func TestEventWebhookSetRequiresURL(t *testing.T) {
	a := &app{out: newPrinter(false, true)}
	cmd := newEventWebhookSetCmd(a, eventWebhookMailbox)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"--secret", "s"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--url") {
		t.Fatalf("err = %v; want --url required", err)
	}
}
