package config

import (
	"fmt"
	"sort"
	"strings"
)

func ResolvedModelDir(modelName string, model Model) string {
	if dir := strings.TrimSpace(model.ModelDir); dir != "" {
		return dir
	}
	return modelName
}

func validateModelIdentities(cfg GatewayConfig) error {
	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		names = append(names, name)
	}
	sort.Strings(names)

	dirs := map[string]string{}
	for _, name := range names {
		model := cfg.Models[name]
		dir := ResolvedModelDir(name, model)
		if model.ModelDir != "" && (dir == "" || dir == "." || dir == ".." || dir != model.ModelDir || strings.ContainsAny(dir, "/\\:")) {
			return fmt.Errorf("model %s model_dir must be a safe relative directory name", name)
		}
		if previous, exists := dirs[dir]; exists {
			return fmt.Errorf("models %s and %s resolve to duplicate model_dir %s", previous, name, dir)
		}
		dirs[dir] = name
	}

	aliases := make([]string, 0, len(cfg.ModelAliases))
	for alias := range cfg.ModelAliases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		rawTarget := cfg.ModelAliases[alias]
		target := strings.TrimSpace(rawTarget)
		if alias == "" || alias != strings.TrimSpace(alias) || target == "" || target != rawTarget {
			return fmt.Errorf("model_aliases entries require non-empty trimmed alias and target")
		}
		if _, exists := cfg.Models[alias]; exists {
			return fmt.Errorf("model alias %s collides with model %s", alias, alias)
		}
		if _, exists := cfg.Models[target]; !exists {
			return fmt.Errorf("model alias %s target %s is not defined", alias, target)
		}
	}
	return nil
}
