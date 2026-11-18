package cli

import (
	"errors"
	"fmt"
)

// Exit codes, documented so agents can branch on them (`mailgoose help
// exit-codes`). An unreachable or failing API is a plain error (1).
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
	// ExitAuth means the API key is missing, invalid or lacks permission.
	ExitAuth = 3
	// ExitNotFound means the named resource doesn't exist.
	ExitNotFound = 4
	// ExitNeedsConfirmation means the command needs an explicit confirmation,
	// such as launching a campaign without --confirm.
	ExitNeedsConfirmation = 5
)

// Error is what every command returns on failure. It carries the exit code
// and the same fields as the API's error shape, so `--json` errors look the
// same whether the CLI or the API raised them.
type Error struct {
	Exit    int
	Code    string
	Message string
	Hint    string
}

func (e *Error) Error() string { return e.Message }

// Errors the CLI raises itself.

func usageError(msg, hint string) *Error {
	return &Error{Exit: ExitUsage, Code: "usage", Message: msg, Hint: hint}
}

func authError(msg, hint string) *Error {
	return &Error{Exit: ExitAuth, Code: "unauthenticated", Message: msg, Hint: hint}
}

// asError turns any error into an *Error: cobra's own usage errors become
// exit 2, anything else exit 1.
func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if isUsageError(err) {
		return usageError(err.Error(), "Run the command with --help to see its usage.")
	}
	return &Error{Exit: ExitError, Code: "error", Message: err.Error()}
}

// Code returns the exit code for err.
func Code(err error) int {
	if err == nil {
		return ExitOK
	}
	return asError(err).Exit
}

// needFlag is what a command returns instead of prompting: the CLI never
// waits for input, so a missing value has to come from a flag.
func needFlag(flag, what string) *Error {
	return usageError(
		fmt.Sprintf("%s is required.", what),
		fmt.Sprintf("Pass --%s. The CLI never prompts, so agents and scripts can run it unattended.", flag),
	)
}

// exitCodesHelp is the text of `mailgoose help exit-codes`.
const exitCodesHelp = `Every mailgoose command exits with one of these codes:

  0  ok                  The command did what it said.
  1  error               Anything else, including an API that can't be reached.
  2  usage               A missing or wrong argument or flag. The message names it.
  3  auth                No API key or login, or the key can't do this
                         (for example, a draft-only key launching a campaign).
  4  not found           The resource named doesn't exist.
  5  needs confirmation  The command needs an explicit confirmation, such as
                         campaign launch --confirm <code>. Show the summary to
                         the person, and run it again with the code once they
                         say yes.

With --json, errors go to stderr as
  {"error": {"code": "...", "message": "...", "hint": "..."}}
and output goes to stdout only.`
