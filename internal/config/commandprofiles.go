package config

import (
	"fmt"
	"time"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
)

type rawDeveloperMode struct {
	Enabled             bool     `json:"enabled"`
	AllowedConnections  []string `json:"allowed_connections"`
	DefaultConfirmation string   `json:"default_confirmation"`
	NetworkDefault      string   `json:"network_default"`
}

type rawCommandProfile struct {
	ID           string              `json:"id"`
	Kind         string              `json:"kind"`
	Platform     []string            `json:"platform"`
	Executable   string              `json:"executable"`
	Identity     rawIdentitySpec     `json:"identity"`
	Argv         rawArgvSpec         `json:"argv"`
	CWD          rawCWDSpec          `json:"cwd"`
	Environment  rawEnvironmentSpec  `json:"env"`
	Limits       rawLimitsSpec       `json:"limits"`
	Network      rawNetworkSpec      `json:"network"`
	Confirmation rawConfirmationSpec `json:"confirmation"`
	Result       rawResultSpec       `json:"result"`
}

type rawIdentitySpec struct {
	RequireRegular bool   `json:"require_regular"`
	RejectReparse  bool   `json:"reject_reparse"`
	SHA256         string `json:"sha256"`
}

type rawArgvSpec struct {
	Variants []rawVariantSpec `json:"variants"`
}

type rawVariantSpec struct {
	ID    string   `json:"variant_id"`
	Exact []string `json:"exact"`
}

type rawCWDSpec struct {
	Kind string `json:"kind"`
}

type rawEnvironmentSpec struct {
	Inherit bool              `json:"inherit"`
	Allow   []string          `json:"allow"`
	Fixed   map[string]string `json:"fixed"`
}

type rawLimitsSpec struct {
	WallTimeoutMS int `json:"wall_timeout_ms"`
	StdoutBytes   int `json:"stdout_bytes"`
	StderrBytes   int `json:"stderr_bytes"`
	MaxProcesses  int `json:"max_processes"`
	MaxChildren   int `json:"max_children"`
}

type rawNetworkSpec struct {
	Mode               string `json:"mode"`
	RequireEnforcement bool   `json:"require_enforcement"`
}

type rawConfirmationSpec struct {
	Mode      string `json:"mode"`
	LocalOnly bool   `json:"local_only"`
}

type rawResultSpec struct {
	Type            string `json:"type"`
	ReturnRawOutput bool   `json:"return_raw_output"`
}

func cloneCommandProfiles(values []commandprofile.Profile) []commandprofile.Profile {
	if values == nil {
		return nil
	}
	out := make([]commandprofile.Profile, len(values))
	for i, value := range values {
		out[i] = value.Clone()
	}
	return out
}

func parseDeveloperMode(raw rawDeveloperMode) (commandprofile.DeveloperMode, error) {
	confirmation := commandprofile.ConfirmationMode(raw.DefaultConfirmation)
	if confirmation == "" {
		confirmation = commandprofile.ConfirmationPerCall
	}
	network := commandprofile.NetworkMode(raw.NetworkDefault)
	if network == "" {
		network = commandprofile.NetworkDeny
	}
	mode, err := commandprofile.NewDeveloperMode(raw.Enabled, raw.AllowedConnections, confirmation, network)
	if err != nil {
		return commandprofile.DeveloperMode{}, fmt.Errorf("%w: developer_mode: %v", ErrInvalid, err)
	}
	return mode, nil
}

func marshalDeveloperMode(mode commandprofile.DeveloperMode) *rawDeveloperMode {
	return &rawDeveloperMode{
		Enabled:             mode.Enabled(),
		AllowedConnections:  mode.AllowedConnections(),
		DefaultConfirmation: string(mode.DefaultConfirmation()),
		NetworkDefault:      string(mode.NetworkDefault()),
	}
}

