package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/alexblock/pig/internal/config"
)

func (a *App) cmdConfig(args []string) (int, error) {
	if len(args) == 0 {
		return ExitUsage, usagef("usage: pig config get|set|unset|validate|init")
	}
	path := a.Paths.Config()
	switch args[0] {
	case "get":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig config get <key>")
		}
		v, err := config.Get(path, args[1])
		if err != nil {
			return ExitError, err
		}
		fmt.Fprintln(a.Out, v)
		return ExitOK, nil
	case "set":
		if len(args) < 3 {
			return ExitUsage, usagef("usage: pig config set <key> <value>")
		}
		raw := strings.Join(args[2:], " ")
		v, err := config.Set(path, args[1], raw)
		if err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Out, "set %s = %s; run \"pig daemon reload\" to apply\n", args[1], config.FormatValue(v))
		return ExitOK, nil
	case "unset":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig config unset <key>")
		}
		if err := config.Unset(path, args[1]); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Out, "unset %s\n", args[1])
		return ExitOK, nil
	case "validate":
		cfg, err := config.Load(path)
		if err != nil {
			return ExitConfig, err
		}
		for _, u := range cfg.Unknown {
			fmt.Fprintf(a.Err, "pig: config: unknown key %s\n", u)
		}
		fmt.Fprintln(a.Out, "config ok")
		return ExitOK, nil
	case "init":
		return a.configInit(args[1:])
	}
	return ExitUsage, usagef("unknown config action %q", args[0])
}

var providerDefaults = map[string]struct {
	Kind      string
	BaseURL   string
	APIKeyEnv string
}{
	"mock":       {"mock", "mock://", ""},
	"anthropic":  {"anthropic", "https://api.anthropic.com", "ANTHROPIC_API_KEY"},
	"openai":     {"openai-chat", "https://api.openai.com/v1", "OPENAI_API_KEY"},
	"deepseek":   {"openai-chat", "https://api.deepseek.com/v1", "DEEPSEEK_API_KEY"},
	"openrouter": {"openai-responses", "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
}

func (a *App) configInit(args []string) (int, error) {
	fs := flag.NewFlagSet("config init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var provider, kind, baseURL, keyEnv, model, ref, input string
	var ctxWindow, maxOut int
	fs.StringVar(&provider, "provider", "mock", "provider name")
	fs.StringVar(&kind, "kind", "", "wire kind")
	fs.StringVar(&baseURL, "base-url", "", "provider base url")
	fs.StringVar(&keyEnv, "api-key-env", "", "env var holding the api key")
	fs.StringVar(&model, "model", "", "model id")
	fs.StringVar(&ref, "ref", "", "model ref")
	fs.StringVar(&input, "input", "text", "comma separated input modalities")
	fs.IntVar(&ctxWindow, "context-window", 0, "context window in tokens")
	fs.IntVar(&maxOut, "max-output", 0, "max output tokens")
	if err := fs.Parse(args); err != nil {
		return ExitUsage, usagef("%v", err)
	}
	if model == "" {
		return ExitUsage, usagef("config init: --model is required")
	}
	def, ok := providerDefaults[provider]
	if !ok {
		if kind == "" || baseURL == "" {
			return ExitUsage, usagef("config init: unknown provider %q; pass --kind and --base-url", provider)
		}
		def.Kind = kind
		def.BaseURL = baseURL
	}
	if kind == "" {
		kind = def.Kind
	}
	if baseURL == "" {
		baseURL = def.BaseURL
	}
	if keyEnv == "" {
		keyEnv = def.APIKeyEnv
	}
	if provider != "mock" {
		if ctxWindow <= 0 || maxOut <= 0 {
			return ExitUsage, usagef("config init: --context-window and --max-output are required")
		}
	} else {
		if ctxWindow <= 0 {
			ctxWindow = 128000
		}
		if maxOut <= 0 {
			maxOut = 4096
		}
	}
	inputs := []string{"text"}
	if strings.TrimSpace(input) != "" {
		inputs = strings.Split(input, ",")
		for i := range inputs {
			inputs[i] = strings.TrimSpace(inputs[i])
		}
	}
	if err := a.Paths.Ensure(); err != nil {
		return ExitStorage, err
	}
	cfg, err := config.Init(a.Paths.Config(), config.InitOptions{
		Provider:      provider,
		Kind:          kind,
		BaseURL:       baseURL,
		APIKeyEnv:     keyEnv,
		Model:         model,
		Ref:           ref,
		ContextWindow: ctxWindow,
		MaxOutput:     maxOut,
		Input:         inputs,
	})
	if err != nil {
		return ExitConfig, err
	}
	fmt.Fprintf(a.Out, "wrote %s (%d model(s))\n", cfg.Path, len(cfg.Models))
	return ExitOK, nil
}

func (a *App) cmdAuth(args []string) (int, error) {
	path := a.Paths.Auth()
	if len(args) == 0 {
		return ExitUsage, usagef("usage: pig auth set|list|rm")
	}
	switch args[0] {
	case "set":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig auth set <NAME>")
		}
		name := args[1]
		value := ""
		if len(args) > 2 {
			value = args[2]
		} else {
			if err := a.Paths.Ensure(); err != nil {
				return ExitStorage, err
			}
			v, err := readSecret(a.In, a.Err, name)
			if err != nil {
				return ExitError, err
			}
			value = v
		}
		if value == "" {
			return ExitUsage, usagef("auth set: value is empty")
		}
		if err := config.SetAuth(path, name, value); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Out, "set %s\n", name)
		return ExitOK, nil
	case "list":
		_, names, err := config.ReadAuth(path)
		if err != nil {
			return ExitError, err
		}
		for _, n := range names {
			fmt.Fprintln(a.Out, n)
		}
		return ExitOK, nil
	case "rm":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig auth rm <NAME>")
		}
		if err := config.RemoveAuth(path, args[1]); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Out, "removed %s\n", args[1])
		return ExitOK, nil
	}
	return ExitUsage, usagef("unknown auth action %q", args[0])
}

