package config

// ConnectionPurpose describes user intent, never authentication or permission.
type ConnectionPurpose string

// ExecutionPolicy states the isolation contract; it grants no tools itself.
type ExecutionPolicy string

const (
	ExecutionIsolatedRequired ExecutionPolicy = "isolated_required"
	ExecutionTrustedHost      ExecutionPolicy = "trusted_host"
)

func (p ExecutionPolicy) valid() bool {
	return p == ExecutionIsolatedRequired || p == ExecutionTrustedHost
}

func (c Connection) ExecutionPolicy() ExecutionPolicy {
	if c.executionPolicy == "" {
		return ExecutionIsolatedRequired
	}
	return c.executionPolicy
}

// WithExecutionPolicy is a local configuration operation. Selecting trusted_host
// acknowledges current-user OS access; independent operation approvals remain required.
func (c Connection) WithExecutionPolicy(p ExecutionPolicy) (Connection, error) {
	if !p.valid() {
		return Connection{}, invalid("connection.execution_policy", "unsupported execution policy")
	}
	c.executionPolicy = p
	return c, nil
}

const (
	PurposeWebLocal    ConnectionPurpose = "web_local"
	PurposeAgentRemote ConnectionPurpose = "agent_remote"
)

func (p ConnectionPurpose) valid() bool {
	return p == PurposeWebLocal || p == PurposeAgentRemote
}

func (c Connection) Purpose() ConnectionPurpose {
	if c.purpose == "" {
		return PurposeWebLocal
	}
	return c.purpose
}

// WithPurpose requires the containing Config to use schema v2 when serialized.
// Older readers reject that schema instead of silently ignoring new intent.
func (c Connection) WithPurpose(p ConnectionPurpose) (Connection, error) {
	if !p.valid() {
		return Connection{}, invalid("connection.purpose", "unsupported purpose")
	}
	c.purpose = p
	return c, nil
}
