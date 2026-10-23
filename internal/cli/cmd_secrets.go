package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hyperlab-sh/husk/internal/cli/output"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func secretsCommand() command {
	set := serviceMethod("husk secrets set <name>", protocol.MethodSecretsSet, []string{"name"}, renderSecretEmpty)
	set.run = runSecretsSet
	return command{usage: "husk secrets", verbs: map[string]command{
		"list":   serviceMethod("husk secrets list", protocol.MethodSecretsNames, nil, renderSecretsList),
		"set":    set,
		"grant":  serviceMethod("husk secrets grant <plugin> <name>", protocol.MethodSecretsGrant, []string{"plugin", "name"}, renderSecretEmpty),
		"revoke": serviceMethod("husk secrets revoke <plugin> <name>", protocol.MethodSecretsRevoke, []string{"plugin", "name"}, renderSecretEmpty),
		"reveal": serviceMethod("husk secrets reveal <name>", protocol.MethodSecretsReveal, []string{"name"}, renderSecretReveal),
	}}
}

func runSecretsSet(ctx context.Context, inv *Invocation, args []string) error {
	if len(args) != 1 {
		return output.Usage("secrets set takes one <name>; value is read from stdin")
	}
	var value []byte
	var err error
	if inv.env.StdinIsTTY {
		if inv.env.ReadPassword == nil {
			return output.Usage("secret terminal reader unavailable")
		}
		if _, err = fmt.Fprintf(inv.out.Stderr, "secret for %s: ", output.Text(args[0])); err != nil {
			return err
		}
		value, err = inv.env.ReadPassword()
		if _, newlineErr := fmt.Fprintln(inv.out.Stderr); err == nil {
			err = newlineErr
		}
	} else {
		value, err = io.ReadAll(io.LimitReader(inv.env.Stdin, protocol.MaxLineBytes+1))
	}
	// Never include a reader's error text: an injected reader may echo input.
	if err != nil {
		return output.Usage("could not read secret from stdin")
	}
	if !utf8.Valid(value) {
		return output.Usage("secret stdin must be valid UTF-8")
	}
	if len(value) > protocol.MaxLineBytes {
		return output.Usage("request exceeds the 4 MiB line limit")
	}
	if bytes.HasSuffix(value, []byte("\r\n")) {
		value = value[:len(value)-2]
	} else {
		value = bytes.TrimSuffix(value, []byte("\n"))
	}
	raw, err := inv.call(ctx, protocol.MethodSecretsSet, map[string]string{"name": args[0], "value": string(value)})
	if err != nil {
		return err
	}
	return renderSecretEmpty(inv, raw)
}

func renderSecretEmpty(inv *Invocation, raw json.RawMessage) error {
	if inv.out.Format == output.JSON {
		return inv.out.Result(raw)
	}
	if strings.TrimSpace(string(raw)) == "{}" {
		return nil
	}
	return inv.out.Result(raw)
}

func renderSecretReveal(inv *Invocation, raw json.RawMessage) error {
	if inv.out.Format == output.JSON {
		return inv.out.Result(raw)
	}
	var res struct {
		Value string `json:"value"`
	}
	if err := decodeService(raw, &res); err != nil {
		return err
	}
	// Reveal is intentionally a bare string result, including its contents.
	value, _ := json.Marshal(res.Value)
	return inv.out.Result(value)
}

func renderSecretsList(inv *Invocation, raw json.RawMessage) error {
	if inv.out.Format == output.JSON {
		return inv.out.Result(raw)
	}
	var res struct {
		Storage string `json:"storage"`
		Secrets []struct {
			Name     string   `json:"name"`
			Declared []string `json:"declared"`
			Granted  []string `json:"granted"`
			Set      *bool    `json:"set"`
			Required bool     `json:"required"`
		} `json:"secrets"`
	}
	if err := decodeService(raw, &res); err != nil {
		return err
	}
	if err := output.WriteRows(inv.out.Stdout, [][]string{{"storage:", output.Text(res.Storage)}}); err != nil {
		return err
	}
	rows := make([][]string, 0, len(res.Secrets))
	for _, s := range res.Secrets {
		set := ""
		if s.Set != nil {
			set = strconv.FormatBool(*s.Set)
		}
		rows = append(rows, []string{s.Name, strings.Join(s.Declared, ","), strings.Join(s.Granted, ","), set, strconv.FormatBool(s.Required)})
	}
	return serviceTable(inv, []string{"NAME", "DECLARED", "GRANTED", "SET", "REQUIRED"}, rows)
}
