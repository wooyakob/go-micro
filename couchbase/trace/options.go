package trace

// DefaultScope and DefaultCollection are where NewExporter and NewStore
// keep spans unless overridden, echoing agentc's "agent_activity" scope for
// run history.
const (
	DefaultScope      = "agent_activity"
	DefaultCollection = "spans"
)

type options struct {
	scope, collection string
}

// Option configures an Exporter or Store.
type Option func(*options)

// InScope overrides the Couchbase scope spans are stored under.
func InScope(scope string) Option {
	return func(o *options) { o.scope = scope }
}

// InCollection overrides the Couchbase collection spans are stored under.
func InCollection(name string) Option {
	return func(o *options) { o.collection = name }
}

func newOptions(opts ...Option) options {
	o := options{scope: DefaultScope, collection: DefaultCollection}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}
