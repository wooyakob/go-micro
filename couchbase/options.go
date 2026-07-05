package couchbase

import "time"

// Options configure a connection to a Couchbase cluster.
type Options struct {
	// ConnectionString is the Couchbase connection string, e.g.
	// "couchbases://cb.xxxxx.cloud.couchbase.com" for Capella or
	// "couchbase://127.0.0.1" for a self-managed cluster.
	ConnectionString string
	// Username and Password authenticate against the cluster.
	Username string
	Password string
	// Bucket is the bucket every scope/collection in this package is created under.
	Bucket string
	// WanProfile applies gocb's WAN development timeout profile, recommended
	// when connecting to Capella from outside its own network.
	WanProfile bool
	// ConnectTimeout bounds how long Connect waits for the bucket to become ready.
	ConnectTimeout time.Duration
}

// Option configures Options.
type Option func(*Options)

// ConnectionString sets the Couchbase connection string.
func ConnectionString(s string) Option {
	return func(o *Options) { o.ConnectionString = s }
}

// Credentials sets the username and password used to authenticate.
func Credentials(username, password string) Option {
	return func(o *Options) { o.Username, o.Password = username, password }
}

// Bucket sets the bucket every scope/collection is created under.
func Bucket(name string) Option {
	return func(o *Options) { o.Bucket = name }
}

// WanDevelopmentProfile applies gocb's WAN development timeout profile.
// Recommended when connecting to a Capella cluster from a different network
// than the one it runs in.
func WanDevelopmentProfile() Option {
	return func(o *Options) { o.WanProfile = true }
}

// ConnectTimeout bounds how long Connect waits for the bucket to become ready.
func ConnectTimeout(d time.Duration) Option {
	return func(o *Options) { o.ConnectTimeout = d }
}

func newOptions(opts ...Option) Options {
	options := Options{ConnectTimeout: 10 * time.Second}
	for _, o := range opts {
		o(&options)
	}
	return options
}

// WriteOptions configure a single Collection write.
type WriteOptions struct {
	// Expiry is the document TTL. Zero means the document never expires.
	Expiry time.Duration
}

// WriteOption configures WriteOptions.
type WriteOption func(*WriteOptions)

// WithExpiry sets the document TTL for a write.
func WithExpiry(d time.Duration) WriteOption {
	return func(o *WriteOptions) { o.Expiry = d }
}

func newWriteOptions(opts ...WriteOption) WriteOptions {
	var options WriteOptions
	for _, o := range opts {
		o(&options)
	}
	return options
}
