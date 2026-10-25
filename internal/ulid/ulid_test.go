package ulid

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFromReader(t *testing.T) {
	// The ULID spec's example ID 01ARZ3NDEKTSV4RRFFQ69G5FAV has this time.
	ts := time.UnixMilli(1469922850259)
	id, err := FromReader(ts, bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if id != "01ARZ3NDEK"+strings.Repeat("0", 16) {
		t.Errorf("id = %s", id)
	}
	id, _ = FromReader(ts, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if id != "01ARZ3NDEK"+strings.Repeat("Z", 16) {
		t.Errorf("id = %s", id)
	}
	if _, err := FromReader(ts, bytes.NewReader(nil)); err == nil {
		t.Error("short read should fail")
	}
}

func TestNewSorts(t *testing.T) {
	a := New(time.UnixMilli(1000))
	b := New(time.UnixMilli(2000))
	if len(a) != 26 || a >= b || a == New(time.UnixMilli(1000)) {
		t.Errorf("a = %s, b = %s", a, b)
	}
}
