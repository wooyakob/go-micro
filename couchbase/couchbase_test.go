package couchbase

import "testing"

func TestConnectRequiresConnectionString(t *testing.T) {
	_, err := Connect(Bucket("agents"))
	if err == nil {
		t.Fatal("expected error when ConnectionString is missing")
	}
}

func TestConnectRequiresBucket(t *testing.T) {
	_, err := Connect(ConnectionString("couchbase://127.0.0.1"))
	if err == nil {
		t.Fatal("expected error when Bucket is missing")
	}
}

func TestNewOptionsDefaults(t *testing.T) {
	o := newOptions(ConnectionString("couchbase://127.0.0.1"), Bucket("agents"), Credentials("u", "p"))
	if o.ConnectionString != "couchbase://127.0.0.1" {
		t.Errorf("ConnectionString = %q", o.ConnectionString)
	}
	if o.Bucket != "agents" {
		t.Errorf("Bucket = %q", o.Bucket)
	}
	if o.Username != "u" || o.Password != "p" {
		t.Errorf("Credentials = %q/%q", o.Username, o.Password)
	}
	if o.ConnectTimeout == 0 {
		t.Error("expected a non-zero default ConnectTimeout")
	}
}

func TestWriteOptions(t *testing.T) {
	o := newWriteOptions()
	if o.Expiry != 0 {
		t.Errorf("expected zero default Expiry, got %v", o.Expiry)
	}
}
