package desktopbridge

import (
	"encoding/json"
	"errors"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
)

// Local-only configuration. Never publish these executable paths/templates
// through the diagnostic export or MCP. Mode enablement is NOT enforcement.
type DeveloperProjection struct {
	Enabled            bool              `json:"enabled"`
	AllowedConnections []string          `json:"allowed_connections"`
	ExecutionAvailable bool              `json:"execution_available"`
	GateCode           string            `json:"gate_code"`
	Profiles           []json.RawMessage `json:"profiles"`
}

func projectDeveloper(cfg config.Config) DeveloperProjection {
	p := DeveloperProjection{Enabled: cfg.DeveloperMode().Enabled(), AllowedConnections: cfg.DeveloperMode().AllowedConnections(), GateCode: "network_enforcement_required", Profiles: []json.RawMessage{}}
	// The real network backend is disabled and there is no production broker.
	// Do not derive execution availability from enabled or a config field.
	data, err := cfg.MarshalJSON()
	if err == nil {
		var local struct {
			Profiles []json.RawMessage `json:"command_profiles"`
		}
		if json.Unmarshal(data, &local) == nil && local.Profiles != nil {
			p.Profiles = local.Profiles
		}
	}
	return p
}

type developerRequest struct {
	ExpectedRevision   string          `json:"expected_revision"`
	Enabled            *bool           `json:"enabled,omitempty"`
	AllowedConnections []string        `json:"allowed_connections,omitempty"`
	ID                 string          `json:"id,omitempty"`
	Profile            json.RawMessage `json:"profile,omitempty"`
}

func decodeDeveloperRequest(method string, raw json.RawMessage, p *developerRequest) bool {
	if !decodeParams(raw, p) || p.ExpectedRevision == "" {
		return false
	}
	switch method {
	case "developer.configure":
		return p.Enabled != nil && p.ID == "" && p.Profile == nil && len(p.AllowedConnections) <= 128 && (!*p.Enabled || len(p.AllowedConnections) > 0) && (*p.Enabled || len(p.AllowedConnections) == 0)
	case "developer.saveProfile":
		return p.Enabled == nil && p.AllowedConnections == nil && p.ID != "" && len(p.Profile) > 0 && len(p.Profile) <= 48<<10 && json.Valid(p.Profile)
	case "developer.removeProfile":
		return p.Enabled == nil && p.AllowedConnections == nil && p.ID != "" && p.Profile == nil
	}
	return false
}

func mutateDeveloper(cfg config.Config, method string, p developerRequest) (config.Config, error) {
	if method == "developer.configure" {
		mode, err := commandprofile.NewDeveloperMode(*p.Enabled, p.AllowedConnections, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
		if err != nil {
			return config.Config{}, err
		}
		return config.NewWithCommandProfiles(cfg.SchemaVersion(), cfg.Roots(), cfg.Profiles(), cfg.Connections(), cfg.Credentials(), cfg.EnvironmentTools(), mode, cfg.CommandProfiles())
	}
	// Reuse strict config parsing, including duplicate/unknown fields, bounded
	// argv, mandatory identity pins and root references. Parsing does not
	// establish executable identity or make a template safe to launch.
	data, err := cfg.MarshalJSON()
	if err != nil {
		return config.Config{}, err
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(data, &raw); err != nil {
		return config.Config{}, err
	}
	var profiles []json.RawMessage
	if value := raw["command_profiles"]; value != nil {
		if err = json.Unmarshal(value, &profiles); err != nil {
			return config.Config{}, err
		}
	}
	var selected struct {
		ID string `json:"id"`
	}
	if method == "developer.saveProfile" && (json.Unmarshal(p.Profile, &selected) != nil || selected.ID != p.ID) {
		return config.Config{}, config.ErrInvalid
	}
	found := false
	next := make([]json.RawMessage, 0, len(profiles)+1)
	for _, profile := range profiles {
		var existing struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal(profile, &existing); err != nil {
			return config.Config{}, err
		}
		if existing.ID == p.ID {
			found = true
			if method == "developer.saveProfile" {
				next = append(next, p.Profile)
			}
		} else {
			next = append(next, profile)
		}
	}
	if method == "developer.removeProfile" && !found {
		return config.Config{}, errors.New("command profile not found")
	}
	if method == "developer.saveProfile" && !found {
		next = append(next, p.Profile)
	}
	raw["command_profiles"], err = json.Marshal(next)
	if err != nil {
		return config.Config{}, err
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return config.Config{}, err
	}
	return config.Parse(data)
}
