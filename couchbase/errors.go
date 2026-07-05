package couchbase

import "errors"

var (
	// ErrNotFound is returned when a requested document does not exist.
	ErrNotFound = errors.New("couchbase: not found")
	// ErrAlreadyExists is returned by Insert when a document already exists.
	ErrAlreadyExists = errors.New("couchbase: already exists")
)
