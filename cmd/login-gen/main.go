package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// login-gen performs an interactive login from a trusted (non-datacenter) IP
// and prints the resulting session JSON to stdout. Paste that JSON into the
// bot via /import_session.
//
// Usage: login-gen <api_id> <api_hash> <phone> [session_file]
func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: login-gen <api_id> <api_hash> <phone> [session_file]")
		os.Exit(2)
	}
	apiID, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fatal(err)
	}
	apiHash := os.Args[2]
	phone := normalizePhone(os.Args[3])
	path := "session.json"
	if len(os.Args) >= 5 {
		path = os.Args[4]
	}

	client := telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: path},
	})

	ua := &stdinAuth{phone: phone}
	flow := auth.NewFlow(ua, auth.SendCodeOptions{})

	if err := client.Run(context.Background(), func(ctx context.Context) error {
		return client.Auth().IfNecessary(ctx, flow)
	}); err != nil {
		fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(data))
}

type stdinAuth struct {
	phone string
}

func (a *stdinAuth) Phone(context.Context) (string, error) { return a.phone, nil }

func (a *stdinAuth) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	fmt.Print("Enter code: ")
	s, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s), err
}

func (a *stdinAuth) Password(_ context.Context) (string, error) {
	fmt.Print("Enter 2FA password: ")
	s, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s), err
}

func (a *stdinAuth) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	return fmt.Errorf("terms-of-service confirmation is not supported")
}

func (a *stdinAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("sign-up is not supported")
}

func normalizePhone(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