func (a *App) cmdModels(args []string) (int, error) {
	path := a.Paths.Config()
	if len(args) == 0 {
		return ExitUsage, usagef("usage: pig models add|list|rm")
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("models add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		var ref, provider, model, input, roles string
		var ctxWindow, maxOut int
		fs.StringVar(&ref, "ref", "", "model ref")
		fs.StringVar(&provider, "provider", "", "provider name")
		fs.StringVar(&model, "model", "", "model id")
		fs.StringVar(&input, "input", "text", "comma separated modalities")
		fs.StringVar(&roles, "roles", "main", "comma separated roles")
		fs.IntVar(&ctxWindow, "context-window", 0, "context window")
		fs.IntVar(&maxOut, "max-output", 0, "max output tokens")
		if err := fs.Parse(args[1:]); err != nil {
			return ExitUsage, usagef("%v", err)
		}
		if provider == "" || model == "" || ctxWindow <= 0 || maxOut <= 0 {
			return ExitUsage, usagef("models add: --provider, --model, --context-window and --max-output are required")
		}
		inputs := strings.Split(input, ",")
		for i := range inputs {
			inputs[i] = strings.TrimSpace(inputs[i])
		}
		var roleList []string
		for _, r := range strings.Split(roles, ",") {
			r = strings.TrimSpace(r)
			if r != "" {
				roleList = append(roleList, r)
			}
		}
		if ref == "" {
			ref = provider + "/" + model
		}
		cfg, err := config.AddModel(path, config.ModelSpec{
			Ref: ref, Provider: provider, Model: model,
			ContextWindow: ctxWindow, MaxOutput: maxOut, Input: inputs, Roles: roleList,
		})
		if err != nil {
			return ExitConfig, err
		}
		fmt.Fprintf(a.Out, "added %s (%d model(s) total)\n", ref, len(cfg.Models))
		return ExitOK, nil
	case "list":
		cfg, err := config.Load(path)
		if err != nil {
			return ExitConfig, err
		}
		if a.JSON {
			a.printJSON(cfg.Models)
			return ExitOK, nil
		}
		for _, m := range cfg.Models {
			fmt.Fprintf(a.Out, "%s\t%s/%s\t%d\t%d\n", m.Ref, m.Provider, m.Model, m.ContextWindow, m.MaxOutput)
		}
		return ExitOK, nil
	case "rm":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig models rm <ref>")
		}
		if _, err := config.RemoveModel(path, args[1]); err != nil {
			return ExitConfig, err
		}
		fmt.Fprintf(a.Out, "removed %s\n", args[1])
		return ExitOK, nil
	}
	return ExitUsage, usagef("unknown models action %q", args[0])
}
