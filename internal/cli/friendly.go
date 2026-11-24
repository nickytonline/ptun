package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// APIError is an error answer from the broker.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("broker: %s (%d)", e.Message, e.Status) }

// UnreachableError means the broker could not be reached at all.
type UnreachableError struct{ Err error }

func (e *UnreachableError) Error() string { return "cannot reach the tunnel service: " + e.Err.Error() }
func (e *UnreachableError) Unwrap() error { return e.Err }

// ErrNotConfigured is returned when `ptun init` has not been run.
var ErrNotConfigured = errors.New("ptun isn't set up yet. Run the setup command your admin gave you, e.g. ptun init ssh.example.com:2200")

// ErrNothingRunning is returned by the pre-flight check.
type ErrNothingRunning struct{ Target, Arg string }

func (e *ErrNothingRunning) Error() string {
	return fmt.Sprintf("Nothing is running on %s yet. Start your app, then run ptun %s again.", e.Target, e.Arg)
}

// FriendlyError rewrites technical errors into plain sentences.
func FriendlyError(err error) error {
	var api *APIError
	var unreach *UnreachableError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &api):
		switch {
		case api.Status == http.StatusForbidden && strings.Contains(api.Message, "public tunnels are disabled"):
			return errors.New("Your organization doesn't allow public links. Use ptun 3000 for a private link.")
		case api.Status == http.StatusForbidden && strings.Contains(api.Message, "MCP tunnels are disabled"):
			return errors.New("Your organization doesn't allow MCP links. Use ptun 3000 for a regular link.")
		case api.Status == http.StatusConflict && strings.Contains(api.Message, "another ptun of yours"):
			return errors.New("You already have this link running in another ptun. Stop it there (Ctrl-C) first. If that ptun crashed, the link frees itself within about 2 minutes.")
		case api.Status == http.StatusConflict && strings.Contains(api.Message, "being started"):
			return errors.New("Another ptun is starting this same link right now. Wait a moment and try again.")
		case api.Status == http.StatusConflict:
			return fmt.Errorf("That link is taken: %s. Pick another name with --name.", api.Message)
		case api.Status == http.StatusBadRequest && strings.Contains(api.Message, "name "):
			return errors.New("That name won't work in a web address. Use 1-32 letters, digits or hyphens, e.g. --name my-demo.")
		case api.Status == http.StatusTooManyRequests:
			return errors.New("You already have the maximum number of links open. Stop one (Ctrl-C in its window) and try again.")
		}
		switch api.Status {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			// Pomerium's own answer: the route exists but the broker behind it is down, not ready, or failing.
			return fmt.Errorf("The ptun service isn't responding (%s). It may not be running or may be failing; ask your admin to check the tunnel-broker.", api.Message)
		}
		return fmt.Errorf("The ptun service said no: %s", api.Message)
	case errors.As(err, &unreach):
		return errors.New("Can't reach your organization's ptun service. Check your connection, or ask your admin whether ptun is set up.")
	}
	return err
}

// CheckPort verifies something accepts TCP connections on target.
func CheckPort(ctx context.Context, target, arg string) error {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ErrNothingRunning{Target: target, Arg: arg}
	}
	_ = c.Close()
	return nil
}

// SSHRejected is the plain message for an ssh "Permission denied".
func SSHRejected(email string) error {
	who := "the account ptun is using"
	if email != "" {
		who = email + " (the account ptun is using)"
	}
	return fmt.Errorf("The SSH sign-in was rejected. Make sure you signed in as %s.", who)
}