func parseCommandProfile(raw rawCommandProfile) (commandprofile.Profile, error) {
	if raw.Limits.WallTimeoutMS <= 0 || raw.Limits.WallTimeoutMS > 10000 {
		return commandprofile.Profile{}, fmt.Errorf("%w: wall_timeout_ms is outside the hard bound", ErrInvalid)
	}
	platform := make([]commandprofile.Platform, len(raw.Platform))
	for i, value := range raw.Platform {
		platform[i] = commandprofile.Platform(value)
	}
	variants := make([]commandprofile.VariantSpec, len(raw.Argv.Variants))
	for i, value := range raw.Argv.Variants {
		variants[i] = commandprofile.VariantSpec{ID: value.ID, Exact: append([]string(nil), value.Exact...)}
	}
	profile, err := commandprofile.New(commandprofile.Spec{
		ID:         raw.ID,
		Kind:       commandprofile.Kind(raw.Kind),
		Platform:   platform,
		Executable: raw.Executable,
		Identity: commandprofile.IdentitySpec{
			RequireRegular: raw.Identity.RequireRegular,
			RejectReparse:  raw.Identity.RejectReparse,
			SHA256:         raw.Identity.SHA256,
		},
		Variants: variants,
		CWD:      commandprofile.CWDSpec{Kind: commandprofile.CWDKind(raw.CWD.Kind)},
		Environment: commandprofile.EnvironmentSpec{
			Inherit: raw.Environment.Inherit,
			Allow:   append([]string(nil), raw.Environment.Allow...),
			Fixed:   cloneStringMap(raw.Environment.Fixed),
		},
		Limits: commandprofile.LimitsSpec{
			WallTimeout:  time.Duration(raw.Limits.WallTimeoutMS) * time.Millisecond,
			StdoutBytes:  raw.Limits.StdoutBytes,
			StderrBytes:  raw.Limits.StderrBytes,
			MaxProcesses: raw.Limits.MaxProcesses,
			MaxChildren:  raw.Limits.MaxChildren,
		},
		Network: commandprofile.NetworkSpec{
			Mode:               commandprofile.NetworkMode(raw.Network.Mode),
			RequireEnforcement: raw.Network.RequireEnforcement,
		},
		Confirmation: commandprofile.ConfirmationSpec{
			Mode:      commandprofile.ConfirmationMode(raw.Confirmation.Mode),
			LocalOnly: raw.Confirmation.LocalOnly,
		},
		Result: commandprofile.ResultSpec{
			Type:            commandprofile.ResultKind(raw.Result.Type),
			ReturnRawOutput: raw.Result.ReturnRawOutput,
		},
	})
	if err != nil {
		return commandprofile.Profile{}, err
	}
	return profile, nil
}

func marshalCommandProfile(profile commandprofile.Profile) rawCommandProfile {
	variants := profile.Variants()
	rawVariants := make([]rawVariantSpec, len(variants))
	for i, value := range variants {
		rawVariants[i] = rawVariantSpec{ID: value.ID(), Exact: value.Exact()}
	}
	env := profile.Environment()
	limits := profile.Limits()
	return rawCommandProfile{
		ID:         profile.ID(),
		Kind:       string(profile.Kind()),
		Platform:   platformStrings(profile.Platform()),
		Executable: profile.Executable(),
		Identity: rawIdentitySpec{
			RequireRegular: profile.Identity().RequireRegular,
			RejectReparse:  profile.Identity().RejectReparse,
			SHA256:         profile.Identity().SHA256,
		},
		Argv: rawArgvSpec{Variants: rawVariants},
		CWD:  rawCWDSpec{Kind: string(profile.CWD().Kind)},
		Environment: rawEnvironmentSpec{
			Inherit: env.Inherit,
			Allow:   append([]string(nil), env.Allow...),
			Fixed:   cloneStringMap(env.Fixed),
		},
		Limits: rawLimitsSpec{
			WallTimeoutMS: int(limits.WallTimeout / time.Millisecond),
			StdoutBytes:   limits.StdoutBytes,
			StderrBytes:   limits.StderrBytes,
			MaxProcesses:  limits.MaxProcesses,
			MaxChildren:   limits.MaxChildren,
		},
		Network: rawNetworkSpec{
			Mode:               string(profile.Network().Mode),
			RequireEnforcement: profile.Network().RequireEnforcement,
		},
		Confirmation: rawConfirmationSpec{
			Mode:      string(profile.Confirmation().Mode),
			LocalOnly: profile.Confirmation().LocalOnly,
		},
		Result: rawResultSpec{
			Type:            string(profile.Result().Type),
			ReturnRawOutput: profile.Result().ReturnRawOutput,
		},
	}
}

func platformStrings(values []commandprofile.Platform) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func validateDeveloperModeConnections(mode commandprofile.DeveloperMode, connectionIDs map[string]struct{}) error {
	if err := mode.Validate(); err != nil {
		return err
	}
	for i, connectionID := range mode.AllowedConnections() {
		if _, ok := connectionIDs[connectionID]; !ok {
			return invalid(fmt.Sprintf("developer_mode.allowed_connections[%d]", i), "unknown connection reference")
		}
	}
	return nil
}
